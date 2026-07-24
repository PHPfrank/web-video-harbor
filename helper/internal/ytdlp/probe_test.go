package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeUsesOnlyExecutableAdjacentBundledBinaryAndVersionArgument(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	bundledPath := filepath.Join(dir, "yt-dlp_macos")
	writeProbeFile(t, bundledPath, 0o700)

	result, err := probeAdjacent(context.Background(), helperPath, func(ctx context.Context, path string, args ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
			t.Fatalf("probe deadline = %v, want a short positive deadline", deadline)
		}
		if path != bundledPath {
			t.Fatalf("probe path = %q, want %q", path, bundledPath)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("probe args = %#v, want only --version", args)
		}
		return []byte("2026.07.04\n"), nil
	})
	if err != nil {
		t.Fatalf("probeAdjacent() error = %v", err)
	}
	if result.Path != bundledPath || result.Version != "2026.07.04" {
		t.Fatalf("probe result = %#v", result)
	}
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

func writeProbeFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
}
