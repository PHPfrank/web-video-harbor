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
	"time"
)

const (
	bundledBinaryName = "yt-dlp_macos"
	probeTimeout      = 2 * time.Second
	maxVersionOutput  = 64
)

var versionPattern = regexp.MustCompile(`^[0-9]{4}\.(0[1-9]|1[0-2])\.(0[1-9]|[12][0-9]|3[01])$`)

// ProbeResult is the validated identity of the downloader bundled beside the
// helper. Path is for internal process execution and must not be exposed by
// health or status responses.
type ProbeResult struct {
	Path    string
	Version string
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
	info, err := os.Lstat(candidate)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("inspect bundled platform downloader: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return ProbeResult{}, errors.New("bundled platform downloader is not a regular executable")
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	output, err := run(probeCtx, candidate, "--version")
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
	return ProbeResult{Path: candidate, Version: version}, nil
}

func runVersionCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	stdout := boundedVersionBuffer{limit: maxVersionOutput}
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.exceeded {
		return nil, errors.New("platform downloader version output exceeded limit")
	}
	return stdout.buffer.Bytes(), nil
}
