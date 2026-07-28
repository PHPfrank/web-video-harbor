package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func TestCurrentPlatformNoticeVersionIsFixedBoundedASCII(t *testing.T) {
	if CurrentPlatformNoticeVersion != "2026-07-28-v1" {
		t.Fatalf("CurrentPlatformNoticeVersion = %q, want fixed version", CurrentPlatformNoticeVersion)
	}
	if len(CurrentPlatformNoticeVersion) == 0 || len(CurrentPlatformNoticeVersion) > 32 {
		t.Fatalf("CurrentPlatformNoticeVersion length = %d, want 1..32", len(CurrentPlatformNoticeVersion))
	}
	if !utf8.ValidString(CurrentPlatformNoticeVersion) {
		t.Fatal("CurrentPlatformNoticeVersion is not valid UTF-8")
	}
	for _, r := range CurrentPlatformNoticeVersion {
		if r < 0x21 || r > 0x7e {
			t.Fatalf("CurrentPlatformNoticeVersion contains unsafe rune %q", r)
		}
	}
}

func TestOpenMissingSettingsDefaultsToDisabledWithoutCreatingFile(t *testing.T) {
	parent := privateTempDir(t)
	path := filepath.Join(parent, "settings.json")
	store := Open(path)

	if got := store.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("Snapshot() = %#v, want disabled", got)
	}
	if store.Enabled() {
		t.Fatal("Enabled() = true, want false")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing settings file was created by Open: %v", err)
	}
}

func TestSetPlatformCompatibilityPersistsSecurely(t *testing.T) {
	parent := privateTempDir(t)
	path := filepath.Join(parent, "settings.json")
	store := Open(path)

	got, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion)
	if err != nil {
		t.Fatalf("SetPlatformCompatibility() error = %v", err)
	}
	want := Snapshot{true, CurrentPlatformNoticeVersion}
	if got != want || store.Snapshot() != want || !store.Enabled() {
		t.Fatalf("enabled snapshots = %#v / %#v, want %#v", got, store.Snapshot(), want)
	}
	if reopened := Open(path).Snapshot(); reopened != want {
		t.Fatalf("reopened Snapshot() = %#v, want %#v", reopened, want)
	}
	assertMode(t, path, 0o600)
	assertOnlyFinalFile(t, parent, path)
}

func TestSetPlatformCompatibilityCreatesMissingParentSecurely(t *testing.T) {
	root := privateTempDir(t)
	parent := filepath.Join(root, "new-parent")
	path := filepath.Join(parent, "settings.json")

	if _, err := Open(path).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
		t.Fatalf("SetPlatformCompatibility() error = %v", err)
	}
	assertMode(t, parent, 0o700)
	assertMode(t, path, 0o600)
}

func TestSetPlatformCompatibilityRejectsInvalidNoticeVersions(t *testing.T) {
	tests := map[string]string{
		"empty":             "",
		"stale":             "2026-07-27-v1",
		"oversized":         strings.Repeat("a", 1024),
		"control-character": "2026-07-28-v1\n",
		"non-ascii":         "2026-07-28-v1-确认",
	}
	for name, version := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "settings.json")
			store := Open(path)
			if got, err := store.SetPlatformCompatibility(true, version); err == nil {
				t.Fatalf("SetPlatformCompatibility() = %#v, nil; want error", got)
			}
			if got := store.Snapshot(); got != (Snapshot{}) {
				t.Fatalf("Snapshot() = %#v after rejection, want disabled", got)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid update created settings file: %v", err)
			}
		})
	}
}

func TestDisablingClearsPersistedAcknowledgment(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "settings.json")
	store := Open(path)
	if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
		t.Fatal(err)
	}

	got, err := store.SetPlatformCompatibility(false, CurrentPlatformNoticeVersion)
	if err != nil {
		t.Fatalf("disable error = %v", err)
	}
	if got != (Snapshot{}) || store.Snapshot() != (Snapshot{}) {
		t.Fatalf("disabled Snapshot() = %#v / %#v, want zero snapshot", got, store.Snapshot())
	}
	if reopened := Open(path).Snapshot(); reopened != (Snapshot{}) {
		t.Fatalf("reopened Snapshot() = %#v, want zero snapshot", reopened)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "platform_notice_version") {
		t.Fatalf("disabled settings retained notice version: %s", data)
	}
}

