package ytdlp

import (
	"crypto/rand"
	"crypto/sha256"
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
	snapshotDirectoryPrefix = ".web-video-harbor-parser-"
	snapshotCreateAttempts  = 32
)

// ExecutableSnapshot pins the private process-lifetime copy used for both the
// version probe and downloads. The random 0700 directory constrains pathname
// replacement by other users; this is not a sandbox against an active process
// running as the same UID.
type ExecutableSnapshot struct {
	mu sync.Mutex

	parent        *os.File
	parentPath    string
	parentInfo    os.FileInfo
	directory     *os.File
	directoryName string
	directoryInfo os.FileInfo
	file          *os.File
	fileInfo      os.FileInfo
	fileName      string
	path          string
	size          int64
	digest        [sha256.Size]byte
	active        int
	closed        bool
}

func createExecutableSnapshot(sourcePath, snapshotFileName string) (*ExecutableSnapshot, error) {
	if snapshotFileName == "" || snapshotFileName == "." || snapshotFileName == ".." ||
		filepath.Base(snapshotFileName) != snapshotFileName || strings.ContainsAny(snapshotFileName, `/\\`) {
		return nil, errors.New("platform snapshot filename is invalid")
	}
	sourceFD, err := unix.Open(sourcePath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("open bundled platform downloader safely")
	}
	source := os.NewFile(uintptr(sourceFD), filepath.Base(sourcePath))
	if source == nil {
		_ = unix.Close(sourceFD)
		return nil, errors.New("open bundled platform downloader safely")
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil || !validExecutableFile(sourceInfo, 0) {
		return nil, errors.New("bundled platform downloader is not a regular executable")
	}
	pathInfo, err := os.Lstat(sourcePath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(sourceInfo, pathInfo) {
		return nil, errors.New("bundled platform downloader identity changed")
	}

	parentPath := os.TempDir()
	parentPathInfo, err := os.Lstat(parentPath)
	if err != nil || !validPrivateOwnedDirectory(parentPathInfo) {
		return nil, errors.New("platform snapshot parent is unsafe")
	}
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("platform snapshot parent is unsafe")
	}
	parent := os.NewFile(uintptr(parentFD), parentPath)
	if parent == nil {
		_ = unix.Close(parentFD)
		return nil, errors.New("platform snapshot parent is unsafe")
	}
	parentInfo, err := parent.Stat()
	if err != nil || !validPrivateOwnedDirectory(parentInfo) || !os.SameFile(parentPathInfo, parentInfo) {
		_ = parent.Close()
		return nil, errors.New("platform snapshot parent is unsafe")
	}

	directoryName, directory, directoryInfo, err := createSnapshotDirectory(parent)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), snapshotFileName, 0)
			_ = directory.Close()
			_ = unix.Unlinkat(int(parent.Fd()), directoryName, unix.AT_REMOVEDIR)
			_ = parent.Close()
		}
	}()

	fileFD, err := unix.Openat(
		int(directory.Fd()), snapshotFileName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o500,
	)
	if err != nil {
		return nil, errors.New("create private platform snapshot")
	}
	file := os.NewFile(uintptr(fileFD), snapshotFileName)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, errors.New("create private platform snapshot")
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hasher), source)
	syncErr := file.Sync()
	modeErr := file.Chmod(0o500)
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || modeErr != nil || closeErr != nil || size <= 0 {
		return nil, errors.New("copy private platform snapshot")
	}
	if err := directory.Sync(); err != nil {
		return nil, errors.New("sync private platform snapshot")
	}

	openedFD, err := unix.Openat(int(directory.Fd()), snapshotFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("reopen private platform snapshot")
	}
	opened := os.NewFile(uintptr(openedFD), snapshotFileName)
	if opened == nil {
		_ = unix.Close(openedFD)
		return nil, errors.New("reopen private platform snapshot")
	}
	openedInfo, err := opened.Stat()
	if err != nil || !validExecutableFile(openedInfo, 0o500) || openedInfo.Size() != size {
		_ = opened.Close()
		return nil, errors.New("validate private platform snapshot")
	}
	var expectedDigest [sha256.Size]byte
	copy(expectedDigest[:], hasher.Sum(nil))
	digest, err := hashExecutable(opened)
	if err != nil || digest != expectedDigest {
		_ = opened.Close()
		return nil, errors.New("validate private platform snapshot")
	}

	directoryPath := filepath.Join(parentPath, directoryName)
	snapshot := &ExecutableSnapshot{
		parent: parent, parentPath: parentPath, parentInfo: parentInfo,
		directory: directory, directoryName: directoryName, directoryInfo: directoryInfo,
		file: opened, fileInfo: openedInfo,
		fileName: snapshotFileName,
		path: filepath.Join(directoryPath, snapshotFileName),
		size: size, digest: expectedDigest,
	}
	if err := snapshot.verifyLocked(); err != nil {
		_ = opened.Close()
		return nil, err
	}
	cleanup = false
	return snapshot, nil
}

func createSnapshotDirectory(parent *os.File) (string, *os.File, os.FileInfo, error) {
	for attempt := 0; attempt < snapshotCreateAttempts; attempt++ {
		name, err := randomSnapshotDirectoryName()
		if err != nil {
			return "", nil, nil, err
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			if errors.Is(err, syscall.EEXIST) {
				continue
			}
			return "", nil, nil, errors.New("create private platform snapshot directory")
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, nil, errors.New("open private platform snapshot directory")
		}
		directory := os.NewFile(uintptr(fd), name)
		if directory == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, nil, errors.New("open private platform snapshot directory")
		}
		info, err := directory.Stat()
		if err != nil || !validPrivateOwnedDirectory(info) {
			_ = directory.Close()
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, nil, errors.New("validate private platform snapshot directory")
		}
		return name, directory, info, nil
	}
	return "", nil, nil, errors.New("allocate private platform snapshot directory")
}

func randomSnapshotDirectoryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("generate private platform snapshot name")
	}
	return fmt.Sprintf("%s%x", snapshotDirectoryPrefix, random[:]), nil
}

func validPrivateOwnedDirectory(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat != nil && stat.Uid == uint32(os.Geteuid())
}

func validExecutableFile(info os.FileInfo, exactMode os.FileMode) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return false
	}
	if exactMode != 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		return info.Mode().Perm() == exactMode && ok && stat != nil && stat.Uid == uint32(os.Geteuid())
	}
	return info.Mode().Perm()&0o111 != 0
}

func hashExecutable(file *os.File) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if file == nil {
		return digest, errors.New("hash private platform snapshot")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return digest, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return digest, err
	}
	copy(digest[:], hasher.Sum(nil))
	_, err := file.Seek(0, io.SeekStart)
	return digest, err
}

// Path returns the private executable path. Callers must Verify immediately
// before and after Start; Path alone is not an identity guarantee.
func (s *ExecutableSnapshot) Path() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ""
	}
	return s.path
}

// Verify binds the private path to the pinned directory/file descriptors,
// inode, size, mode, and SHA-256 digest.
func (s *ExecutableSnapshot) Verify() error {
	if s == nil {
		return errors.New("platform executable snapshot is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifyLocked()
}

func (s *ExecutableSnapshot) verifyLocked() error {
	if s.closed || s.parent == nil || s.directory == nil || s.file == nil {
		return errors.New("platform executable snapshot is closed")
	}
	parentPathInfo, err := os.Lstat(s.parentPath)
	if err != nil || !validPrivateOwnedDirectory(parentPathInfo) || !os.SameFile(s.parentInfo, parentPathInfo) {
		return errors.New("platform executable snapshot parent changed")
	}
	parentInfo, err := s.parent.Stat()
	if err != nil || !validPrivateOwnedDirectory(parentInfo) || !os.SameFile(s.parentInfo, parentInfo) {
		return errors.New("platform executable snapshot parent changed")
	}
	directoryInfo, err := fileInfoAt(s.parent, s.directoryName)
	if err != nil || !validPrivateOwnedDirectory(directoryInfo) || !os.SameFile(s.directoryInfo, directoryInfo) {
		return errors.New("platform executable snapshot directory changed")
	}
	pinnedDirectoryInfo, err := s.directory.Stat()
	if err != nil || !validPrivateOwnedDirectory(pinnedDirectoryInfo) || !os.SameFile(s.directoryInfo, pinnedDirectoryInfo) {
		return errors.New("platform executable snapshot directory changed")
	}
	pinnedFileInfo, err := s.file.Stat()
	if err != nil || !validExecutableFile(pinnedFileInfo, 0o500) || pinnedFileInfo.Size() != s.size || !os.SameFile(s.fileInfo, pinnedFileInfo) {
		return errors.New("platform executable snapshot file descriptor changed")
	}
	pinnedDigest, err := hashExecutable(s.file)
	if err != nil || pinnedDigest != s.digest {
		return errors.New("platform executable snapshot file descriptor changed")
	}
	fd, err := unix.Openat(int(s.directory.Fd()), s.fileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("platform executable snapshot file changed")
	}
	opened := os.NewFile(uintptr(fd), s.fileName)
	if opened == nil {
		_ = unix.Close(fd)
		return errors.New("platform executable snapshot file changed")
	}
	defer opened.Close()
	info, err := opened.Stat()
	if err != nil || !validExecutableFile(info, 0o500) || info.Size() != s.size || !os.SameFile(s.fileInfo, info) {
		return errors.New("platform executable snapshot file changed")
	}
	digest, err := hashExecutable(opened)
	if err != nil || digest != s.digest {
		return errors.New("platform executable snapshot digest changed")
	}
	return nil
}

// acquire pins the snapshot for one complete Runner invocation. Close refuses
// cleanup while any invocation is active, so timeout paths cannot remove an
// executable that a worker is still using.
func (s *ExecutableSnapshot) acquire() (func(), error) {
	if s == nil {
		return nil, errors.New("platform executable snapshot is unavailable")
	}
	s.mu.Lock()
	if err := s.verifyLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.active++
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.active > 0 {
				s.active--
			}
			s.mu.Unlock()
		})
	}, nil
}

// Close releases pinned descriptors and removes only the still-owned private
// snapshot. Cleanup is best effort; a replaced entry is preserved.
func (s *ExecutableSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.active != 0 {
		return errors.New("platform executable snapshot is active")
	}
	var result error
	if err := s.verifyLocked(); err == nil {
		result = errors.Join(result, unix.Unlinkat(int(s.directory.Fd()), s.fileName, 0))
	} else {
		result = errors.Join(result, err)
	}
	result = errors.Join(result, s.file.Close())
	if directoryInfo, err := fileInfoAt(s.parent, s.directoryName); err == nil && os.SameFile(s.directoryInfo, directoryInfo) {
		result = errors.Join(result, s.directory.Close())
		result = errors.Join(result, unix.Unlinkat(int(s.parent.Fd()), s.directoryName, unix.AT_REMOVEDIR))
	} else {
		result = errors.Join(result, err)
		result = errors.Join(result, s.directory.Close())
	}
	result = errors.Join(result, s.parent.Close())
	s.closed = true
	return result
}
