package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
)

const maxRuntimeVersionOutput = 512

var denoVersionPattern = regexp.MustCompile(`^deno ([0-9]+\.[0-9]+\.[0-9]+)(?:[[:space:](]|$)`)

// RuntimeResult is the validated identity of the JavaScript runtime bundled
// beside the helper. Path is internal and must never be exposed in health.
type RuntimeResult struct {
	Path     string
	Version  string
	Snapshot *ExecutableSnapshot
}

func (r RuntimeResult) Close() error {
	if r.Snapshot == nil {
		return nil
	}
	return r.Snapshot.Close()
}

func ProbeRuntime(ctx context.Context) (RuntimeResult, error) {
	if ctx == nil {
		return RuntimeResult{}, errors.New("probe JavaScript runtime: nil context")
	}
	helperPath, err := os.Executable()
	if err != nil {
		return RuntimeResult{}, fmt.Errorf("resolve helper executable: %w", err)
	}
	return probeRuntimeAdjacent(ctx, helperPath, goruntime.GOARCH, runRuntimeVersionCommand)
}

func probeRuntimeAdjacent(ctx context.Context, helperPath, goarch string, run versionCommand) (RuntimeResult, error) {
	if ctx == nil {
		return RuntimeResult{}, errors.New("probe JavaScript runtime: nil context")
	}
	if err := ctx.Err(); err != nil {
		return RuntimeResult{}, err
	}
	if run == nil {
		return RuntimeResult{}, errors.New("probe JavaScript runtime: missing command runner")
	}
	fileName := ""
	switch goarch {
	case "arm64":
		fileName = "deno_macos_arm64"
	case "amd64":
		fileName = "deno_macos_x86_64"
	default:
		return RuntimeResult{}, errors.New("JavaScript runtime architecture is unsupported")
	}
	absHelper, err := filepath.Abs(helperPath)
	if err != nil {
		return RuntimeResult{}, fmt.Errorf("resolve helper path: %w", err)
	}
	candidate := filepath.Join(filepath.Dir(absHelper), fileName)
	snapshot, err := createExecutableSnapshot(candidate, fileName)
	if err != nil {
		return RuntimeResult{}, err
	}
	keepSnapshot := false
	defer func() {
		if !keepSnapshot {
			_ = snapshot.Close()
		}
	}()
	if err := snapshot.Verify(); err != nil {
		return RuntimeResult{}, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	output, err := run(probeCtx, snapshot.Path(), "--version")
	if err != nil {
		if probeCtx.Err() != nil {
			return RuntimeResult{}, probeCtx.Err()
		}
		return RuntimeResult{}, errors.New("bundled JavaScript runtime version probe failed")
	}
	if len(output) == 0 || len(output) > maxRuntimeVersionOutput {
		return RuntimeResult{}, errors.New("bundled JavaScript runtime returned an invalid version")
	}
	firstLine := strings.SplitN(string(output), "\n", 2)[0]
	match := denoVersionPattern.FindStringSubmatch(firstLine)
	if len(match) != 2 {
		return RuntimeResult{}, errors.New("bundled JavaScript runtime returned an invalid version")
	}
	if err := snapshot.Verify(); err != nil {
		return RuntimeResult{}, err
	}
	keepSnapshot = true
	return RuntimeResult{Path: snapshot.Path(), Version: match[1], Snapshot: snapshot}, nil
}

func runRuntimeVersionCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	return runBoundedVersionCommand(ctx, path, maxRuntimeVersionOutput, args...)
}