func TestOpenInvalidJSONFailsClosed(t *testing.T) {
	tests := map[string]string{
		"malformed":        `{not json`,
		"unknown field":    `{"experimental_platform_compatibility_enabled":true,"platform_notice_version":"2026-07-28-v1","extra":true}`,
		"multiple values":  `{"experimental_platform_compatibility_enabled":true,"platform_notice_version":"2026-07-28-v1"} {}`,
		"stale notice":     `{"experimental_platform_compatibility_enabled":true,"platform_notice_version":"2026-07-27-v1"}`,
		"missing notice":   `{"experimental_platform_compatibility_enabled":true}`,
		"notice while off": `{"experimental_platform_compatibility_enabled":false,"platform_notice_version":"2026-07-28-v1"}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "settings.json")
			writeRawSettings(t, path, contents, 0o600)
			store := Open(path)
			if store.Enabled() || store.Snapshot() != (Snapshot{}) {
				t.Fatalf("invalid settings enabled compatibility: %#v", store.Snapshot())
			}
		})
	}
}

func TestExplicitValidUpdateRepairsMalformedSecureRegularFile(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "settings.json")
	writeRawSettings(t, path, `{not json`, 0o600)
	store := Open(path)
	if store.Enabled() {
		t.Fatal("malformed file enabled compatibility")
	}
	want := Snapshot{true, CurrentPlatformNoticeVersion}
	got, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion)
	if err != nil || got != want {
		t.Fatalf("repair = %#v, %v; want %#v, nil", got, err, want)
	}
	if reopened := Open(path).Snapshot(); reopened != want {
		t.Fatalf("reopened repaired Snapshot() = %#v, want %#v", reopened, want)
	}
}

func TestUnsafePathsNeverEnableOrGetReplaced(t *testing.T) {
	valid := `{"experimental_platform_compatibility_enabled":true,"platform_notice_version":"2026-07-28-v1"}`
	t.Run("settings symlink", func(t *testing.T) {
		parent := privateTempDir(t)
		target := filepath.Join(parent, "target.json")
		writeRawSettings(t, target, valid, 0o600)
		link := filepath.Join(parent, "settings.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		store := Open(link)
		if store.Enabled() {
			t.Fatal("symlink enabled compatibility")
		}
		if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err == nil {
			t.Fatal("update replaced settings symlink")
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("settings symlink was replaced: %v, %v", info, err)
		}
	})

	t.Run("non regular", func(t *testing.T) {
		path := filepath.Join(privateTempDir(t), "settings.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		store := Open(path)
		if store.Enabled() {
			t.Fatal("directory enabled compatibility")
		}
		if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err == nil {
			t.Fatal("update replaced non-regular path")
		}
	})

	t.Run("insecure file mode", func(t *testing.T) {
		path := filepath.Join(privateTempDir(t), "settings.json")
		writeRawSettings(t, path, valid, 0o644)
		store := Open(path)
		if store.Enabled() {
			t.Fatal("insecure file enabled compatibility")
		}
		if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err == nil {
			t.Fatal("update replaced insecure settings file")
		}
		assertMode(t, path, 0o644)
	})

	t.Run("insecure parent mode", func(t *testing.T) {
		parent := privateTempDir(t)
		path := filepath.Join(parent, "settings.json")
		writeRawSettings(t, path, valid, 0o600)
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		store := Open(path)
		if store.Enabled() {
			t.Fatal("insecure parent enabled compatibility")
		}
		if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err == nil {
			t.Fatal("update wrote through insecure parent")
		}
	})

	t.Run("symlink parent", func(t *testing.T) {
		root := privateTempDir(t)
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(realParent, "settings.json")
		writeRawSettings(t, path, valid, 0o600)
		linkedParent := filepath.Join(root, "linked")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		store := Open(filepath.Join(linkedParent, "settings.json"))
		if store.Enabled() {
			t.Fatal("symlink parent enabled compatibility")
		}
		if _, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err == nil {
			t.Fatal("update wrote through symlink parent")
		}
	})
}

func TestAtomicWriteUsesPrivateSameDirectoryTempAndSyncs(t *testing.T) {
	parent := privateTempDir(t)
	path := filepath.Join(parent, "settings.json")
	ops := defaultStoreOps()
	originalWrite := ops.writeData
	originalSyncFile := ops.syncFile
	originalPublish := ops.renameAt
	originalSyncParent := ops.syncDir

	var tempPath, publishedFrom, publishedTo string
	var fileSynced, parentSynced bool
	ops.writeData = func(file *os.File, data []byte) (int, error) {
		tempPath = file.Name()
		info, err := file.Stat()
		if err != nil {
			t.Fatalf("stat temporary settings: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("temporary mode = %04o, want 0600", info.Mode().Perm())
		}
		return originalWrite(file, data)
	}
	ops.syncFile = func(file *os.File) error {
		fileSynced = true
		return originalSyncFile(file)
	}
	ops.renameAt = func(dirFD int, oldName, newName string) error {
		publishedFrom, publishedTo = oldName, newName
		return originalPublish(dirFD, oldName, newName)
	}
	ops.syncDir = func(dirFD int) error {
		parentSynced = true
		return originalSyncParent(dirFD)
	}

	if _, err := openWithOps(path, ops).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
		t.Fatalf("SetPlatformCompatibility() error = %v", err)
	}
	if filepath.Dir(tempPath) != parent || tempPath == path || !strings.Contains(filepath.Base(tempPath), ".settings.json-") {
		t.Fatalf("temporary path = %q, want random sibling of %q", tempPath, path)
	}
	if !fileSynced {
		t.Fatal("temporary file was not synced")
	}
	if publishedFrom != filepath.Base(tempPath) || publishedTo != filepath.Base(path) {
		t.Fatalf("publish = (%q, %q), want basename-only (%q, %q)", publishedFrom, publishedTo, filepath.Base(tempPath), filepath.Base(path))
	}
	if !parentSynced {
		t.Fatal("settings parent directory was not synced")
	}
	assertOnlyFinalFile(t, parent, path)
}

func TestWriteFailuresPreserveLastValidFileAndCleanTemps(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*storeOps)
	}{
		{
			name: "short write",
			inject: func(ops *storeOps) {
				ops.writeData = func(file *os.File, data []byte) (int, error) {
					if len(data) == 0 {
						return 0, nil
					}
					return file.Write(data[:len(data)-1])
				}
			},
		},
		{
			name: "file sync",
			inject: func(ops *storeOps) {
				ops.syncFile = func(*os.File) error { return errors.New("injected file sync failure") }
			},
		},
		{
			name: "publish",
			inject: func(ops *storeOps) {
				ops.renameAt = func(int, string, string) error { return errors.New("injected publish failure") }
			},
		},
		{
			name: "parent sync",
			inject: func(ops *storeOps) {
				ops.syncDir = func(int) error { return errors.New("injected parent sync failure") }
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := privateTempDir(t)
			path := filepath.Join(parent, "settings.json")
			want := Snapshot{true, CurrentPlatformNoticeVersion}
			if _, err := Open(path).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
				t.Fatal(err)
			}
			ops := defaultStoreOps()
			tc.inject(&ops)
			store := openWithOps(path, ops)

			if got, err := store.SetPlatformCompatibility(false, ""); err == nil || got != want {
				t.Fatalf("failed update = %#v, %v; want preserved %#v and error", got, err, want)
			}
			if got := store.Snapshot(); got != want {
				t.Fatalf("in-memory Snapshot() = %#v, want preserved %#v", got, want)
			}
			if got := Open(path).Snapshot(); got != want {
				t.Fatalf("persisted Snapshot() = %#v, want preserved %#v", got, want)
			}
			assertOnlyFinalFile(t, parent, path)
		})
	}
}

func TestPostCommitCleanupFailuresKeepCommittedState(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*storeOps)
	}{
		{
			name: "backup unlink",
			inject: func(ops *storeOps) {
				original := ops.unlinkAt
				failed := false
				ops.unlinkAt = func(dirFD int, name string) error {
					if !failed && strings.Contains(name, "-backup-") {
						failed = true
						return errors.New("injected backup unlink failure")
					}
					return original(dirFD, name)
				}
			},
		},
		{
			name: "cleanup directory sync",
			inject: func(ops *storeOps) {
				original := ops.syncDir
				calls := 0
				ops.syncDir = func(dirFD int) error {
					calls++
					if calls == 2 {
						return errors.New("injected cleanup directory sync failure")
					}
					return original(dirFD)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := privateTempDir(t)
			path := filepath.Join(parent, "settings.json")
			if _, err := Open(path).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
				t.Fatal(err)
			}
			ops := defaultStoreOps()
			tc.inject(&ops)
			store := openWithOps(path, ops)

			got, err := store.SetPlatformCompatibility(false, "")
			if err != nil || got != (Snapshot{}) {
				t.Fatalf("post-commit cleanup result = %#v, %v; want committed disabled snapshot", got, err)
			}
			if store.Snapshot() != (Snapshot{}) || Open(path).Snapshot() != (Snapshot{}) {
				t.Fatalf("memory/disk diverged after commit: memory=%#v disk=%#v", store.Snapshot(), Open(path).Snapshot())
			}
			assertOnlyFinalFile(t, parent, path)
		})
	}
}

func TestRollbackRenameFailurePreservesRecoveryBackup(t *testing.T) {
	parent := privateTempDir(t)
	path := filepath.Join(parent, "settings.json")
	wantOld := Snapshot{true, CurrentPlatformNoticeVersion}
	if _, err := Open(path).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
		t.Fatal(err)
	}

	ops := defaultStoreOps()
	originalRename := ops.renameAt
	renameCalls := 0
	ops.renameAt = func(dirFD int, oldName, newName string) error {
		renameCalls++
		if renameCalls == 2 {
			return errors.New("injected rollback rename failure")
		}
		return originalRename(dirFD, oldName, newName)
	}
	ops.syncDir = func(int) error { return errors.New("injected commit sync failure") }
	store := openWithOps(path, ops)

	got, err := store.SetPlatformCompatibility(false, "")
	if err == nil || got != wantOld || store.Snapshot() != wantOld {
		t.Fatalf("failed rollback = %#v, %v, memory %#v; want old snapshot and error", got, err, store.Snapshot())
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var recoveryPath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-backup-") {
			recoveryPath = filepath.Join(parent, entry.Name())
			break
		}
	}
	if recoveryPath == "" {
		t.Fatalf("rollback failure removed the only recovery backup; entries=%v", entries)
	}
	if recovered := Open(recoveryPath).Snapshot(); recovered != wantOld {
		t.Fatalf("recovery backup Snapshot() = %#v, want %#v", recovered, wantOld)
	}
}

func TestTargetReplacementRaceDoesNotReplaceSymlink(t *testing.T) {
	parent := privateTempDir(t)
	path := filepath.Join(parent, "settings.json")
	victim := filepath.Join(parent, "victim.json")
	wantOld := Snapshot{true, CurrentPlatformNoticeVersion}
	if _, err := Open(path).SetPlatformCompatibility(true, CurrentPlatformNoticeVersion); err != nil {
		t.Fatal(err)
	}
	writeRawSettings(t, victim, `{"experimental_platform_compatibility_enabled":true,"platform_notice_version":"2026-07-28-v1"}`, 0o600)

	ops := defaultStoreOps()
	ops.beforePublish = func(_ int, _ string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Symlink(victim, path)
	}
	store := openWithOps(path, ops)
	got, err := store.SetPlatformCompatibility(false, "")
	if err == nil || got != wantOld || store.Snapshot() != wantOld {
		t.Fatalf("raced update = %#v, %v, memory %#v; want rejection and old memory", got, err, store.Snapshot())
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("raced symlink was replaced: info=%v err=%v", info, statErr)
	}
	if victimSnapshot := Open(victim).Snapshot(); victimSnapshot != wantOld {
		t.Fatalf("victim was modified: %#v", victimSnapshot)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var recoveryPath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-backup-") {
			recoveryPath = filepath.Join(parent, entry.Name())
			break
		}
	}
	if recoveryPath == "" || Open(recoveryPath).Snapshot() != wantOld {
		t.Fatalf("target race did not preserve the displaced valid settings; entries=%v", entries)
	}
}

func TestSeparateStoresSerializeWritesOnParentDirectory(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "settings.json")
	if _, err := Open(path).SetPlatformCompatibility(false, ""); err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondLockAttempted := make(chan struct{})
	secondEntered := make(chan struct{})

	firstOps := defaultStoreOps()
	firstOps.beforePublish = func(int, string) error {
		close(firstEntered)
		<-releaseFirst
		return nil
	}
	secondOps := defaultStoreOps()
	originalSecondFlock := secondOps.flock
	secondOps.flock = func(fd, how int) error {
		if how == unix.LOCK_EX {
			close(secondLockAttempted)
		}
		return originalSecondFlock(fd, how)
	}
	secondOps.beforePublish = func(int, string) error {
		close(secondEntered)
		return nil
	}
	first := openWithOps(path, firstOps)
	second := openWithOps(path, secondOps)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := first.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion)
		firstDone <- err
	}()
	<-firstEntered
	go func() {
		_, err := second.SetPlatformCompatibility(false, "")
		secondDone <- err
	}()
	select {
	case <-secondLockAttempted:
	case <-time.After(2 * time.Second):
		t.Fatal("second Store did not attempt to acquire the directory write lock")
	}
	select {
	case <-secondEntered:
		t.Fatal("second Store entered publish while first Store still held the directory write lock")
	default:
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second Store did not resume after directory write lock release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentReadsAndWritesReturnCompleteSnapshots(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "settings.json")
	store := Open(path)
	const workers = 24
	const iterations = 40
	start := make(chan struct{})
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			ready.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if (worker+i)%3 == 0 {
					enabled := (worker+i)%2 == 0
					version := ""
					if enabled {
						version = CurrentPlatformNoticeVersion
					}
					if _, err := store.SetPlatformCompatibility(enabled, version); err != nil {
						errs <- err
						return
					}
					continue
				}
				got := store.Snapshot()
				if got != (Snapshot{}) && got != (Snapshot{true, CurrentPlatformNoticeVersion}) {
					errs <- errors.New("observed incomplete snapshot")
					return
				}
				_ = store.Enabled()
			}
			errs <- nil
		}()
	}
	ready.Wait()
	close(start)
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := Open(path).Snapshot(); got != (Snapshot{}) && got != (Snapshot{true, CurrentPlatformNoticeVersion}) {
		t.Fatalf("persisted incomplete Snapshot() = %#v", got)
	}
}

func TestPathForConfig(t *testing.T) {
	got, err := PathForConfig(filepath.Join("relative", "..", "config.json"))
	if err != nil {
		t.Fatalf("PathForConfig() error = %v", err)
	}
	absConfig, err := filepath.Abs("config.json")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(filepath.Clean(absConfig)), "settings.json")
	if got != want || !filepath.IsAbs(got) || got != filepath.Clean(got) {
		t.Fatalf("PathForConfig() = %q, want cleaned absolute %q", got, want)
	}

	for _, unsafe := range []string{"", "   ", "\x00", ".", string(filepath.Separator)} {
		if got, err := PathForConfig(unsafe); err == nil {
			t.Errorf("PathForConfig(%q) = %q, nil; want error", unsafe, got)
		}
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod private temp directory: %v", err)
	}
	return dir
}

func writeRawSettings(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create settings parent: %v", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("chmod settings parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod settings: %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %04o, want %04o", path, got, want)
	}
}

func assertOnlyFinalFile(t *testing.T, parent, finalPath string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read settings parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(finalPath) {
		t.Fatalf("settings parent entries = %v, want only %q", entries, filepath.Base(finalPath))
	}
}
