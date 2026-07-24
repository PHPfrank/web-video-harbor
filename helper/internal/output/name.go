package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/sys/unix"
)

// Eighty CJK runes plus an extension and collision suffix remain below the
// common 255-byte file-name limit on macOS filesystems.
const maxBaseNameRunes = 80

const (
	maxFilenameComponentBytes = 255
	maxExtensionBytes         = 8
)

var allowedExtensions = map[string]struct{}{
	".m4v":  {},
	".mkv":  {},
	".mp4":  {},
	".ts":   {},
	".webm": {},
}

var errReservationFinalized = errors.New("output reservation is already finalized")

// Reservation owns an exclusively created output file until it is published
// or released. Write through File so no path lookup can replace the reserved
// file before publication.
type Reservation struct {
	path      string
	name      string
	file      *os.File
	directory *os.File

	mu        sync.Mutex
	finalized bool
}

func (r *Reservation) Path() string {
	return r.path
}

func (r *Reservation) File() *os.File {
	return r.file
}

// Publish synchronizes and closes the reserved file, leaving it at Path.
func (r *Reservation) Publish() error {
	return r.publishExpected(-1)
}

// PublishExpected publishes only when the private owned file has exactly the
// expected size and still occupies its fd-relative reserved directory entry.
func (r *Reservation) PublishExpected(expectedSize int64) error {
	if expectedSize < 0 {
		return errors.New("expected published size is invalid")
	}
	return r.publishExpected(expectedSize)
}

func (r *Reservation) publishExpected(expectedSize int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return errReservationFinalized
	}
	if err := r.file.Sync(); err != nil {
		return fmt.Errorf("sync reserved output: %w", err)
	}
	openedInfo, err := r.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect open reservation: %w", err)
	}
	pathInfo, err := r.inspectPath()
	if err != nil {
		return fmt.Errorf("inspect reserved output path: %w", err)
	}
	if !validPublishedReservation(openedInfo, pathInfo, expectedSize) {
		return errors.New("reserved output path is not the private owned file")
	}
	err = r.file.Close()
	r.finalized = true
	if err != nil {
		return errors.Join(fmt.Errorf("close reserved output: %w", err), r.closeDirectory())
	}
	pathInfo, err = r.inspectPath()
	if err != nil {
		return errors.Join(fmt.Errorf("reinspect published output path: %w", err), r.closeDirectory())
	}
	if !validPublishedReservation(openedInfo, pathInfo, expectedSize) {
		return errors.Join(errors.New("published output path no longer names the private owned file"), r.closeDirectory())
	}
	return r.closeDirectory()
}

func validPublishedReservation(openedInfo, pathInfo os.FileInfo, expectedSize int64) bool {
	sizeMatches := openedInfo != nil && pathInfo != nil && openedInfo.Size() == pathInfo.Size()
	if expectedSize >= 0 {
		sizeMatches = sizeMatches && openedInfo.Size() == expectedSize
	}
	return openedInfo != nil && pathInfo != nil &&
		openedInfo.Mode().IsRegular() && pathInfo.Mode().IsRegular() &&
		openedInfo.Mode().Perm() == 0o600 && pathInfo.Mode().Perm() == 0o600 &&
		sizeMatches && os.SameFile(openedInfo, pathInfo)
}

// Release closes and removes an unpublished reservation. It refuses to remove
// the path if it no longer names the originally reserved file.
func (r *Reservation) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return errReservationFinalized
	}

	openedInfo, err := r.file.Stat()
	if err != nil {
		closeErr := r.file.Close()
		directoryErr := r.closeDirectory()
		r.finalized = true
		return errors.Join(fmt.Errorf("inspect open reservation: %w", err), closeErr, directoryErr)
	}
	pathInfo, err := r.inspectPath()
	if err != nil {
		closeErr := r.file.Close()
		directoryErr := r.closeDirectory()
		r.finalized = true
		if errors.Is(err, os.ErrNotExist) {
			return errors.Join(closeErr, directoryErr)
		}
		return errors.Join(fmt.Errorf("inspect reserved output path: %w", err), closeErr, directoryErr)
	}
	if !os.SameFile(openedInfo, pathInfo) {
		closeErr := r.file.Close()
		directoryErr := r.closeDirectory()
		r.finalized = true
		return errors.Join(errors.New("reserved output path no longer names the owned file"), closeErr, directoryErr)
	}

	removeErr := r.removePath()
	closeErr := r.file.Close()
	directoryErr := r.closeDirectory()
	r.finalized = true
	if removeErr != nil {
		removeErr = fmt.Errorf("remove reserved output: %w", removeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close reserved output: %w", closeErr)
	}
	return errors.Join(removeErr, closeErr, directoryErr)
}

func (r *Reservation) inspectPath() (os.FileInfo, error) {
	if r.directory == nil {
		return os.Lstat(r.path)
	}
	fd, err := unix.Openat(
		int(r.directory.Fd()),
		r.name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), r.name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open reserved output path")
	}
	defer file.Close()
	return file.Stat()
}

func (r *Reservation) removePath() error {
	if r.directory == nil {
		return os.Remove(r.path)
	}
	return unix.Unlinkat(int(r.directory.Fd()), r.name, 0)
}

func (r *Reservation) closeDirectory() error {
	if r.directory == nil {
		return nil
	}
	err := r.directory.Close()
	r.directory = nil
	return err
}

