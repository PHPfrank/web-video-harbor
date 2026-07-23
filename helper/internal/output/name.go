package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Eighty CJK runes plus an extension and collision suffix remain below the
// common 255-byte file-name limit on macOS filesystems.
const maxBaseNameRunes = 80

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

// NextAvailablePath selects a non-existing file name below dir. Callers that
// create the file themselves should still use an exclusive create operation to
// handle another process choosing the same path after this function returns.
func NextAvailablePath(dir, base, ext string) (string, error) {
	cleanExt, err := normalizeExtension(ext)
	if err != nil {
		return "", err
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve output directory: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return "", fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("output path is not a directory")
	}

	cleanBase := SanitizeBaseName(base)
	for index := 1; ; index++ {
		candidateBase := cleanBase
		if index > 1 {
			candidateBase = fmt.Sprintf("%s (%d)", cleanBase, index)
		}
		candidate := filepath.Join(absDir, candidateBase+cleanExt)
		if !isWithinDirectory(absDir, candidate) {
			return "", errors.New("output path escapes output directory")
		}

		_, err := os.Lstat(candidate)
		switch {
		case err == nil:
			continue
		case errors.Is(err, os.ErrNotExist):
			return candidate, nil
		default:
			return "", fmt.Errorf("inspect output path: %w", err)
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
	if len(ext) < 2 {
		return "", errors.New("file extension is empty")
	}
	for _, r := range ext[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", errors.New("file extension contains unsafe characters")
		}
	}
	return ext, nil
}

func isWithinDirectory(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
