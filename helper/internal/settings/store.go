// Package settings persists the helper's local, consent-gated feature settings.
package settings

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	CurrentPlatformNoticeVersion = "2026-07-28-v1"
	maxSettingsSize              = 64 << 10
	maxNoticeVersionLength       = 64
)

type storeOps struct {
	writeData     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	syncDir       func(int) error
	renameAt      func(int, string, string) error
	linkAt        func(int, string, string) error
	unlinkAt      func(int, string) error
	flock         func(int, int) error
	beforePublish func(int, string) error
}

func defaultStoreOps() storeOps {
	return storeOps{
		writeData: func(file *os.File, data []byte) (int, error) {
			return file.Write(data)
		},
		syncFile: func(file *os.File) error {
			return file.Sync()
		},
		syncDir: func(dirFD int) error {
			return unix.Fsync(dirFD)
		},
		renameAt: func(dirFD int, oldName, newName string) error {
			return unix.Renameat(dirFD, oldName, dirFD, newName)
		},
		linkAt: func(dirFD int, oldName, newName string) error {
			return unix.Linkat(dirFD, oldName, dirFD, newName, 0)
		},
		unlinkAt: func(dirFD int, name string) error {
			return unix.Unlinkat(dirFD, name, 0)
		},
		flock: func(fd, how int) error {
			return unix.Flock(fd, how)
		},
		beforePublish: func(int, string) error { return nil },
	}
}

// Snapshot is the complete persisted compatibility setting.
type Snapshot struct {
	ExperimentalPlatformCompatibilityEnabled bool   `json:"experimental_platform_compatibility_enabled"`
	PlatformNoticeVersion                    string `json:"platform_notice_version,omitempty"`
}

// Store is a concurrency-safe view of a settings file.
type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot Snapshot
	loadErr  error
	ops      storeOps
}

// Open returns a usable store. Missing or unsafe settings fail closed.
func Open(path string) *Store {
	return openWithOps(path, defaultStoreOps())
}

func openWithOps(path string, ops storeOps) *Store {
	s := &Store{ops: ops}
	cleaned, err := cleanSettingsPath(path)
	if err != nil {
		s.loadErr = err
		return s
	}
	s.path = cleaned
	s.snapshot, s.loadErr = load(cleaned, ops)
	if s.loadErr != nil {
		s.snapshot = Snapshot{}
	}
	return s
}

// PathForConfig returns settings.json next to the selected config file.
func PathForConfig(configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", errors.New("config path is empty")
	}
	if strings.ContainsRune(configPath, 0) {
		return "", errors.New("config path contains a null byte")
	}
	if strings.HasSuffix(configPath, string(filepath.Separator)) {
		return "", errors.New("config path names a directory")
	}

	cleaned := filepath.Clean(configPath)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "", errors.New("config path must name a file")
	}
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if info, err := os.Lstat(absolute); err == nil && info.IsDir() {
		return "", errors.New("config path names a directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect config path: %w", err)
	}
	return filepath.Join(filepath.Dir(absolute), "settings.json"), nil
}

// Snapshot returns a complete in-memory copy of the current setting.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Enabled reports whether the user has acknowledged the current notice.
func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.ExperimentalPlatformCompatibilityEnabled
}

// SetPlatformCompatibility atomically persists an explicit local choice.
func (s *Store) SetPlatformCompatibility(enabled bool, noticeVersion string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		return s.snapshot, errors.New("settings path is unavailable")
	}
	desired := Snapshot{}
	if enabled {
		if err := validateNoticeVersion(noticeVersion); err != nil {
			return s.snapshot, err
		}
		desired = Snapshot{
			ExperimentalPlatformCompatibilityEnabled: true,
			PlatformNoticeVersion:                    CurrentPlatformNoticeVersion,
		}
	}
	committed, err := writeSnapshot(s.path, desired, s.ops)
	if committed {
		s.snapshot = desired
		s.loadErr = nil
		return s.snapshot, nil
	}
	if err != nil {
		return s.snapshot, err
	}
	return s.snapshot, errors.New("settings update did not reach a terminal state")
}

func cleanSettingsPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("settings path is empty")
	}
	if strings.ContainsRune(path, 0) {
		return "", errors.New("settings path contains a null byte")
	}
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve settings path: %w", err)
	}
	if filepath.Clean(cleaned) == string(filepath.Separator) {
		return "", errors.New("settings path must name a file")
	}
	return filepath.Clean(cleaned), nil
}

func validateNoticeVersion(version string) error {
	if version == "" {
		return errors.New("platform notice version is required")
	}
	if len(version) > maxNoticeVersionLength {
		return errors.New("platform notice version is too long")
	}
	for _, r := range version {
		if r < 0x21 || r > 0x7e {
			return errors.New("platform notice version must be printable ASCII")
		}
	}
	if version != CurrentPlatformNoticeVersion {
		return errors.New("platform notice version is stale")
	}
	return nil
}

