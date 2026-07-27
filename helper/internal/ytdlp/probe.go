package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	bundledBinaryName = "yt-dlp_macos"
	probeTimeout      = 30 * time.Second
	maxVersionOutput  = 64
)

var versionPattern = regexp.MustCompile(`^[0-9]{4}\.(0[1-9]|1[0-2])\.(0[1-9]|[12][0-9]|3[01])$`)

// ProbeResult is the validated identity of the downloader bundled beside the
// helper. Path is for internal process execution and must not be exposed by
// health or status responses.
type ProbeResult struct {
	Path     string
	Version  string
	Snapshot *ExecutableSnapshot
}

// Close releases the private executable snapshot after the task engine has
// fully shut down. It is safe to call on an empty or already-closed result.
func (r ProbeResult) Close() error {
	if r.Snapshot == nil {
		return nil
	}
	return r.Snapshot.Close()
}

type versionCommand func(context.Context, string, ...string) ([]byte, error)

type boundedVersionBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (w *boundedVersionBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.limit - w.buffer.Len()
	if remaining < len(p) {
		w.exceeded = true
	}
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.buffer.Write(p[:remaining])
	}
	return written, nil
}

// Probe discovers only the fixed executable bundled next to this helper.
// It deliberately does not search PATH or consult user configuration.
func Probe(ctx context.Context) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, errors.New("probe platform downloader: nil context")
	}
	helperPath, err := os.Executable()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("resolve helper executable: %w", err)
	}
	return probeAdjacent(ctx, helperPath, runVersionCommand)
}

func probeAdjacent(ctx context.Context, helperPath string, run versionCommand) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, errors.New("probe platform downloader: nil context")
	}
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	if run == nil {
		return ProbeResult{}, errors.New("probe platform downloader: missing command runner")
	}
	absHelper, err := filepath.Abs(helperPath)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("resolve helper path: %w", err)
	}
	candidate := filepath.Join(filepath.Dir(absHelper), bundledBinaryName)
	snapshot, err := createExecutableSnapshot(candidate, bundledBinaryName)
	if err != nil {
		return ProbeResult{}, err
	}
	keepSnapshot := false
	defer func() {
		if !keepSnapshot {
			_ = snapshot.Close()
		}
	}()
	if err := snapshot.Verify(); err != nil {
		return ProbeResult{}, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	output, err := run(probeCtx, snapshot.Path(), "--version")
	if err != nil {
		if probeCtx.Err() != nil {
			return ProbeResult{}, probeCtx.Err()
		}
		return ProbeResult{}, errors.New("bundled platform downloader version probe failed")
	}
	if len(output) == 0 || len(output) > maxVersionOutput {
		return ProbeResult{}, errors.New("bundled platform downloader returned an invalid version")
	}
	version := strings.TrimSuffix(string(output), "\n")
	if !versionPattern.MatchString(version) {
		return ProbeResult{}, errors.New("bundled platform downloader returned an invalid version")
	}
	if _, err := time.Parse("2006.01.02", version); err != nil {
		return ProbeResult{}, errors.New("bundled platform downloader returned an invalid version")
	}
	if err := snapshot.Verify(); err != nil {
		return ProbeResult{}, err
	}
	keepSnapshot = true
	return ProbeResult{Path: snapshot.Path(), Version: version, Snapshot: snapshot}, nil
}

func runVersionCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	return runBoundedVersionCommand(ctx, path, maxVersionOutput, args...)
}

func runBoundedVersionCommand(ctx context.Context, path string, limit int, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil || limit <= 0 {
		if err == nil {
			err = errors.New("version output limit is invalid")
		}
		return nil, err
	}
	command := exec.Command(path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = outputDrainGrace
	stdout := boundedVersionBuffer{limit: limit}
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitResult:
		terminateOrphanedProcessGroup(command.Process.Pid)
	case <-ctx.Done():
		_ = terminateProcessGroup(command.Process.Pid, waitResult)
		return nil, ctx.Err()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if stdout.exceeded {
		return nil, errors.New("platform downloader version output exceeded limit")
	}
	return stdout.buffer.Bytes(), nil
}

func terminateOrphanedProcessGroup(pid int) bool {
	if pid <= 1 || !processGroupExists(pid) {
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if confirmProcessGroupExit(pid, terminationGrace, processGroupExists) {
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return confirmProcessGroupExit(pid, terminationConfirmGrace, processGroupExists)
}
