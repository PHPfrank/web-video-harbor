package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("reserved path was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("reserved path permissions = %o, want 600", perm)
	}
}

func TestNextAvailablePathAtomicallyReservesConcurrentNames(t *testing.T) {
	dir := t.TempDir()
	const workers = 24
	start := make(chan struct{})
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup

	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			path, err := NextAvailablePath(dir, "并发视频", ".mp4")
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	close(start)
	group.Wait()
	close(paths)
	close(errs)

	for err := range errs {
		t.Errorf("NextAvailablePath() error = %v", err)
	}
	seen := make(map[string]bool, workers)
	for path := range paths {
		if seen[path] {
			t.Errorf("duplicate reserved path: %q", path)
		}
		seen[path] = true
		if _, err := os.Stat(path); err != nil {
			t.Errorf("reserved path %q missing: %v", path, err)
		}
	}
	if len(seen) != workers {
		t.Fatalf("unique reserved paths = %d, want %d", len(seen), workers)
	}
}

func TestNextAvailablePathDoesNotOverwriteSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "视频.mp4")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}

	got, err := NextAvailablePath(dir, "视频", ".mp4")
	if err != nil {
		t.Fatalf("NextAvailablePath() error = %v", err)
	}
	if got != filepath.Join(dir, "视频 (2).mp4") {
		t.Fatalf("reserved path = %q", got)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "keep me" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
}

func TestReservationPublishAndRelease(t *testing.T) {
	dir := t.TempDir()

	published, err := ReserveAvailablePath(dir, "发布", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := published.File().Write([]byte("video")); err != nil {
		t.Fatal(err)
	}
	if err := published.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	contents, err := os.ReadFile(published.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "video" {
		t.Fatalf("published contents = %q", contents)
	}

	released, err := ReserveAvailablePath(dir, "取消", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	releasedPath := released.Path()
	if err := released.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(releasedPath); !os.IsNotExist(err) {
		t.Fatalf("released reservation still exists: %v", err)
	}
}

func TestReservationPublishRejectsUnsafeFinalPathState(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, *Reservation)
	}{
		{
			name: "same-size regular replacement",
			replace: func(t *testing.T, reservation *Reservation) {
				moved := reservation.Path() + ".owned"
				if err := os.Rename(reservation.Path(), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(reservation.Path(), []byte("video"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symbolic link replacement",
			replace: func(t *testing.T, reservation *Reservation) {
				target := filepath.Join(filepath.Dir(reservation.Path()), "target.txt")
				if err := os.WriteFile(target, []byte("video"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(reservation.Path()); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, reservation.Path()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world-readable owned file",
			replace: func(t *testing.T, reservation *Reservation) {
				if err := os.Chmod(reservation.Path(), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservation, err := ReserveAvailablePath(t.TempDir(), "发布复核", ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reservation.File().Write([]byte("video")); err != nil {
				t.Fatal(err)
			}
			test.replace(t, reservation)

			if err := reservation.Publish(); err == nil {
				t.Fatal("Publish() accepted an unsafe final path state")
			}
		})
	}
}

func TestReservationReleaseClosesDirectoryWhenOwnedFileIsAlreadyClosed(t *testing.T) {
	reservation, err := ReserveAvailablePath(t.TempDir(), "关闭失败", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	directory := reservation.directory
	if err := reservation.File().Close(); err != nil {
		t.Fatal(err)
	}

	if err := reservation.Release(); err == nil {
		t.Fatal("Release() succeeded after the owned file was already closed")
	}
	if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reservation directory remains open after Release() failure: %v", err)
	}
	if !reservation.finalized {
		t.Fatal("Release() failure did not finalize the reservation")
	}
}

func TestPublishedErrorCarriesOwnershipWithoutLeakingPathInMessage(t *testing.T) {
	path := "/Users/example/Downloads/private-title.mp4"
	cause := errors.New("cleanup failed")
	err := NewPublishedError(path, cause)

	gotPath, ok := PublishedPath(err)
	if !ok || gotPath != path {
		t.Fatalf("PublishedPath() = %q, %t, want %q, true", gotPath, ok, path)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("published error does not wrap cause: %v", err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("published error leaked output path: %q", err)
	}
	if _, ok := PublishedPath(cause); ok {
		t.Fatal("ordinary error was treated as published output")
	}
}

func TestReservationReleaseClosesHandleWhenPathIsAlreadyGone(t *testing.T) {
	dir := t.TempDir()
	reservation, err := ReserveAvailablePath(dir, "已移除", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(reservation.Path()); err != nil {
		t.Fatal(err)
	}

	if err := reservation.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := reservation.File().Write([]byte("must be closed")); err == nil {
		t.Fatal("reservation file remained open after release")
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
	tests := []string{
		"../outside",
		"." + strings.Repeat("a", 64),
		".exe",
	}
	for _, ext := range tests {
		if _, err := NextAvailablePath(dir, "视频", ext); err == nil {
			t.Errorf("unsafe or unsupported extension %q was accepted", ext)
		}
	}
}

func TestNextAvailablePathKeepsCompleteComponentWithinMacOSByteLimit(t *testing.T) {
	dir := t.TempDir()
	got, err := NextAvailablePath(dir, strings.Repeat("😀", maxBaseNameRunes), ".webm")
	if err != nil {
		t.Fatalf("NextAvailablePath() error = %v", err)
	}
	if size := len(filepath.Base(got)); size > maxFilenameComponentBytes {
		t.Fatalf("file-name component bytes = %d, want <= %d", size, maxFilenameComponentBytes)
	}
}
