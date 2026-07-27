package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProbeUsesOnlyExecutableAdjacentBundledBinaryAndVersionArgument(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	bundledPath := filepath.Join(dir, "yt-dlp_macos")
	writeProbeFile(t, bundledPath, 0o700)
	probedPath := ""

	result, err := probeAdjacent(context.Background(), helperPath, func(ctx context.Context, path string, args ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 31*time.Second {
			t.Fatalf("probe deadline = %v, want a bounded cold-start deadline", deadline)
		}
		if path == bundledPath || filepath.Base(path) != bundledBinaryName {
			t.Fatalf("probe path = %q, want a private bundled snapshot", path)
		}
		probedPath = path
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("probe args = %#v, want only --version", args)
		}
		return []byte("2026.07.04\n"), nil
	})
	if err != nil {
		t.Fatalf("probeAdjacent() error = %v", err)
	}
	t.Cleanup(func() { _ = result.Close() })
	if result.Path != probedPath || result.Snapshot == nil || result.Version != "2026.07.04" {
		t.Fatalf("probe result = %#v", result)
	}
	directoryInfo, err := os.Lstat(filepath.Dir(result.Path))
	if err != nil || !validPrivateOwnedDirectory(directoryInfo) || !regexp.MustCompile(`^\.web-video-harbor-parser-[0-9a-f]{32}$`).MatchString(filepath.Base(filepath.Dir(result.Path))) {
		t.Fatalf("snapshot directory is not a random private owned directory: path=%q info=%#v err=%v", filepath.Dir(result.Path), directoryInfo, err)
	}
	fileInfo, err := os.Lstat(result.Path)
	if err != nil || !validExecutableFile(fileInfo, 0o500) {
		t.Fatalf("snapshot executable permissions are unsafe: info=%#v err=%v", fileInfo, err)
	}
}

func TestProbeAllowsBundledParserColdStartWindow(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	writeProbeFile(t, filepath.Join(dir, bundledBinaryName), 0o700)

	result, err := probeAdjacent(context.Background(), helperPath, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("version probe has no deadline")
		}
		if remaining := time.Until(deadline); remaining < 29*time.Second {
			return nil, fmt.Errorf("version probe window = %v, want at least 29s", remaining)
		}
		return []byte("2026.07.04\n"), nil
	})
	if err != nil {
		t.Fatalf("probeAdjacent() rejected a valid cold-start window: %v", err)
	}
	t.Cleanup(func() { _ = result.Close() })
}

func TestProbeRejectsUnsafeBundledFilesBeforeExecution(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "missing", setup: func(*testing.T, string) {}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			writeProbeFile(t, target, 0o700)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non executable", setup: func(t *testing.T, path string) {
			writeProbeFile(t, path, 0o600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			helperPath := filepath.Join(dir, "web-video-harbor-helper")
			writeProbeFile(t, helperPath, 0o700)
			candidate := filepath.Join(dir, "yt-dlp_macos")
			tc.setup(t, candidate)
			called := false
			if _, err := probeAdjacent(context.Background(), helperPath, func(context.Context, string, ...string) ([]byte, error) {
				called = true
				return []byte("2026.07.04\n"), nil
			}); err == nil {
				t.Fatal("probeAdjacent() accepted an unsafe bundled file")
			}
			if called {
				t.Fatal("unsafe bundled file was executed")
			}
		})
	}
}

func TestExecutableSnapshotRejectsUnsafeTempParent(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), bundledBinaryName)
	if err := os.WriteFile(sourcePath, []byte("trusted executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		setup func(*testing.T) string
	}{
		{name: "symlink", setup: func(t *testing.T) string {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "temp-link")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "non private mode", setup: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "shared-temp")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMPDIR", test.setup(t))
			if snapshot, err := createExecutableSnapshot(sourcePath); err == nil {
				_ = snapshot.Close()
				t.Fatal("createExecutableSnapshot accepted an unsafe TMPDIR")
			}
		})
	}
}