// SanitizeBaseName produces a readable single path component while preserving
// non-ASCII text such as Chinese titles.
func SanitizeBaseName(name string) string {
	name = strings.TrimSpace(name)
	result := make([]rune, 0, len([]rune(name)))
	lastWasReplacement := false

	for _, r := range name {
		if len(result) >= maxBaseNameRunes {
			break
		}
		if unicode.IsControl(r) {
			continue
		}
		if len(result) == 0 && (r == '.' || unicode.IsSpace(r)) {
			continue
		}
		if isInvalidFilenameRune(r) {
			if len(result) > 0 && !lastWasReplacement {
				result = append(result, '-')
				lastWasReplacement = true
			}
			continue
		}
		result = append(result, r)
		lastWasReplacement = false
	}

	cleaned := strings.TrimRightFunc(string(result), func(r rune) bool {
		return unicode.IsSpace(r) || r == '.' || r == '-'
	})
	if cleaned == "" {
		return "视频"
	}
	return cleaned
}

// NextAvailablePath atomically reserves a unique 0600 placeholder below dir.
// The caller owns that placeholder and must either write/publish it or remove
// it. New code that writes in-process should prefer ReserveAvailablePath and
// its open file handle.
func NextAvailablePath(dir, base, ext string) (string, error) {
	reservation, err := ReserveAvailablePath(dir, base, ext)
	if err != nil {
		return "", err
	}
	path := reservation.Path()
	if err := reservation.Publish(); err != nil {
		_ = reservation.Release()
		return "", err
	}
	return path, nil
}

// ReserveAvailablePath atomically creates and returns a unique output
// reservation below dir.
func ReserveAvailablePath(dir, base, ext string) (*Reservation, error) {
	cleanExt, err := normalizeExtension(ext)
	if err != nil {
		return nil, err
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve output directory: %w", err)
	}
	directory, err := os.OpenFile(absDir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open output directory: %w", err)
	}
	defer directory.Close()
	return ReserveAvailablePathAt(directory, absDir, base, cleanExt)
}

// ReserveAvailablePathAt atomically reserves a unique 0600 output below a
// pinned directory handle. dir is used only as the display path returned by
// Reservation.Path and must still name directory when the reservation starts.
func ReserveAvailablePathAt(directory *os.File, dir, base, ext string) (*Reservation, error) {
	cleanExt, err := normalizeExtension(ext)
	if err != nil {
		return nil, err
	}
	if directory == nil {
		return nil, errors.New("output directory handle is missing")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve output directory: %w", err)
	}
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() {
		return nil, errors.New("output directory handle is invalid")
	}
	pathInfo, err := os.Lstat(absDir)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return nil, errors.New("output path no longer names the pinned directory")
	}
	ownedDirectoryFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate output directory handle: %w", err)
	}
	unix.CloseOnExec(ownedDirectoryFD)
	ownedDirectory := os.NewFile(uintptr(ownedDirectoryFD), filepath.Base(absDir))
	if ownedDirectory == nil {
		_ = unix.Close(ownedDirectoryFD)
		return nil, errors.New("duplicate output directory handle")
	}

	cleanBase := SanitizeBaseName(base)
	for index := 1; ; index++ {
		suffix := ""
		if index > 1 {
			suffix = fmt.Sprintf(" (%d)", index)
		}
		baseBudget := maxFilenameComponentBytes - len(suffix) - len(cleanExt)
		if baseBudget < 1 {
			return nil, errors.New("file extension and collision suffix leave no room for a base name")
		}
		candidateBase := truncateUTF8(cleanBase, baseBudget)
		candidate := filepath.Join(absDir, candidateBase+suffix+cleanExt)
		if !isWithinDirectory(absDir, candidate) {
			return nil, errors.New("output path escapes output directory")
		}

		candidateName := filepath.Base(candidate)
		fd, err := unix.Openat(
			int(ownedDirectory.Fd()),
			candidateName,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		switch {
		case err == nil:
			file := os.NewFile(uintptr(fd), candidateName)
			if file == nil {
				_ = unix.Close(fd)
				_ = ownedDirectory.Close()
				return nil, errors.New("open reserved output")
			}
			return &Reservation{path: candidate, name: candidateName, file: file, directory: ownedDirectory}, nil
		case errors.Is(err, os.ErrExist):
			continue
		default:
			_ = ownedDirectory.Close()
			return nil, fmt.Errorf("reserve output path: %w", err)
		}
	}
}

func isInvalidFilenameRune(r rune) bool {
	return strings.ContainsRune(`/\\:*?"<>|`, r)
}

func normalizeExtension(ext string) (string, error) {
	if ext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	ext = strings.ToLower(ext)
	if len(ext) < 2 {
		return "", errors.New("file extension is empty")
	}
	if len(ext) > maxExtensionBytes {
		return "", errors.New("file extension is too long")
	}
	if _, ok := allowedExtensions[ext]; !ok {
		return "", errors.New("file extension is not an allowed video format")
	}
	return ext, nil
}

func truncateUTF8(value string, byteLimit int) string {
	if len(value) <= byteLimit {
		return value
	}
	used := 0
	for index, r := range value {
		runeBytes := len(string(r))
		if used+runeBytes > byteLimit {
			return strings.TrimRightFunc(value[:index], func(r rune) bool {
				return unicode.IsSpace(r) || r == '.' || r == '-'
			})
		}
		used += runeBytes
	}
	return value
}

func isWithinDirectory(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