func load(path string, ops storeOps) (Snapshot, error) {
	parent, err := openVerifiedParent(filepath.Dir(path), false)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer parent.Close()
	if err := ops.flock(int(parent.Fd()), unix.LOCK_SH); err != nil {
		return Snapshot{}, fmt.Errorf("lock settings parent for reading: %w", err)
	}
	defer func() { _ = ops.flock(int(parent.Fd()), unix.LOCK_UN) }()

	base := filepath.Base(path)
	stat, exists, err := statAt(int(parent.Fd()), base)
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, nil
	}
	if err := validateSettingsStat(stat); err != nil {
		return Snapshot{}, err
	}
	if stat.Size > maxSettingsSize {
		return Snapshot{}, fmt.Errorf("settings file is too large: maximum size is %d bytes", maxSettingsSize)
	}

	file, openedStat, err := openRegularAt(int(parent.Fd()), base)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	if !sameObject(stat, openedStat) {
		return Snapshot{}, errors.New("settings file changed while opening")
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxSettingsSize+1))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode settings: %w", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.ExperimentalPlatformCompatibilityEnabled {
		return validateNoticeVersion(snapshot.PlatformNoticeVersion)
	}
	if snapshot.PlatformNoticeVersion != "" {
		return errors.New("disabled settings must not retain a platform notice version")
	}
	return nil
}

func prepareParent(parent string) error {
	info, err := os.Lstat(parent)
	if err == nil {
		return validateParentInfo(info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect settings parent: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create settings parent: %w", err)
	}
	info, err = os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("verify settings parent: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return validateParentInfo(info)
	}
	if err := validateOwnerInfo(info, "settings parent"); err != nil {
		return err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("secure settings parent permissions: %w", err)
	}
	info, err = os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("verify secured settings parent: %w", err)
	}
	return validateParentInfo(info)
}

func validateParentInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("settings parent must not be a symbolic link")
	}
	if !info.IsDir() {
		return errors.New("settings parent is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("settings parent permissions %04o are insecure; expected 0700", info.Mode().Perm())
	}
	return validateOwnerInfo(info, "settings parent")
}

func validateOwnerInfo(info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s owner could not be determined", label)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s owner %d does not match current user %d", label, stat.Uid, os.Getuid())
	}
	return nil
}

func openVerifiedParent(parent string, create bool) (*os.File, error) {
	if create {
		if err := prepareParent(parent); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("open settings parent without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), parent)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened settings parent: %w", err)
	}
	if err := validateParentStat(stat); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateParentStat(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("settings parent is not a directory")
	}
	if stat.Mode&0o777 != 0o700 {
		return fmt.Errorf("settings parent permissions %04o are insecure; expected 0700", stat.Mode&0o777)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("settings parent owner %d does not match current user %d", stat.Uid, os.Getuid())
	}
	return nil
}

func statAt(dirFD int, base string) (unix.Stat_t, bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(dirFD, base, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return unix.Stat_t{}, false, nil
	}
	if err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("inspect settings entry: %w", err)
	}
	return stat, true, nil
}

func validateSettingsStat(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errors.New("settings path must not be a symbolic link")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("settings path is not a regular file")
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("settings permissions %04o are insecure; group and other permissions are forbidden", stat.Mode&0o777)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("settings file owner %d does not match current user %d", stat.Uid, os.Getuid())
	}
	return nil
}