func TestProbeExecutesPrivateSnapshotWhenSourcePathIsReplaced(t *testing.T) {
	for _, replacement := range []string{"symlink", "other inode"} {
		t.Run(replacement, func(t *testing.T) {
			dir := t.TempDir()
			helperPath := filepath.Join(dir, "web-video-harbor-helper")
			writeProbeFile(t, helperPath, 0o700)
			candidate := filepath.Join(dir, "yt-dlp_macos")
			if err := os.WriteFile(candidate, []byte("trusted snapshot bytes"), 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := probeAdjacent(context.Background(), helperPath, func(_ context.Context, executionPath string, _ ...string) ([]byte, error) {
				replacementPath := filepath.Join(dir, "replacement")
				if err := os.WriteFile(replacementPath, []byte("replacement bytes"), 0o700); err != nil {
					t.Fatal(err)
				}
				switch replacement {
				case "symlink":
					if err := os.Remove(candidate); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(replacementPath, candidate); err != nil {
						t.Fatal(err)
					}
				case "other inode":
					if err := os.Rename(replacementPath, candidate); err != nil {
						t.Fatal(err)
					}
				}
				if executionPath == candidate {
					t.Errorf("version probe still executes mutable source path %q", executionPath)
				}
				got, err := os.ReadFile(executionPath)
				if err != nil {
					t.Fatalf("read execution snapshot: %v", err)
				}
				if string(got) != "trusted snapshot bytes" {
					t.Fatalf("execution bytes = %q", got)
				}
				return []byte("2026.07.04\n"), nil
			})
			if err != nil {
				t.Fatalf("probeAdjacent() error = %v", err)
			}
			t.Cleanup(func() { _ = result.Close() })
			if result.Path == candidate {
				t.Fatalf("ProbeResult path remains mutable source path: %#v", result)
			}
		})
	}
}

func TestExecutableSnapshotDetectsDigestMutationAndClosesOwnedFiles(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), bundledBinaryName)
	trusted := []byte("trusted executable bytes")
	if err := os.WriteFile(sourcePath, trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := createExecutableSnapshot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := snapshot.Path()
	snapshotDir := filepath.Dir(snapshotPath)
	t.Cleanup(func() { _ = os.RemoveAll(snapshotDir) })
	if err := os.Chmod(snapshotPath, 0o700); err != nil {
		t.Fatal(err)
	}
	mutated := []byte("mutated executable bytes")
	if len(mutated) != len(trusted) {
		t.Fatalf("test mutation length = %d, want %d", len(mutated), len(trusted))
	}
	if err := os.WriteFile(snapshotPath, mutated, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snapshotPath, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err == nil {
		t.Fatal("Verify accepted same-inode, same-size digest mutation")
	}
	if err := os.Chmod(snapshotPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, trusted, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snapshotPath, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err != nil {
		t.Fatalf("Verify after restoring trusted bytes: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Lstat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot path remained after Close: %v", err)
	}
	if _, err := os.Lstat(snapshotDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot directory remained after Close: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestExecutableSnapshotRejectsClosedPinnedFileDescriptor(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), bundledBinaryName)
	if err := os.WriteFile(sourcePath, []byte("trusted executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := createExecutableSnapshot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Dir(snapshot.Path())
	t.Cleanup(func() {
		_ = snapshot.Close()
		_ = os.RemoveAll(snapshotDir)
	})
	if err := snapshot.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err == nil {
		t.Fatal("Verify accepted a closed pinned snapshot file descriptor")
	}
}

func TestExecutableSnapshotClosePreservesActiveLease(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), bundledBinaryName)
	if err := os.WriteFile(sourcePath, []byte("trusted executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := createExecutableSnapshot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := snapshot.Path()
	release, err := snapshot.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err == nil {
		t.Fatal("Close removed a snapshot with an active Runner lease")
	}
	if _, err := os.Lstat(snapshotPath); err != nil {
		t.Fatalf("active snapshot was removed: %v", err)
	}
	if err := snapshot.Verify(); err != nil {
		t.Fatalf("active snapshot became invalid: %v", err)
	}
	release()
	if err := snapshot.Close(); err != nil {
		t.Fatalf("Close after release: %v", err)
	}
}

func TestProbeAcceptsOnlyStrictBoundedVersion(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	writeProbeFile(t, filepath.Join(dir, "yt-dlp_macos"), 0o700)

	for _, tc := range []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "official format", output: "2026.07.04\n"},
		{name: "single digit month", output: "2026.7.04\n", wantErr: true},
		{name: "prefix", output: "v2026.07.04\n", wantErr: true},
		{name: "suffix", output: "2026.07.04 release\n", wantErr: true},
		{name: "multiple lines", output: "2026.07.04\nsecret\n", wantErr: true},
		{name: "invalid month", output: "2026.13.04\n", wantErr: true},
		{name: "invalid day", output: "2026.07.32\n", wantErr: true},
		{name: "overlong", output: strings.Repeat("2", 128), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := probeAdjacent(context.Background(), helperPath, func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.output), nil
			})
			if tc.wantErr {
				if err == nil || result != (ProbeResult{}) {
					t.Fatalf("probeAdjacent() = %#v, %v; want empty result and error", result, err)
				}
				return
			}
			if err != nil || result.Version != "2026.07.04" {
				t.Fatalf("probeAdjacent() = %#v, %v", result, err)
			}
			t.Cleanup(func() { _ = result.Close() })
		})
	}
}

