package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeRuntimeUsesOnlyAdjacentArchitectureBinaryAndPrivateSnapshot(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)
	candidate := filepath.Join(dir, "deno_macos_arm64")
	if err := os.WriteFile(candidate, []byte("trusted deno bytes"), 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := probeRuntimeAdjacent(context.Background(), helperPath, "arm64", func(_ context.Context, path string, args ...string) ([]byte, error) {
		if path == candidate || filepath.Base(path) != "deno_macos_arm64" {
			t.Fatalf("runtime execution path = %q", path)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("runtime args = %q", args)
		}
		return []byte("deno 2.4.5 (stable, release, aarch64-apple-darwin)\nv8 13.7\ntypescript 5.8.3\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Close() })
	if result.Version != "2.4.5" || result.Path == "" || result.Snapshot == nil {
		t.Fatalf("runtime result = %#v", result)
	}
}

func TestProbeRuntimeRejectsUnsupportedArchitectureAndInvalidVersion(t *testing.T) {
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "web-video-harbor-helper")
	writeProbeFile(t, helperPath, 0o700)

	if _, err := probeRuntimeAdjacent(context.Background(), helperPath, "386", func(context.Context, string, ...string) ([]byte, error) {
		return []byte("deno 2.4.5\n"), nil
	}); err == nil {
		t.Fatal("probeRuntimeAdjacent accepted unsupported architecture")
	}

	writeProbeFile(t, filepath.Join(dir, "deno_macos_x86_64"), 0o700)
	for _, output := range []string{"", "2.4.5\n", "deno latest\n", "secret\ndeno 2.4.5\n"} {
		if result, err := probeRuntimeAdjacent(context.Background(), helperPath, "amd64", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(output), nil
		}); err == nil {
			_ = result.Close()
			t.Fatalf("probeRuntimeAdjacent accepted version output %q", output)
		}
	}
}
