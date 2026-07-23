package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChromeSupervisorCleansProfileMatchedChildAfterNodeFailure(t *testing.T) {
	browserRoot := t.TempDir()
	profileDir := filepath.Join(browserRoot, "chrome-profile-controlled")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(browserRoot, "chrome.pid")
	observedPIDPath := filepath.Join(browserRoot, "observed-child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestChromeSupervisorHelperProcess", "--", "fake-node")
	command.Env = append(os.Environ(),
		"GO_WANT_CHROME_SUPERVISOR_HELPER=1",
		"CHROME_HELPER_ROLE=node",
		"CHROME_HELPER_PROFILE="+profileDir,
		"CHROME_HELPER_PID_PATH="+pidPath,
		"CHROME_HELPER_OBSERVED_PID_PATH="+observedPIDPath,
	)

	_, err := superviseChromeCommand(command, pidPath, browserRoot)
	if err == nil {
		t.Fatal("fake Node failure unexpectedly succeeded")
	}
	pidBytes, readErr := os.ReadFile(observedPIDPath)
	if readErr != nil {
		t.Fatalf("read controlled Chrome PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse controlled Chrome PID: %v", parseErr)
	}
	if commandLine, commandErr := processCommand(pid); !errors.Is(commandErr, errProcessNotFound) {
		t.Fatalf("controlled Chrome process %d remained after Node failure: command=%q err=%v", pid, commandLine, commandErr)
	}
	if _, statErr := os.Stat(pidPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Chrome PID file was not removed after cleanup: %v", statErr)
	}
}

func TestContainsExactProcessArgumentRejectsProfilePrefixCollision(t *testing.T) {
	wanted := "--user-data-dir=/tmp/chrome-profile-1"
	if !containsExactProcessArgument("/Applications/Chrome "+wanted+" --headless", wanted) {
		t.Fatal("exact profile argument was not recognized")
	}
	if containsExactProcessArgument("/Applications/Chrome "+wanted+"-attacker --headless", wanted) {
		t.Fatal("profile prefix collision was accepted as an exact argument")
	}
}

func TestChromeSupervisorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CHROME_SUPERVISOR_HELPER") != "1" {
		return
	}
	role := os.Getenv("CHROME_HELPER_ROLE")
	if role == "chrome" {
		signal.Ignore(syscall.SIGTERM)
		observedPIDPath := os.Getenv("CHROME_HELPER_OBSERVED_PID_PATH")
		if err := os.WriteFile(observedPIDPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(95)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	if role != "node" {
		os.Exit(91)
	}
	profileDir := os.Getenv("CHROME_HELPER_PROFILE")
	pidPath := os.Getenv("CHROME_HELPER_PID_PATH")
	observedPIDPath := os.Getenv("CHROME_HELPER_OBSERVED_PID_PATH")
	child := exec.Command(os.Args[0], "-test.run=TestChromeSupervisorHelperProcess", "--", "--user-data-dir="+profileDir)
	child.Env = append(os.Environ(), "CHROME_HELPER_ROLE=chrome")
	if err := child.Start(); err != nil {
		os.Exit(92)
	}
	record, _ := json.Marshal(chromeProcessRecord{PID: child.Process.Pid, ProfileDir: profileDir})
	if err := os.WriteFile(pidPath, record, 0o600); err != nil {
		_ = child.Process.Kill()
		os.Exit(93)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(observedPIDPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			os.Exit(94)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(23)
}
