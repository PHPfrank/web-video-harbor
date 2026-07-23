package main

import (
	"bytes"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	var stdout bytes.Buffer

	exitCode := run([]string{"--version"}, &stdout)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0", exitCode)
	}
	if got, want := stdout.String(), "web-video-helper dev\n"; got != want {
		t.Fatalf("run() output = %q, want %q", got, want)
	}
}
