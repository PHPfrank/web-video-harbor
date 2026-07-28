// Package settings persists the helper's local, consent-gated feature settings.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const (
	CurrentPlatformNoticeVersion = "2026-07-28-v1"
	maxSettingsSize              = 64 << 10
	maxNoticeVersionLength       = 64
)

var (
	writeSettingsData = func(file *os.File, data []byte) (int, error) {
		return file.Write(data)
	}
	syncSettingsFile = func(file *os.File) error {
		return file.Sync()
	}
	publishSettingsFile = func(oldPath, newPath string) error {
		return os.Rename(oldPath, newPath)
	}
	syncSettingsParent = syncDirectory
)

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
}

// Open returns a usable store. Missing or unsafe settings fail closed.
func Open(path string) *Store {
	s := &Store{}
	cleaned, err := cleanSettingsPath(path)
	if err != nil {
		s.loadErr = err
		return s
	}
	s.path = cleaned
	s.snapshot, s.loadErr = load(cleaned)
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
	if err := writeSnapshot(s.path, desired); err != nil {
		return s.snapshot, err
	}
	s.snapshot = desired
	s.loadErr = nil
	return s.snapshot, nil
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

func load(path string) (Snapshot, error) {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect settings parent: %w", err)
	}
	if err := validateParent(parentInfo); err != nil {
		return Snapshot{}, err
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect settings: %w", err)
	}
	if err := validateSettingsFile(info); err != nil {
		return Snapshot{}, err
	}
	if info.Size() > maxSettingsSize {
		return Snapshot{}, fmt.Errorf("settings file is too large: maximum size is %d bytes", maxSettingsSize)
	}

	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open settings: %w", err)
	}
	defer file.Close()

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

func validateParent(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("settings parent must not be a symbolic link")
	}
	if !info.IsDir() {
		return errors.New("settings parent is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("settings parent permissions %04o are insecure; expected 0700", info.Mode().Perm())
	}
	if err := validateOwner(info, "settings parent"); err != nil {
		return err
	}
	return nil
}

func validateSettingsFile(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("settings path must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("settings path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("settings permissions %04o are insecure; group and other permissions are forbidden", info.Mode().Perm())
	}
	if err := validateOwner(info, "settings file"); err != nil {
		return err
	}
	return nil
}

func validateOwner(info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s owner could not be determined", label)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s owner %d does not match current user %d", label, stat.Uid, os.Getuid())
	}
	return nil
}

func prepareParent(parent string) error {
	info, err := os.Lstat(parent)
	if err == nil {
		return validateParent(info)
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
		return validateParent(info)
	}
	if err := validateOwner(info, "settings parent"); err != nil {
		return err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("secure settings parent permissions: %w", err)
	}
	info, err = os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("verify secured settings parent: %w", err)
	}
	return validateParent(info)
}

func inspectReplaceTarget(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect settings: %w", err)
	}
	if err := validateSettingsFile(info); err != nil {
		return false, err
	}
	return true, nil
}

func writeSnapshot(path string, snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')

	parent := filepath.Dir(path)
	if err := prepareParent(parent); err != nil {
		return err
	}
	if _, err := inspectReplaceTarget(path); err != nil {
		return err
	}

	file, err := os.CreateTemp(parent, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tempPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = os.Remove(tempPath)
	}()

	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary settings permissions: %w", err)
	}
	written, err := writeSettingsData(file, data)
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write settings: %w", io.ErrShortWrite)
	}
	if err := syncSettingsFile(file); err != nil {
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close settings: %w", err)
	}
	closed = true

	if err := validateParentAgain(parent); err != nil {
		return err
	}
	existed, err := inspectReplaceTarget(path)
	if err != nil {
		return err
	}
	backupPath := ""
	if existed {
		backupPath, err = makeBackupLink(parent, path)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(backupPath) }()
	}

	if err := publishSettingsFile(tempPath, path); err != nil {
		return fmt.Errorf("publish settings: %w", err)
	}
	if err := syncSettingsParent(parent); err != nil {
		if rollbackErr := rollbackPublish(parent, path, backupPath, existed); rollbackErr != nil {
			return fmt.Errorf("sync settings parent: %v (rollback failed: %w)", err, rollbackErr)
		}
		backupPath = ""
		return fmt.Errorf("sync settings parent: %w", err)
	}
	if backupPath != "" {
		if err := os.Remove(backupPath); err != nil {
			return fmt.Errorf("remove settings backup: %w", err)
		}
		backupPath = ""
		if err := syncDirectory(parent); err != nil {
			return fmt.Errorf("sync settings parent after cleanup: %w", err)
		}
	}
	return nil
}

func validateParentAgain(parent string) error {
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("reinspect settings parent: %w", err)
	}
	return validateParent(info)
}

func makeBackupLink(parent, path string) (string, error) {
	placeholder, err := os.CreateTemp(parent, "."+filepath.Base(path)+"-backup-*.tmp")
	if err != nil {
		return "", fmt.Errorf("reserve settings backup: %w", err)
	}
	backupPath := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("close settings backup placeholder: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return "", fmt.Errorf("prepare settings backup: %w", err)
	}
	if err := os.Link(path, backupPath); err != nil {
		return "", fmt.Errorf("create settings backup: %w", err)
	}
	return backupPath, nil
}

func rollbackPublish(parent, path, backupPath string, existed bool) error {
	if existed {
		if backupPath == "" {
			return errors.New("settings backup is unavailable")
		}
		if err := os.Rename(backupPath, path); err != nil {
			return fmt.Errorf("restore previous settings: %w", err)
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unpublished settings: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync restored settings parent: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