func openRegularAt(dirFD int, base string) (*os.File, unix.Stat_t, error) {
	fd, err := unix.Openat(dirFD, base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, fmt.Errorf("open settings without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), base)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("inspect opened settings: %w", err)
	}
	if err := validateSettingsStat(stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	return file, stat, nil
}

func sameObject(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func writeSnapshot(path string, snapshot Snapshot, ops storeOps) (bool, error) {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')

	parentPath := filepath.Dir(path)
	parent, err := openVerifiedParent(parentPath, true)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	dirFD := int(parent.Fd())
	// The parent is private and owned by this user. All helper processes use an
	// advisory lock on this same opened directory, defining one cooperative
	// writer at a time; basename-only operations below never re-resolve parent.
	if err := ops.flock(dirFD, unix.LOCK_EX); err != nil {
		return false, fmt.Errorf("lock settings parent for writing: %w", err)
	}
	defer func() { _ = ops.flock(dirFD, unix.LOCK_UN) }()

	base := filepath.Base(path)
	initial, existed, err := statAt(dirFD, base)
	if err != nil {
		return false, err
	}
	if existed {
		if err := validateSettingsStat(initial); err != nil {
			return false, err
		}
	}

	tempName, file, err := createTempAt(dirFD, parentPath, base)
	if err != nil {
		return false, err
	}
	tempOwned := true
	defer func() {
		if tempOwned {
			_ = ops.unlinkAt(dirFD, tempName)
		}
	}()
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if written, err := ops.writeData(file, data); err != nil {
		return false, fmt.Errorf("write settings: %w", err)
	} else if written != len(data) {
		return false, fmt.Errorf("write settings: %w", io.ErrShortWrite)
	}
	if err := ops.syncFile(file); err != nil {
		return false, fmt.Errorf("sync settings: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return false, fmt.Errorf("close settings: %w", err)
	}
	closed = true

	backupName := ""
	backupOwned := false
	backupCleanupAllowed := true
	defer func() {
		if backupOwned && backupCleanupAllowed {
			_ = ops.unlinkAt(dirFD, backupName)
		}
	}()
	if existed {
		backupName, err = makeBackupLinkAt(dirFD, base, initial, ops)
		if err != nil {
			return false, err
		}
		backupOwned = true
	}
	if err := ops.beforePublish(dirFD, base); err != nil {
		return false, fmt.Errorf("prepare settings publish: %w", err)
	}
	if err := verifyTargetUnchanged(dirFD, base, initial, existed); err != nil {
		// If a non-cooperating same-user process displaced the target, the
		// hard link may now be the only recoverable copy of the old value.
		backupCleanupAllowed = false
		return false, err
	}
	if err := ops.renameAt(dirFD, tempName, base); err != nil {
		if verifyTargetUnchanged(dirFD, base, initial, existed) != nil {
			backupCleanupAllowed = false
		}
		return false, fmt.Errorf("publish settings: %w", err)
	}
	tempOwned = false
	backupCleanupAllowed = false

	if err := ops.syncDir(dirFD); err != nil {
		rollbackErr, recovered := rollbackPublishAt(dirFD, base, backupName, existed, ops)
		if recovered {
			backupOwned = false
			backupCleanupAllowed = true
		}
		if rollbackErr != nil {
			return false, fmt.Errorf("sync settings parent: %v (rollback failed: %w)", err, rollbackErr)
		}
		return false, fmt.Errorf("sync settings parent: %w", err)
	}

	// The rename is durable at this point. Cleanup failures must not turn a
	// committed value into an ordinary write failure or leave memory stale.
	backupCleanupAllowed = true
	if backupOwned {
		if err := ops.unlinkAt(dirFD, backupName); err == nil {
			backupOwned = false
		} else if retryErr := ops.unlinkAt(dirFD, backupName); retryErr == nil {
			backupOwned = false
		}
	}
	if existed && !backupOwned {
		if err := ops.syncDir(dirFD); err != nil {
			_ = ops.syncDir(dirFD)
		}
	}
	return true, nil
}

func createTempAt(dirFD int, parentPath, base string) (string, *os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 12)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", nil, fmt.Errorf("generate temporary settings name: %w", err)
		}
		name := "." + base + "-" + base64.RawURLEncoding.EncodeToString(random) + ".tmp"
		fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create temporary settings: %w", err)
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(dirFD, name, 0)
			return "", nil, fmt.Errorf("secure temporary settings permissions: %w", err)
		}
		return name, os.NewFile(uintptr(fd), filepath.Join(parentPath, name)), nil
	}
	return "", nil, errors.New("create temporary settings: name collisions exhausted")
}

func makeBackupLinkAt(dirFD int, base string, expected unix.Stat_t, ops storeOps) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 12)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", fmt.Errorf("generate settings backup name: %w", err)
		}
		name := "." + base + "-backup-" + base64.RawURLEncoding.EncodeToString(random) + ".bak"
		err := ops.linkAt(dirFD, base, name)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create settings backup: %w", err)
		}
		backup, exists, statErr := statAt(dirFD, name)
		if statErr != nil || !exists {
			_ = ops.unlinkAt(dirFD, name)
			if statErr != nil {
				return "", fmt.Errorf("inspect settings backup: %w", statErr)
			}
			return "", errors.New("settings backup disappeared")
		}
		if err := validateSettingsStat(backup); err != nil || !sameObject(expected, backup) {
			_ = ops.unlinkAt(dirFD, name)
			return "", errors.New("settings target changed while creating backup")
		}
		return name, nil
	}
	return "", errors.New("create settings backup: name collisions exhausted")
}

func verifyTargetUnchanged(dirFD int, base string, expected unix.Stat_t, expectedExists bool) error {
	current, exists, err := statAt(dirFD, base)
	if err != nil {
		return err
	}
	if exists != expectedExists {
		return errors.New("settings target changed before publish")
	}
	if !exists {
		return nil
	}
	if err := validateSettingsStat(current); err != nil {
		return err
	}
	if !sameObject(expected, current) {
		return errors.New("settings target identity changed before publish")
	}
	return nil
}

func rollbackPublishAt(dirFD int, base, backupName string, existed bool, ops storeOps) (error, bool) {
	if existed {
		if backupName == "" {
			return errors.New("settings recovery backup is unavailable"), false
		}
		if err := ops.renameAt(dirFD, backupName, base); err != nil {
			return fmt.Errorf("restore previous settings: %w", err), false
		}
	} else if err := ops.unlinkAt(dirFD, base); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove unpublished settings: %w", err), false
	}
	if err := ops.syncDir(dirFD); err != nil {
		return fmt.Errorf("sync restored settings parent: %w", err), true
	}
	return nil, true
}
