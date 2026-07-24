package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unicode/utf8"

	"golang.org/x/sys/unix"
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

func TestReservationPublishRejectsReplacedDisplayParent(t *testing.T) {
	root := t.TempDir()
	displayDir := filepath.Join(root, "downloads")
	movedDir := filepath.Join(root, "owned-downloads")
	if err := os.Mkdir(displayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	reservation, err := ReserveAvailablePath(displayDir, "父目录替换", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.File().Write([]byte("video")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(displayDir, movedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(displayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reservation.Path(), []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := reservation.PublishExpected(int64(len("video"))); err == nil {
		t.Fatal("PublishExpected() accepted a Path whose display parent no longer names the pinned directory")
	}
	contents, err := os.ReadFile(reservation.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "attacker" {
		t.Fatalf("replacement display path was modified: %q", contents)
	}
	if err := reservation.Release(); err != nil {
		t.Fatalf("Release() after rejected publication: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(movedDir, filepath.Base(reservation.Path()))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned reservation remains in moved pinned directory: %v", err)
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

func TestReservationReleasePreservesReplacementRacedAfterValidation(t *testing.T) {
	dir := t.TempDir()
	reservation, err := ReserveAvailablePath(dir, "释放竞态", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.File().Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	movedOwned := reservation.Path() + ".owned"
	reservation.beforeReleaseIsolation = func() {
		if err := os.Rename(reservation.Path(), movedOwned); err != nil {
			t.Fatalf("move owned reservation after validation: %v", err)
		}
		if err := os.WriteFile(reservation.Path(), []byte("replacement"), 0o600); err != nil {
			t.Fatalf("create replacement after validation: %v", err)
		}
	}

	if err := reservation.Release(); err == nil {
		t.Fatal("Release() reported success after the validated entry was replaced")
	}
	if !treeContainsFileContents(t, dir, "replacement") {
		t.Fatal("Release() deleted the replacement raced in after validation")
	}
}

func TestReservationReleasePreservesReplacementWhenIsolationRenameFails(t *testing.T) {
	dir := t.TempDir()
	reservation, err := ReserveAvailablePath(dir, "隔离改名失败", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.File().Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	movedOwned := reservation.Path() + ".owned"
	reservation.beforeReleaseIsolation = func() {
		if err := os.Rename(reservation.Path(), movedOwned); err != nil {
			t.Fatalf("move owned reservation before failed isolation: %v", err)
		}
	}
	var replacementInfo os.FileInfo
	reservation.beforeReleaseGuardRemove = func(parent *os.File, name string) {
		if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
			t.Fatalf("move failed-isolation guard: %v", err)
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			t.Fatalf("create failed-isolation guard replacement: %v", err)
		}
		replacementInfo, err = inspectEntryAt(parent, name)
		if err != nil {
			t.Fatalf("inspect failed-isolation guard replacement: %v", err)
		}
	}

	if err := reservation.Release(); err == nil {
		t.Fatal("Release() reported success after isolation rename failed")
	}
	if replacementInfo == nil {
		t.Fatal("Release() bypassed final guard validation after isolation rename failed")
	}
	if !treeContainsSameFile(t, dir, replacementInfo) {
		t.Fatal("Release() deleted the replacement guard after isolation rename failed")
	}
}

func TestCreatePrivateDirectoryPreservesReplacementOnOpenFailure(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var replacementInfo os.FileInfo
	ops := privateDirectoryOps{
		open: func(parentFD int, name string, _ int, _ uint32) (int, error) {
			replacementInfo = replacePrivateDirectoryEntry(t, parentFD, name)
			return -1, syscall.EIO
		},
	}

	if _, directory, err := createPrivateDirectoryAt(root, ".open-failure-", ops); err == nil || directory != nil {
		t.Fatalf("createPrivateDirectoryAt() = directory %v, err %v; want open failure", directory, err)
	}
	if !treeContainsSameFile(t, rootPath, replacementInfo) {
		t.Fatal("open failure cleanup deleted the replacement private directory")
	}
}

func TestCreatePrivateDirectoryPreservesReplacementOnWrapFailure(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var replacementInfo os.FileInfo
	ops := privateDirectoryOps{
		wrap: func(_ uintptr, name string) *os.File {
			replacementInfo = replacePrivateDirectoryEntry(t, int(root.Fd()), name)
			return nil
		},
	}

	if _, directory, err := createPrivateDirectoryAt(root, ".wrap-failure-", ops); err == nil || directory != nil {
		t.Fatalf("createPrivateDirectoryAt() = directory %v, err %v; want wrap failure", directory, err)
	}
	if !treeContainsSameFile(t, rootPath, replacementInfo) {
		t.Fatal("wrap failure cleanup deleted the replacement private directory")
	}
}

func replacePrivateDirectoryEntry(t *testing.T, parentFD int, name string) os.FileInfo {
	t.Helper()
	if err := unix.Renameat(parentFD, name, parentFD, name+".owned"); err != nil {
		t.Fatalf("move private directory before failure cleanup: %v", err)
	}
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		t.Fatalf("create private directory replacement: %v", err)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open private directory replacement: %v", err)
	}
	replacement := os.NewFile(uintptr(fd), name)
	if replacement == nil {
		_ = unix.Close(fd)
		t.Fatal("wrap private directory replacement")
	}
	info, err := replacement.Stat()
	if closeErr := replacement.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("inspect private directory replacement: %v", err)
	}
	return info
}

func TestReservationReleasePreservesReplacementAfterIsolation(t *testing.T) {
	dir := t.TempDir()
	reservation, err := ReserveAvailablePath(dir, "隔离后竞态", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.File().Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	reservation.afterReleaseIsolation = func(quarantine *os.File) {
		if err := unix.Renameat(int(quarantine.Fd()), "owned-entry", int(quarantine.Fd()), "owned-entry.original"); err != nil {
			t.Fatalf("move isolated owned entry: %v", err)
		}
		fd, err := unix.Openat(
			int(quarantine.Fd()),
			"owned-entry",
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		if err != nil {
			t.Fatalf("create isolated replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), "owned-entry")
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap isolated replacement")
		}
		if _, err := replacement.Write([]byte("isolated replacement")); err != nil {
			t.Fatal(err)
		}
		if err := replacement.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if err := reservation.Release(); err == nil {
		t.Fatal("Release() reported success after the isolated entry was replaced")
	}
	if !treeContainsFileContents(t, dir, "isolated replacement") {
		t.Fatal("Release() deleted the replacement raced in after isolation")
	}
	if !treeContainsFileContents(t, dir, "owned") {
		t.Fatal("Release() lost the owned entry moved aside during the isolation race")
	}
}

func TestReservationReleasePreservesReplacementOfFinalGuard(t *testing.T) {
	dir := t.TempDir()
	reservation, err := ReserveAvailablePath(dir, "最终隔离目录", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.File().Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	var replacementInfo os.FileInfo
	reservation.beforeReleaseGuardRemove = func(_ *os.File, name string) {
		path := filepath.Join(dir, name)
		if err := os.Rename(path, path+".owned"); err != nil {
			t.Fatalf("move final release guard: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create final release guard replacement: %v", err)
		}
		replacementInfo, err = os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect final release guard replacement: %v", err)
		}
	}

	if err := reservation.Release(); err == nil {
		t.Fatal("Release() reported success after the final guard was replaced")
	}
	if !treeContainsSameFile(t, dir, replacementInfo) {
		t.Fatal("Release() deleted the replacement final guard inode")
	}
}

func treeContainsFileContents(t *testing.T, root, want string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(contents) == want {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func treeContainsSameFile(t *testing.T, root string, want os.FileInfo) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if want != nil && os.SameFile(want, info) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
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