func TestProbePropagatesCancellationWithoutReturningIdentity(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	writeProbeFile(t, filepath.Join(dir, "yt-dlp_macos"), 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := probeAdjacent(ctx, helperPath, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || result != (ProbeResult{}) {
		t.Fatalf("probeAdjacent() = %#v, %v; want canceled empty result", result, err)
	}
}

func TestRunVersionCommandKillsDescendantHoldingOutputPipe(t *testing.T) {
	t.Setenv("WVH_PROBE_PROCESS_HELPER", "1")
	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := runVersionCommand(ctx, os.Args[0],
			"-test.run=^TestProbeProcessHelper$", "--", "leader", pidPath,
		)
		result <- err
	}()

	pid := 0
	readyDeadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(readyDeadline) {
		pidBytes, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("descendant did not publish its PID")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	started := time.Now()
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("runVersionCommand() did not return promptly after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runVersionCommand() error = %v, want canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("runVersionCommand() took %v after cancellation", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("descendant process %d survived probe cancellation", pid)
	}
}

func TestRunVersionCommandBoundsDrainFromEscapedPipeHolder(t *testing.T) {
	t.Setenv("WVH_PROBE_PROCESS_HELPER", "1")
	pidPath := filepath.Join(t.TempDir(), "escaped.pid")
	result := make(chan error, 1)
	go func() {
		_, err := runVersionCommand(context.Background(), os.Args[0],
			"-test.run=^TestProbeProcessHelper$", "--", "escaped-leader", pidPath,
		)
		result <- err
	}()
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		pidBytes, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("escaped descendant did not publish its PID")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	ready := time.Now()
	var err error
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("runVersionCommand() did not bound drain after escaped descendant became ready")
	}
	if err == nil {
		t.Fatal("runVersionCommand() accepted output whose pipe holder escaped the process group")
	}
	if elapsed := time.Since(ready); elapsed > 2*time.Second {
		t.Fatalf("runVersionCommand() waited %v for escaped output pipe", elapsed)
	}
}

func TestProbeProcessHelper(t *testing.T) {
	if os.Getenv("WVH_PROBE_PROCESS_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	switch os.Args[separator+1] {
	case "leader":
		if separator+2 >= len(os.Args) {
			os.Exit(2)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestProbeProcessHelper$", "--", "descendant")
		command.Env = os.Environ()
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte(fmt.Sprintf("%d\n", command.Process.Pid)), 0o600); err != nil {
			os.Exit(4)
		}
		_, _ = fmt.Fprintln(os.Stdout, "2026.07.04")
		os.Exit(0)
	case "descendant":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(3 * time.Second)
		os.Exit(0)
	case "escaped-leader":
		if separator+2 >= len(os.Args) {
			os.Exit(2)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestProbeProcessHelper$", "--", "escaped-descendant")
		command.Env = os.Environ()
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte(fmt.Sprintf("%d\n", command.Process.Pid)), 0o600); err != nil {
			os.Exit(4)
		}
		_, _ = fmt.Fprintln(os.Stdout, "2026.07.04")
		os.Exit(0)
	case "escaped-descendant":
		time.Sleep(3 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func writeProbeFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
}
