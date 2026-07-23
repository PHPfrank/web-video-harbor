package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

type chromeProcessRecord struct {
	PID        int    `json:"pid"`
	ProfileDir string `json:"profileDir"`
}

var errProcessNotFound = errors.New("process not found")

func superviseChromeCommand(command *exec.Cmd, pidPath, browserRoot string) (output []byte, err error) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Signal(syscall.SIGTERM)
	}
	command.WaitDelay = 7 * time.Second
	defer func() {
		err = errors.Join(err, cleanupChromeFromPIDFile(pidPath, browserRoot))
	}()
	return command.CombinedOutput()
}

func cleanupChromeFromPIDFile(pidPath, browserRoot string) error {
	recordBytes, err := os.ReadFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Chrome PID file: %w", err)
	}
	var record chromeProcessRecord
	if err := json.Unmarshal(recordBytes, &record); err != nil {
		return fmt.Errorf("decode Chrome PID file: %w", err)
	}
	if record.PID <= 1 || !pathWithin(record.ProfileDir, browserRoot) {
		return errors.New("refuse unsafe Chrome process record")
	}
	commandLine, commandErr := processCommand(record.PID)
	if errors.Is(commandErr, errProcessNotFound) {
		return removePIDFile(pidPath)
	}
	if commandErr != nil {
		return commandErr
	}
	wantedProfileArgument := "--user-data-dir=" + record.ProfileDir
	if !containsExactProcessArgument(commandLine, wantedProfileArgument) {
		return fmt.Errorf("refuse Chrome PID %d without exact profile argument", record.PID)
	}
	process, err := os.FindProcess(record.PID)
	if err != nil {
		return fmt.Errorf("find Chrome PID %d: %w", record.PID, err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("terminate Chrome PID %d: %w", record.PID, err)
	}
	if waitForProcessExit(record.PID, 2*time.Second) {
		return removePIDFile(pidPath)
	}
	if err := process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill Chrome PID %d: %w", record.PID, err)
	}
	if !waitForProcessExit(record.PID, 2*time.Second) {
		return fmt.Errorf("Chrome PID %d remained after SIGKILL", record.PID)
	}
	return removePIDFile(pidPath)
}

func containsExactProcessArgument(commandLine, wanted string) bool {
	for offset := 0; offset <= len(commandLine); {
		index := strings.Index(commandLine[offset:], wanted)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(wanted)
		leftOK := start == 0
		if !leftOK {
			value, _ := utf8.DecodeLastRuneInString(commandLine[:start])
			leftOK = unicode.IsSpace(value)
		}
		rightOK := end == len(commandLine)
		if !rightOK {
			value, _ := utf8.DecodeRuneInString(commandLine[end:])
			rightOK = unicode.IsSpace(value)
		}
		if leftOK && rightOK {
			return true
		}
		offset = start + 1
	}
	return false
}

func processCommand(pid int) (string, error) {
	output, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(string(output)) == "" {
			return "", errProcessNotFound
		}
		return "", fmt.Errorf("inspect Chrome PID %d: %w", pid, err)
	}
	commandLine := strings.TrimSpace(string(output))
	if commandLine == "" {
		return "", errProcessNotFound
	}
	return commandLine, nil
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := processCommand(pid); errors.Is(err, errProcessNotFound) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	_, err := processCommand(pid)
	return errors.Is(err, errProcessNotFound)
}

func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func removePIDFile(pidPath string) error {
	if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Chrome PID file: %w", err)
	}
	return nil
}
