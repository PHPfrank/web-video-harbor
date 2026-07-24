package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
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
	path string
	file *os.File

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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return errReservationFinalized
	}
	if err := r.file.Sync(); err != nil {
		return fmt.Errorf("sync reserved output: %w", err)
	}
	err := r.file.Close()
	r.finalized = true
	if err != nil {
		return fmt.Errorf("close reserved output: %w", err)
	}
	return nil
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
		return fmt.Errorf("inspect open reservation: %w", err)
	}
	pathInfo, err := os.Lstat(r.path)
	if err != nil {
		closeErr := r.file.Close()
		r.finalized = true
		if errors.Is(err, os.ErrNotExist) {
			return closeErr
		}
		return errors.Join(fmt.Errorf("inspect reserved output path: %w", err), closeErr)
	}
	if !os.SameFile(openedInfo, pathInfo) {
		closeErr := r.file.Close()
		r.finalized = true
		return errors.Join(errors.New("reserved output path no longer names the owned file"), closeErr)
	}

	removeErr := os.Remove(r.path)
	closeErr := r.file.Close()
	r.finalized = true
	if removeErr != nil {
		removeErr = fmt.Errorf("remove reserved output: %w", removeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close reserved output: %w", closeErr)
	}
	return errors.Join(removeErr, closeErr)
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
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("output path is not a directory")
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

		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			return &Reservation{path: candidate, file: file}, nil
		case errors.Is(err, os.ErrExist):
			continue
		default:
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
