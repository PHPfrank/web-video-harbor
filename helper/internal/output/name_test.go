package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeBaseName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "preserves Chinese", in: "夏日旅行", want: "夏日旅行"},
		{name: "replaces separators and colon", in: "旅行/上海:第一集", want: "旅行-上海-第一集"},
		{name: "removes control characters", in: "视\x00频\n标题\t", want: "视频标题"},
		{name: "removes leading dots", in: "...隐藏视频", want: "隐藏视频"},
		{name: "trims trailing spaces", in: "视频标题   ", want: "视频标题"},
		{name: "uses fallback for empty name", in: "... \x00\n", want: "视频"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeBaseName(tt.in); got != tt.want {
				t.Fatalf("SanitizeBaseName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeBaseNameCapsNameByRuneCount(t *testing.T) {
	got := SanitizeBaseName(strings.Repeat("视", maxBaseNameRunes+20))
	if count := utf8.RuneCountInString(got); count != maxBaseNameRunes {
		t.Fatalf("rune count = %d, want %d", count, maxBaseNameRunes)
	}
}

func TestSanitizeBaseNameCountsReplacementTowardRuneLimit(t *testing.T) {
	input := strings.Repeat("视", maxBaseNameRunes-1) + "/" + strings.Repeat("频", 20)
	got := SanitizeBaseName(input)
	if count := utf8.RuneCountInString(got); count > maxBaseNameRunes {
		t.Fatalf("rune count = %d, want at most %d", count, maxBaseNameRunes)
	}
}

func TestNextAvailablePathAvoidsExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "视频.mp4")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NextAvailablePath(dir, "视频", ".mp4")
	if err != nil {
		t.Fatalf("NextAvailablePath() error = %v", err)
	}
	want := filepath.Join(dir, "视频 (2).mp4")
	if got != want {
		t.Fatalf("NextAvailablePath() = %q, want %q", got, want)
	}
}

func TestNextAvailablePathSanitizesNameAndStaysInsideDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := NextAvailablePath(dir, "../../私密/视频", "mp4")
	if err != nil {
		t.Fatalf("NextAvailablePath() error = %v", err)
	}

	rel, err := filepath.Rel(dir, got)
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("path escaped directory: %q", got)
	}
	if filepath.Base(got) != "私密-视频.mp4" {
		t.Fatalf("base name = %q", filepath.Base(got))
	}
}

func TestNextAvailablePathRejectsUnsafeExtension(t *testing.T) {
	dir := t.TempDir()
	if _, err := NextAvailablePath(dir, "视频", "../outside"); err == nil {
		t.Fatal("unsafe extension was accepted")
	}
}
