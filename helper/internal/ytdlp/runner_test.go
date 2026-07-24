package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"web-video-harbor/helper/internal/output"
)

const testYouTubeURL = "https://www.youtube.com/watch?v=_mVb1D8wHxg"

func TestYTDLPHelperProcess(t *testing.T) {
	if os.Getenv("WVH_FAKE_YTDLP") != "1" {
		return
	}

	args := os.Args
	for index, arg := range args {
		if arg == "--" {
			args = args[index+1:]
			break
		}
	}
	stagingDir := helperValueAfter(args, "--paths")
	stagingDir = strings.TrimPrefix(stagingDir, "home:")

	switch os.Getenv("WVH_FAKE_MODE") {
	case "success":
		fmt.Println("WVH_PROGRESS:\"video-137\"\t10%")
		fmt.Println("untrusted output https://signed.example.invalid/video?token=secret")
		fmt.Println("WVH_PROGRESS:\"video-137\"\t100%")
		fmt.Println("WVH_PROGRESS:\"audio-140\"\t5%")
		fmt.Println("WVH_PROGRESS:\"audio-140\"\t60%")
		fmt.Println("WVH_PROGRESS:\"audio-140\"\t100%")
		if err := os.WriteFile(filepath.Join(stagingDir, "media.mp4"), []byte("video bytes"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "fake helper failed")
			os.Exit(2)
		}
		os.Exit(0)
	case "success-long-stdout":
		fmt.Println(strings.Repeat("x", maxProgressLineBytes*128))
		fmt.Println("WVH_PROGRESS:\"combined-18\"\t55%")
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4"), []byte("video bytes"), 0o600)
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stderr, os.Getenv("WVH_FAKE_DIAGNOSTIC"))
		os.Exit(1)
	case "late-diagnostic-parent":
		fmt.Fprintln(os.Stderr, "generic parent failure")
		exitReader, exitWriter, err := os.Pipe()
		if err != nil {
			os.Exit(3)
		}
		readyReader, readyWriter, err := os.Pipe()
		if err != nil {
			_ = exitReader.Close()
			_ = exitWriter.Close()
			os.Exit(3)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestYTDLPHelperProcess$")
		child.Env = append(os.Environ(), "WVH_FAKE_YTDLP=1", "WVH_FAKE_MODE=late-diagnostic-leaf")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.ExtraFiles = []*os.File{exitReader, readyWriter}
		if err := child.Start(); err != nil {
			_ = exitReader.Close()
			_ = exitWriter.Close()
			_ = readyReader.Close()
			_ = readyWriter.Close()
			os.Exit(3)
		}
		_ = exitReader.Close()
		_ = readyWriter.Close()
		defer exitWriter.Close()
		var ready [1]byte
		if _, err := io.ReadFull(readyReader, ready[:]); err != nil {
			_ = readyReader.Close()
			os.Exit(3)
		}
		_ = readyReader.Close()
		os.Exit(1)
	case "late-diagnostic-leaf":
		exitReader := os.NewFile(3, "parent-exit")
		readyWriter := os.NewFile(4, "child-ready")
		if exitReader == nil || readyWriter == nil {
			os.Exit(3)
		}
		if _, err := readyWriter.Write([]byte{1}); err != nil {
			os.Exit(3)
		}
		if err := readyWriter.Close(); err != nil {
			os.Exit(3)
		}
		var signal [1]byte
		if _, err := exitReader.Read(signal[:]); !errors.Is(err, io.EOF) {
			os.Exit(3)
		}
		_ = exitReader.Close()
		fmt.Fprintln(os.Stderr, "ERROR: Sign in to continue")
		os.Exit(0)
	case "tail-eviction":
		fmt.Fprintln(os.Stderr, "Sign in to continue")
		for range maxDiagnosticTailBytes {
			fmt.Fprintln(os.Stderr, "generic failure padding")
		}
		os.Exit(1)
	case "overlong-diagnostic":
		fmt.Fprintln(os.Stderr, strings.Repeat("x", maxDiagnosticLineBytes+1)+" Sign in to continue")
		os.Exit(1)
	case "replace-staging":
		moved := stagingDir + ".moved"
		_ = os.Rename(stagingDir, moved)
		_ = os.Mkdir(stagingDir, 0o700)
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4"), []byte("attacker replacement"), 0o600)
		os.Exit(0)
	case "no-output":
		os.Exit(0)
	case "multiple":
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4"), []byte("video"), 0o600)
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mkv"), []byte("video"), 0o600)
		os.Exit(0)
	case "zero-byte":
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4"), nil, 0o600)
		os.Exit(0)
	case "symlink":
		_ = os.Symlink(os.Getenv("WVH_FAKE_TARGET"), filepath.Join(stagingDir, "media.mp4"))
		os.Exit(0)
	case "directory":
		_ = os.Mkdir(filepath.Join(stagingDir, "media.mp4"), 0o700)
		os.Exit(0)
	case "unsupported":
		_ = os.WriteFile(filepath.Join(stagingDir, "media.exe"), []byte("video"), 0o600)
		os.Exit(0)
	case "extra-part":
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4"), []byte("video"), 0o600)
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4.part"), []byte("partial"), 0o600)
		os.Exit(0)
	case "cancel-tree":
		ignoreTermination := make(chan os.Signal, 1)
		signal.Notify(ignoreTermination, syscall.SIGTERM)
		defer signal.Stop(ignoreTermination)
		markerDir := os.Getenv("WVH_FAKE_MARKER_DIR")
		go func() {
			<-ignoreTermination
			_ = os.WriteFile(filepath.Join(markerDir, "parent.term"), []byte("term"), 0o600)
		}()
		_ = os.WriteFile(filepath.Join(stagingDir, "media.mp4.part"), []byte("partial"), 0o600)
		child := exec.Command(os.Args[0], "-test.run=^TestYTDLPHelperProcess$")
		child.Env = append(os.Environ(), "WVH_FAKE_YTDLP=1", "WVH_FAKE_MODE=cancel-leaf")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(markerDir, "leaf.ready")); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := writePIDMarker(filepath.Join(markerDir, "parent.pid"), os.Getpid()); err != nil {
			os.Exit(4)
		}
		if err := writePIDMarker(filepath.Join(markerDir, "child.pid"), child.Process.Pid); err != nil {
			os.Exit(4)
		}
		_ = child.Wait()
		os.Exit(0)
	case "cancel-leaf":
		ignoreTermination := make(chan os.Signal, 1)
		signal.Notify(ignoreTermination, syscall.SIGTERM)
		defer signal.Stop(ignoreTermination)
		markerDir := os.Getenv("WVH_FAKE_MARKER_DIR")
		_ = os.WriteFile(filepath.Join(markerDir, "leaf.ready"), []byte("ready"), 0o600)
		go func() {
			<-ignoreTermination
			_ = os.WriteFile(filepath.Join(markerDir, "leaf.term"), []byte("term"), 0o600)
		}()
		select {}
	case "escaped-pipe-holder-parent":
		markerDir := os.Getenv("WVH_FAKE_MARKER_DIR")
		child := exec.Command(os.Args[0], "-test.run=^TestYTDLPHelperProcess$")
		child.Env = append(os.Environ(), "WVH_FAKE_YTDLP=1", "WVH_FAKE_MODE=escaped-pipe-holder-leaf")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := writePIDMarker(filepath.Join(markerDir, "escaped.pid"), child.Process.Pid); err != nil {
			os.Exit(4)
		}
		select {}
	case "escaped-pipe-holder-leaf":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "fake helper mode is invalid")
		os.Exit(2)
	}
}

func writePIDMarker(path string, pid int) error {
	if pid <= 0 {
		return errors.New("pid marker value is invalid")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, writeErr := temporary.WriteString(strconv.Itoa(pid))
	closeErr := temporary.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Rename(temporaryPath, path)
}

func TestRunnerCancellationTerminatesProcessGroupAndCleansStaging(t *testing.T) {
	config := testConfig(t)
	markerDir := t.TempDir()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("cancel-tree", []string{"WVH_FAKE_MARKER_DIR=" + markerDir})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, validRequest("取消平台视频"))
		result <- err
	}()

	parentPID := waitForPIDFile(t, filepath.Join(markerDir, "parent.pid"))
	childPID := waitForPIDFile(t, filepath.Join(markerDir, "child.pid"))
	cancel()
	select {
	case err := <-result:
		assertRunnerCode(t, err, CodeCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error does not wrap context.Canceled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	assertProcessGone(t, parentPID)
	assertProcessGone(t, childPID)
	assertFileContents(t, filepath.Join(markerDir, "parent.term"), "term")
	assertFileContents(t, filepath.Join(markerDir, "leaf.term"), "term")
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerCancellationBoundsDrainFromEscapedDescendant(t *testing.T) {
	config := testConfig(t)
	markerDir := t.TempDir()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("escaped-pipe-holder-parent", []string{"WVH_FAKE_MARKER_DIR=" + markerDir})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, validRequest("逃逸后代取消"))
		result <- runErr
	}()

	escapedPID := waitForPIDFile(t, filepath.Join(markerDir, "escaped.pid"))
	t.Cleanup(func() { _ = syscall.Kill(escapedPID, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		assertRunnerCode(t, err, CodeCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error does not wrap context.Canceled: %v", err)
		}
	case <-time.After(1 * time.Second):
		_ = syscall.Kill(escapedPID, syscall.SIGKILL)
		select {
		case <-result:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("Run() waited without bound for an escaped descendant to close inherited output pipes")
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestConfirmProcessGroupExitPollsUntilGoneAndStaysBounded(t *testing.T) {
	checks := 0
	exited := confirmProcessGroupExit(123, 100*time.Millisecond, func(int) bool {
		checks++
		return checks < 3
	})
	if !exited || checks != 3 {
		t.Fatalf("confirmProcessGroupExit() = %t after %d checks, want true after 3", exited, checks)
	}

	started := time.Now()
	exited = confirmProcessGroupExit(123, 20*time.Millisecond, func(int) bool { return true })
	if exited {
		t.Fatal("confirmProcessGroupExit() reported a persistent group gone")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("confirmation exceeded its bound: %v", elapsed)
	}
}

func TestRunnerClassifiesBoundedDiagnosticsIntoStableSafeErrors(t *testing.T) {
	tests := []struct {
		name        string
		diagnostic  string
		wantCode    Code
		wantMessage string
	}{
		{
			name:        "login required",
			diagnostic:  "ERROR: Sign in to confirm you are not a bot; use --cookies-from-browser",
			wantCode:    CodeLoginRequired,
			wantMessage: "当前视频需要登录，v0.2.0 暂不支持",
		},
		{
			name:        "private member or paid",
			diagnostic:  "ERROR: This video is private and available to members-only accounts",
			wantCode:    CodeAccessLimited,
			wantMessage: "当前内容受会员、付费或私有访问限制",
		},
		{
			name:        "geo restricted",
			diagnostic:  "ERROR: This video is not available in your country due to geo restriction",
			wantCode:    CodeGeoRestricted,
			wantMessage: "当前网络所在地区无法访问此视频",
		},
		{
			name:        "extractor outdated",
			diagnostic:  "ERROR: Unable to extract nsig function; please update yt-dlp",
			wantCode:    CodeExtractor,
			wantMessage: "平台解析规则已变化，请升级网页视频港",
		},
		{
			name:        "ffmpeg missing",
			diagnostic:  "ERROR: Postprocessing failed because ffmpeg not found",
			wantCode:    CodeFFmpegMissing,
			wantMessage: "未找到可用的 FFmpeg，请安装或修复后重试",
		},
		{
			name:        "network",
			diagnostic:  "ERROR: Unable to download webpage: network is unreachable",
			wantCode:    CodeNetwork,
			wantMessage: "网络连接失败，请检查网络后重试",
		},
		{
			name:        "generic process failure",
			diagnostic:  "ERROR: rejected https://signed.example.invalid/video?token=top-secret /Users/private/file",
			wantCode:    CodeProcess,
			wantMessage: "平台暂时拒绝了下载，请稍后重试",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig(t)
			runner, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			runner.commandFactory = fakeCommandFactory("failure", []string{"WVH_FAKE_DIAGNOSTIC=" + test.diagnostic})
			path, err := runner.Run(context.Background(), validRequest("错误分类"))
			if path != "" || err == nil {
				t.Fatalf("Run() = (%q, %v), want classified error", path, err)
			}
			assertRunnerCode(t, err, test.wantCode)
			if err.Error() != test.wantMessage {
				t.Fatalf("Error() = %q, want %q", err, test.wantMessage)
			}
			for _, secret := range []string{"signed.example.invalid", "top-secret", "/Users/private", testYouTubeURL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("safe error leaked %q: %v", secret, err)
				}
			}
			assertNoPlatformStaging(t, config.OutputDir)
		})
	}
}

func TestRunnerEnforcesDiagnosticLineAndTailLimits(t *testing.T) {
	for _, mode := range []string{"tail-eviction", "overlong-diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			config := testConfig(t)
			runner, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			runner.commandFactory = fakeCommandFactory(mode, nil)
			_, err = runner.Run(context.Background(), validRequest("有界诊断"))
			assertRunnerCode(t, err, CodeProcess)
			assertNoPlatformStaging(t, config.OutputDir)
		})
	}
}

func TestRunnerDrainsOverlongStdoutAndContinuesParsingProgress(t *testing.T) {
	config := testConfig(t)
	var progress []float64
	config.OnProgress = func(update Progress) { progress = append(progress, update.Percent) }
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success-long-stdout", nil)
	if _, err := runner.Run(context.Background(), validRequest("超长输出")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := []float64{27.225}; !reflect.DeepEqual(progress, want) {
		t.Fatalf("progress = %#v, want %#v", progress, want)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerWaitsForLateDiagnosticWriterBeforeClassifying(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("late-diagnostic-parent", nil)
	_, err = runner.Run(context.Background(), validRequest("等待诊断"))
	var runnerError *Error
	if !errors.As(err, &runnerError) || runnerError.Code != CodeLoginRequired {
		t.Fatalf("error = %#v, want diagnostic written only after parent exit", err)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRejectsReplacedStagingDirectoryWithoutDeletingReplacement(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("replace-staging", nil)
	path, err := runner.Run(context.Background(), validRequest("目录替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	matches, err := filepath.Glob(filepath.Join(config.OutputDir, ".web-video-platform-*"))
	if err != nil {
		t.Fatal(err)
	}
	var replacementFound bool
	for _, match := range matches {
		contents, readErr := os.ReadFile(filepath.Join(match, "media.mp4"))
		if readErr == nil && string(contents) == "attacker replacement" {
			replacementFound = true
		}
	}
	if !replacementFound {
		t.Fatal("runner deleted or lost the replacement staging directory")
	}
}

func TestRunnerReturnsPublishedErrorWhenCleanupFailsAfterPublication(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.removeTree = func(*os.File) error { return errors.New("simulated cleanup warning") }

	path, err := runner.Run(context.Background(), validRequest("清理警告"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus warning", path, err)
	}
	publishedPath, ok := output.PublishedPath(err)
	if !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	assertRunnerCode(t, err, CodeOutput)
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "simulated cleanup warning") {
		t.Fatalf("published warning leaked internal data: %v", err)
	}
	assertFileContents(t, path, "video bytes")
}

func TestRunnerPreservesProcessFailureAndCleanupFailureInSafeErrorChain(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("failure", []string{"WVH_FAKE_DIAGNOSTIC=generic failure"})
	runner.removeTree = func(*os.File) error { return errors.New("private cleanup detail") }

	path, err := runner.Run(context.Background(), validRequest("失败清理"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want process and cleanup errors", path, err)
	}
	assertRunnerCode(t, err, CodeProcess)
	assertRunnerErrorChainContainsCode(t, err, CodeOutput)
	if strings.Contains(err.Error(), "private cleanup detail") {
		t.Fatalf("cleanup detail leaked through safe error chain: %v", err)
	}
}

func TestRunnerPreservesCancellationAndCleanupFailureInSafeErrorChain(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.removeTree = func(*os.File) error { return errors.New("private cleanup detail") }
	copyStarted := make(chan struct{})
	runner.copyOutput = func(ctx context.Context, _ io.Writer, _ io.Reader) error {
		close(copyStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, validRequest("取消清理"))
		result <- runErr
	}()
	select {
	case <-copyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not begin output copy")
	}
	cancel()

	select {
	case err := <-result:
		assertRunnerCode(t, err, CodeCanceled)
		assertRunnerErrorChainContainsCode(t, err, CodeOutput)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation chain lost context.Canceled: %v", err)
		}
		if strings.Contains(err.Error(), "private cleanup detail") {
			t.Fatalf("cleanup detail leaked through safe error chain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not return after cancellation")
	}
}

func TestRunnerQuarantinesAndRechecksStagingBeforeRecursiveCleanup(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.beforeCleanupRename = func(path string) {
		moved := path + ".owned-moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatalf("move owned staging before cleanup race: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create cleanup replacement: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "must-survive.txt"), []byte("replacement"), 0o600); err != nil {
			t.Fatalf("write cleanup replacement marker: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("清理隔离"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if publishedPath, ok := output.PublishedPath(err); !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	var replacementFound bool
	walkErr := filepath.Walk(config.OutputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Name() != "must-survive.txt" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr == nil && string(contents) == "replacement" {
			replacementFound = true
		}
		return readErr
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if !replacementFound {
		t.Fatal("recursive cleanup deleted the replacement staging directory")
	}
}

func TestRunnerDoesNotRecursivelyDeleteReplacedQuarantineRoot(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.beforeCleanupRemove = func(path string) {
		moved := path + ".owned-moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatalf("move quarantine root before cleanup race: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create replacement quarantine root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "must-survive.txt"), []byte("replacement"), 0o600); err != nil {
			t.Fatalf("write quarantine replacement marker: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("根目录隔离"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if publishedPath, ok := output.PublishedPath(err); !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	if !treeContainsFileContents(t, config.OutputDir, "replacement") {
		t.Fatal("cleanup lost the replacement quarantine contents while isolating it")
	}
}

func TestRunnerPreservesEmptyReplacementOfQuarantineRootBeforeFinalRemoval(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var replacementInfo os.FileInfo
	runner.beforeCleanupRemove = func(path string) {
		if err := os.Rename(path, path+".owned-moved"); err != nil {
			t.Fatalf("move quarantine root before final removal: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create empty quarantine replacement: %v", err)
		}
		replacementInfo, err = os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect empty quarantine replacement: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("空根替换"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if publishedPath, ok := output.PublishedPath(err); !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	if !treeContainsSameFile(t, config.OutputDir, replacementInfo) {
		t.Fatal("final cleanup deleted the empty replacement quarantine inode")
	}
}

func TestRunnerPreservesReplacementOfFinalRootCleanupGuard(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var replacementInfo os.FileInfo
	runner.beforeCleanupGuardRemove = func(parent *os.File, name string) {
		if replacementInfo != nil || !strings.HasPrefix(name, ".web-video-platform-root-cleanup-") {
			return
		}
		if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
			t.Fatalf("move final root cleanup guard: %v", err)
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			t.Fatalf("create final root cleanup replacement: %v", err)
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("open final root cleanup replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), name)
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap final root cleanup replacement")
		}
		replacementInfo, err = replacement.Stat()
		if closeErr := replacement.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("inspect final root cleanup replacement: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("最终根隔离"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if publishedPath, ok := output.PublishedPath(err); !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	if !treeContainsSameFile(t, config.OutputDir, replacementInfo) {
		t.Fatal("cleanup deleted the replacement final root guard inode")
	}
}

func TestRunnerPreservesReplacementOfRecursiveCleanupGuard(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var replacementInfo os.FileInfo
	runner.beforeCleanupGuardRemove = func(parent *os.File, name string) {
		if replacementInfo != nil || parent.Name() != "owned-staging" ||
			!strings.HasPrefix(name, ".web-video-platform-entry-cleanup-") {
			return
		}
		if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
			t.Fatalf("move recursive cleanup guard: %v", err)
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			t.Fatalf("create recursive cleanup guard replacement: %v", err)
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("open recursive cleanup guard replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), name)
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap recursive cleanup guard replacement")
		}
		replacementInfo, err = replacement.Stat()
		if closeErr := replacement.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("inspect recursive cleanup guard replacement: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("递归隔离目录"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if publishedPath, ok := output.PublishedPath(err); !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	if !treeContainsSameFile(t, config.OutputDir, replacementInfo) {
		t.Fatal("cleanup deleted the replacement recursive guard inode")
	}
}

func TestRunnerPreservesReplacementRacedBeforeRecursiveEntryIsolation(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	replaced := false
	runner.beforeCleanupEntryIsolation = func(directory *os.File, name string) {
		if replaced || filepath.Ext(name) != ".mp4" {
			return
		}
		replaced = true
		if err := unix.Renameat(int(directory.Fd()), name, int(directory.Fd()), name+".owned"); err != nil {
			t.Fatalf("move validated cleanup entry: %v", err)
		}
		fd, err := unix.Openat(
			int(directory.Fd()),
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		if err != nil {
			t.Fatalf("create cleanup replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), name)
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap cleanup replacement")
		}
		if _, err := replacement.Write([]byte("recursive replacement")); err != nil {
			t.Fatal(err)
		}
		if err := replacement.Close(); err != nil {
			t.Fatal(err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("递归项替换"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus cleanup warning", path, err)
	}
	if !treeContainsFileContents(t, config.OutputDir, "recursive replacement") {
		t.Fatal("recursive cleanup deleted the replacement raced in before entry isolation")
	}
}

func TestRecursiveCleanupPreservesReplacementAfterEntryIsolation(t *testing.T) {
	rootPath := t.TempDir()
	ownedPath := filepath.Join(rootPath, "owned-dir")
	if err := os.Mkdir(ownedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownedPath, "owned.txt"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var replacementInfo os.FileInfo
	afterIsolation := func(parent *os.File, name string) {
		if replacementInfo != nil || name != "isolated-entry" {
			return
		}
		replacementInfo = replaceIsolatedDirectoryEntry(t, parent, name, "replacement-after-isolation")
	}

	err = removeDirectoryContentsWithHook(root, nil, afterIsolation, nil)
	if err == nil {
		t.Fatal("recursive cleanup reported success after the isolated directory was replaced")
	}
	if replacementInfo == nil {
		t.Fatal("recursive cleanup did not exercise the post-isolation replacement")
	}
	if !treeContainsSameFile(t, rootPath, replacementInfo) {
		t.Fatal("recursive cleanup deleted the replacement directory opened after isolation")
	}
	if !treeContainsFileContents(t, rootPath, "replacement-after-isolation") {
		t.Fatal("recursive cleanup deleted contents of the replacement opened after isolation")
	}
}

func TestRecursiveCleanupPreservesReplacementBeforeFinalDirectoryRemove(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "owned-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var replacementInfo os.FileInfo
	beforeGuardRemove := func(parent *os.File, name string) {
		if replacementInfo != nil || name != "isolated-entry" {
			return
		}
		replacementInfo = replaceIsolatedDirectoryEntry(t, parent, name, "")
	}

	err = removeDirectoryContentsWithHook(root, nil, nil, beforeGuardRemove)
	if err == nil {
		t.Fatal("recursive cleanup reported success after final directory replacement")
	}
	if replacementInfo == nil {
		t.Fatal("recursive cleanup bypassed the pinned final directory guard")
	}
	if !treeContainsSameFile(t, rootPath, replacementInfo) {
		t.Fatal("recursive cleanup deleted the replacement before final directory removal")
	}
}

func replaceIsolatedDirectoryEntry(t *testing.T, parent *os.File, name, contents string) os.FileInfo {
	t.Helper()
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
		t.Fatalf("move isolated directory entry: %v", err)
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		t.Fatalf("create isolated directory replacement: %v", err)
	}
	fd, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatalf("open isolated directory replacement: %v", err)
	}
	replacement := os.NewFile(uintptr(fd), name)
	if replacement == nil {
		_ = unix.Close(fd)
		t.Fatal("wrap isolated directory replacement")
	}
	if contents != "" {
		markerFD, openErr := unix.Openat(
			int(replacement.Fd()),
			"must-survive.txt",
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		if openErr != nil {
			_ = replacement.Close()
			t.Fatalf("create isolated replacement marker: %v", openErr)
		}
		marker := os.NewFile(uintptr(markerFD), "must-survive.txt")
		if marker == nil {
			_ = unix.Close(markerFD)
			_ = replacement.Close()
			t.Fatal("wrap isolated replacement marker")
		}
		_, writeErr := marker.WriteString(contents)
		closeErr := marker.Close()
		if writeErr != nil || closeErr != nil {
			_ = replacement.Close()
			t.Fatalf("write isolated replacement marker: %v", errors.Join(writeErr, closeErr))
		}
	}
	info, statErr := replacement.Stat()
	closeErr := replacement.Close()
	if statErr != nil || closeErr != nil {
		t.Fatalf("inspect isolated directory replacement: %v", errors.Join(statErr, closeErr))
	}
	return info
}

func TestIsolateOwnedEntryPreservesReplacementWhenRenameFails(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(rootPath, "owned.mp4"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectedInfo, err := fileInfoAt(root, "owned.mp4")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := identityFromFileInfo(expectedInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(rootPath, "owned.mp4"), filepath.Join(rootPath, "owned.mp4.moved")); err != nil {
		t.Fatal(err)
	}
	var replacementInfo os.FileInfo
	beforeGuardRemove := func(parent *os.File, name string) {
		if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
			t.Fatalf("move failed-isolation guard: %v", err)
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			t.Fatalf("create failed-isolation guard replacement: %v", err)
		}
		replacementInfo, err = fileInfoAt(parent, name)
		if err != nil {
			t.Fatalf("inspect failed-isolation guard replacement: %v", err)
		}
	}

	_, _, matched, err := isolateOwnedEntryAt(
		root,
		"owned.mp4",
		expected,
		".web-video-platform-entry-cleanup-",
		beforeGuardRemove,
	)
	if err == nil || matched {
		t.Fatalf("isolateOwnedEntryAt() = matched %t, err %v; want failed rename", matched, err)
	}
	if replacementInfo == nil {
		t.Fatal("isolateOwnedEntryAt() bypassed final guard validation after rename failure")
	}
	if !treeContainsSameFile(t, rootPath, replacementInfo) {
		t.Fatal("isolateOwnedEntryAt() deleted the replacement guard after rename failure")
	}
}

func TestCreatePrivateDirectoryPreservesReplacementOnFailure(t *testing.T) {
	tests := []struct {
		name string
		ops  func(*testing.T, *os.File, *os.FileInfo) privateDirectoryOps
	}{
		{
			name: "open",
			ops: func(t *testing.T, root *os.File, replacementInfo *os.FileInfo) privateDirectoryOps {
				return privateDirectoryOps{
					open: func(parentFD int, name string, _ int, _ uint32) (int, error) {
						*replacementInfo = replacePrivateDirectoryEntry(t, parentFD, name)
						return -1, syscall.EIO
					},
				}
			},
		},
		{
			name: "wrap",
			ops: func(t *testing.T, root *os.File, replacementInfo *os.FileInfo) privateDirectoryOps {
				return privateDirectoryOps{
					wrap: func(_ uintptr, name string) *os.File {
						*replacementInfo = replacePrivateDirectoryEntry(t, int(root.Fd()), name)
						return nil
					},
				}
			},
		},
		{
			name: "fchmod",
			ops: func(t *testing.T, root *os.File, replacementInfo *os.FileInfo) privateDirectoryOps {
				var createdName string
				return privateDirectoryOps{
					open: func(parentFD int, name string, flags int, mode uint32) (int, error) {
						createdName = name
						return unix.Openat(parentFD, name, flags, mode)
					},
					fchmod: func(_ int, _ uint32) error {
						*replacementInfo = replacePrivateDirectoryEntry(t, int(root.Fd()), createdName)
						return syscall.EIO
					},
				}
			},
		},
		{
			name: "stat",
			ops: func(t *testing.T, root *os.File, replacementInfo *os.FileInfo) privateDirectoryOps {
				return privateDirectoryOps{
					stat: func(directory *os.File) (os.FileInfo, error) {
						*replacementInfo = replacePrivateDirectoryEntry(t, int(root.Fd()), directory.Name())
						return nil, syscall.EIO
					},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := os.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var replacementInfo os.FileInfo

			if _, directory, _, err := createPrivateDirectoryAt(
				root,
				".create-failure-",
				test.ops(t, root, &replacementInfo),
			); err == nil || directory != nil {
				t.Fatalf("createPrivateDirectoryAt() = directory %v, err %v; want %s failure", directory, err, test.name)
			}
			if replacementInfo == nil {
				t.Fatalf("%s failure injection did not create a replacement", test.name)
			}
			if !treeContainsSameFile(t, rootPath, replacementInfo) {
				t.Fatalf("%s failure cleanup deleted the replacement private directory", test.name)
			}
		})
	}
}

func replacePrivateDirectoryEntry(t *testing.T, parentFD int, name string) os.FileInfo {
	t.Helper()
	if err := unix.Renameat(parentFD, name, parentFD, name+".owned"); err != nil {
		t.Fatalf("move private directory before failure cleanup: %v", err)
	}
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		t.Fatalf("create private directory replacement: %v", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatalf("inspect private directory replacement: %v", err)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open private directory replacement: %v", err)
	}
	replacement := os.NewFile(uintptr(fd), name)
	if replacement == nil {
		_ = unix.Close(fd)
		t.Fatal("wrap private directory replacement")
	}
	info, err := replacement.Stat()
	if closeErr := replacement.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("stat private directory replacement: %v", err)
	}
	return info
}

func TestRunnerRejectsStagedFileReplacementBetweenLstatAndOpen(t *testing.T) {
	config := testConfig(t)
	target := filepath.Join(t.TempDir(), "target.mp4")
	if err := os.WriteFile(target, []byte("do not publish"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.beforeOpenStaged = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove staged file: %v", err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("replace staged file with symlink: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("句柄复核"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	assertFileContents(t, target, "do not publish")
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerDoesNotBlockWhenCandidateIsReplacedByFIFO(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var openStarted time.Time
	runner.beforeOpenStaged = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove staged candidate: %v", err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("replace candidate with FIFO: %v", err)
		}
		openStarted = time.Now()
		go func() {
			time.Sleep(300 * time.Millisecond)
			writer, openErr := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
			if openErr == nil {
				_ = writer.Close()
			}
		}()
	}

	path, err := runner.Run(context.Background(), validRequest("FIFO替换"))
	elapsed := time.Since(openStarted)
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("opening replaced FIFO blocked for %v", elapsed)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRechecksStagingIdentityAfterOpeningCandidate(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.beforeOpenStaged = func(path string) {
		stagingDir := filepath.Dir(path)
		movedDir := stagingDir + ".moved"
		if err := os.Rename(stagingDir, movedDir); err != nil {
			t.Fatalf("move owned staging: %v", err)
		}
		if err := os.Mkdir(stagingDir, 0o700); err != nil {
			t.Fatalf("create replacement staging: %v", err)
		}
		if err := os.Link(filepath.Join(movedDir, filepath.Base(path)), path); err != nil {
			t.Fatalf("link original candidate into replacement: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("目录二次复核"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error before publication", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
}

func TestRunnerUsesFixedBinaryMinimalEnvironmentAndPrivateDirectChildStaging(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	baseFactory := fakeCommandFactory("success", nil)
	var gotPath string
	var gotArgs []string
	var gotEnv []string
	var gotStaging string
	var gotStagingMode os.FileMode
	runner.commandFactory = func(path string, args []string, env []string) *exec.Cmd {
		gotPath = path
		gotArgs = append([]string{}, args...)
		gotEnv = append([]string{}, env...)
		gotStaging = strings.TrimPrefix(helperValueAfter(args, "--paths"), "home:")
		info, statErr := os.Lstat(gotStaging)
		if statErr != nil {
			t.Fatalf("inspect staging before process start: %v", statErr)
		}
		gotStagingMode = info.Mode()
		return baseFactory(path, args, env)
	}

	if _, err := runner.Run(context.Background(), validRequest("固定进程")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotPath != config.BinaryPath {
		t.Fatalf("command path = %q, want fixed binary %q", gotPath, config.BinaryPath)
	}
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != testYouTubeURL {
		t.Fatalf("fixed args lost canonical URL: %#v", gotArgs)
	}
	wantEnv := []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("command environment = %#v, want %#v", gotEnv, wantEnv)
	}
	if filepath.Dir(gotStaging) != config.OutputDir || !strings.HasPrefix(filepath.Base(gotStaging), ".web-video-platform-") {
		t.Fatalf("staging path = %q, want direct private child of output", gotStaging)
	}
	if !gotStagingMode.IsDir() || gotStagingMode.Perm() != 0o700 || gotStagingMode&os.ModeSymlink != 0 {
		t.Fatalf("staging mode = %v, want real 0700 directory", gotStagingMode)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRejectsOutputRootReplacedWithSymlinkAfterNew(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads")
	attackerDir := filepath.Join(root, "attacker")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.OutputDir = outputDir
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	baseFactory := fakeCommandFactory("success", nil)
	runner.commandFactory = func(path string, args []string, env []string) *exec.Cmd {
		started = true
		return baseFactory(path, args, env)
	}
	if err := os.Rename(outputDir, filepath.Join(root, "owned-downloads")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attackerDir, outputDir); err != nil {
		t.Fatal(err)
	}

	path, err := runner.Run(context.Background(), validRequest("根目录替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if started {
		t.Fatal("runner started the platform process after output root replacement")
	}
	assertDirectoryEmpty(t, attackerDir)
}

func TestRunnerRejectsOutputRootReplacementDuringCopy(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads")
	ownedDir := filepath.Join(root, "owned-downloads")
	attackerDir := filepath.Join(root, "attacker")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.OutputDir = outputDir
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.copyOutput = func(ctx context.Context, destination io.Writer, source io.Reader) error {
		if err := copyWithContext(ctx, destination, source); err != nil {
			return err
		}
		if err := os.Rename(outputDir, ownedDir); err != nil {
			t.Fatalf("move output root during copy: %v", err)
		}
		if err := os.Symlink(attackerDir, outputDir); err != nil {
			t.Fatalf("replace output root with symlink: %v", err)
		}
		return nil
	}

	path, err := runner.Run(context.Background(), validRequest("复制根替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error without published path", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if publishedPath, ok := output.PublishedPath(err); ok {
		t.Fatalf("replaced root produced published path %q", publishedPath)
	}
	assertDirectoryEmpty(t, attackerDir)
	assertNoPublishedVideo(t, ownedDir)
}

func TestRunnerRejectsOutputRootReplacementDuringDeferredCleanup(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads")
	ownedDir := filepath.Join(root, "owned-downloads")
	attackerDir := filepath.Join(root, "attacker")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.OutputDir = outputDir
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.beforeCleanupRemove = func(string) {
		if err := os.Rename(outputDir, ownedDir); err != nil {
			t.Fatalf("move output root during deferred cleanup: %v", err)
		}
		if err := os.Symlink(attackerDir, outputDir); err != nil {
			t.Fatalf("replace output root during deferred cleanup: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("清理根替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error without a published path", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if publishedPath, ok := output.PublishedPath(err); ok {
		t.Fatalf("replaced root produced published path %q", publishedPath)
	}
	assertDirectoryEmpty(t, attackerDir)
	entries, readErr := os.ReadDir(ownedDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var published int
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".web-video-platform-") {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("published videos in moved owned root = %d, want 1", published)
	}
}

func TestRunnerPreservesProcessFailureWhenOutputRootChangesDuringCleanup(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads")
	ownedDir := filepath.Join(root, "owned-downloads")
	attackerDir := filepath.Join(root, "attacker")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.OutputDir = outputDir
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("failure", []string{"WVH_FAKE_DIAGNOSTIC=generic failure"})
	runner.beforeCleanupRemove = func(string) {
		if err := os.Rename(outputDir, ownedDir); err != nil {
			t.Fatalf("move output root during failure cleanup: %v", err)
		}
		if err := os.Symlink(attackerDir, outputDir); err != nil {
			t.Fatalf("replace output root during failure cleanup: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("失败根替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want process and output errors", path, err)
	}
	assertRunnerCode(t, err, CodeProcess)
	assertRunnerErrorChainContainsCode(t, err, CodeOutput)
	if publishedPath, ok := output.PublishedPath(err); ok {
		t.Fatalf("failure after root replacement produced published path %q", publishedPath)
	}
	assertDirectoryEmpty(t, attackerDir)
}

func TestRunnerPreservesCancellationWithoutStalePublicationWhenRootChangesDuringCleanup(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads")
	ownedDir := filepath.Join(root, "owned-downloads")
	attackerDir := filepath.Join(root, "attacker")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.OutputDir = outputDir
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	ctx, cancel := context.WithCancel(context.Background())
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, reserveErr := output.ReserveAvailablePathAt(root, dir, base, extension)
		if reserveErr != nil {
			return nil, reserveErr
		}
		return &failingReservation{inner: reservation, onPublish: cancel}, nil
	}
	runner.beforeCleanupRemove = func(string) {
		if err := os.Rename(outputDir, ownedDir); err != nil {
			t.Fatalf("move output root during canceled cleanup: %v", err)
		}
		if err := os.Symlink(attackerDir, outputDir); err != nil {
			t.Fatalf("replace output root during canceled cleanup: %v", err)
		}
	}

	path, err := runner.Run(ctx, validRequest("取消根替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want canceled and output errors without a published path", path, err)
	}
	assertRunnerCode(t, err, CodeCanceled)
	assertRunnerErrorChainContainsCode(t, err, CodeOutput)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("root replacement lost context.Canceled: %v", err)
	}
	if publishedPath, ok := output.PublishedPath(err); ok {
		t.Fatalf("root replacement retained stale published path %q", publishedPath)
	}
	assertDirectoryEmpty(t, attackerDir)
}

func TestRunnerReturnsCanceledWithoutStartingWhenContextAlreadyCanceled(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	runner.commandFactory = func(path string, args []string, env []string) *exec.Cmd {
		started = true
		return fakeCommandFactory("success", nil)(path, args, env)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path, err := runner.Run(ctx, validRequest("预先取消"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want canceled error", path, err)
	}
	assertRunnerCode(t, err, CodeCanceled)
	if started {
		t.Fatal("runner started process for an already canceled context")
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerPreservesDeadlineExceededWithoutStarting(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	runner.commandFactory = func(path string, args []string, env []string) *exec.Cmd {
		started = true
		return fakeCommandFactory("success", nil)(path, args, env)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	path, err := runner.Run(ctx, validRequest("截止时间"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want deadline error", path, err)
	}
	assertRunnerCode(t, err, CodeCanceled)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error does not wrap context.DeadlineExceeded: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("deadline error incorrectly wraps context.Canceled: %v", err)
	}
	if started {
		t.Fatal("runner started process for an expired deadline")
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerMapsProcessStartFailureWithoutLeakingBinaryPath(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	path, err := runner.Run(context.Background(), validRequest("启动失败"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want process error", path, err)
	}
	assertRunnerCode(t, err, CodeProcess)
	if err.Error() != "平台暂时拒绝了下载，请稍后重试" || strings.Contains(err.Error(), config.BinaryPath) {
		t.Fatalf("unsafe process-start error: %v", err)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerCancellationInterruptsCopyAndDoesNotPublish(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	copyStarted := make(chan struct{})
	runner.copyOutput = func(ctx context.Context, _ io.Writer, _ io.Reader) error {
		close(copyStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, validRequest("复制取消"))
		result <- err
	}()
	select {
	case <-copyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not begin publishing copy")
	}
	cancel()
	select {
	case err := <-result:
		assertRunnerCode(t, err, CodeCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("copy cancellation does not wrap context.Canceled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not interrupt publishing copy")
	}
	assertNoPublishedVideo(t, config.OutputDir)
	assertNoPlatformStaging(t, config.OutputDir)
}

type failingReservation struct {
	inner               *output.Reservation
	releaseErr          error
	publishAfterSuccess error
	onPublish           func()
}

func (r *failingReservation) Path() string   { return r.inner.Path() }
func (r *failingReservation) File() *os.File { return r.inner.File() }
func (r *failingReservation) Release() error {
	if r.releaseErr != nil {
		return r.releaseErr
	}
	return r.inner.Release()
}
func (r *failingReservation) PublishExpected(expectedSize int64) error {
	if err := r.inner.PublishExpected(expectedSize); err != nil {
		return err
	}
	if r.onPublish != nil {
		r.onPublish()
	}
	return r.publishAfterSuccess
}

func TestRunnerReturnsPublishedCancellationWhenContextCancelsAfterPublish(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	ctx, cancel := context.WithCancel(context.Background())
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, err := output.ReserveAvailablePathAt(root, dir, base, extension)
		if err != nil {
			return nil, err
		}
		return &failingReservation{inner: reservation, onPublish: cancel}, nil
	}

	path, err := runner.Run(ctx, validRequest("发布后取消"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus canceled error", path, err)
	}
	assertRunnerCode(t, err, CodeCanceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-publication cancellation does not wrap context.Canceled: %v", err)
	}
	publishedPath, ok := output.PublishedPath(err)
	if !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	assertFileContents(t, path, "video bytes")
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRejectsSameSizeReplacementDuringPublish(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var replacementPath string
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, err := output.ReserveAvailablePathAt(root, dir, base, extension)
		if err != nil {
			return nil, err
		}
		replacementPath = reservation.Path()
		return &failingReservation{
			inner: reservation,
			onPublish: func() {
				if err := os.Rename(replacementPath, replacementPath+".owned"); err != nil {
					t.Fatalf("move owned published path: %v", err)
				}
				if err := os.WriteFile(replacementPath, []byte("video bytes"), 0o600); err != nil {
					t.Fatalf("create same-size replacement: %v", err)
				}
			},
		}, nil
	}

	path, err := runner.Run(context.Background(), validRequest("发布替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error without a published path", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if publishedPath, ok := output.PublishedPath(err); ok {
		t.Fatalf("unsafe replacement was reported as published: %q", publishedPath)
	}
	assertFileContents(t, replacementPath, "video bytes")
}

func TestRunnerRejectsShortSuccessfulCopy(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.copyOutput = func(_ context.Context, destination io.Writer, _ io.Reader) error {
		_, err := destination.Write([]byte("short"))
		return err
	}

	path, err := runner.Run(context.Background(), validRequest("短复制"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	assertNoPublishedVideo(t, config.OutputDir)
}

func TestRunnerRemovesPartialReservationWhenReleaseReportsFailure(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	var reservedPath string
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, err := output.ReserveAvailablePathAt(root, dir, base, extension)
		if err != nil {
			return nil, err
		}
		reservedPath = reservation.Path()
		return &failingReservation{inner: reservation, releaseErr: errors.New("simulated release failure")}, nil
	}
	runner.copyOutput = func(_ context.Context, destination io.Writer, _ io.Reader) error {
		_, _ = destination.Write([]byte("partial"))
		return errors.New("simulated copy failure")
	}

	path, err := runner.Run(context.Background(), validRequest("回滚失败"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if _, statErr := os.Lstat(reservedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial reservation remains after fallback cleanup: %v", statErr)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRollbackPreservesReplacementRacedAfterFallbackValidation(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, reserveErr := output.ReserveAvailablePathAt(root, dir, base, extension)
		if reserveErr != nil {
			return nil, reserveErr
		}
		return &failingReservation{inner: reservation, releaseErr: errors.New("simulated release failure")}, nil
	}
	runner.copyOutput = func(_ context.Context, destination io.Writer, _ io.Reader) error {
		_, _ = destination.Write([]byte("partial"))
		return errors.New("simulated copy failure")
	}
	runner.beforeRollbackIsolation = func(root *os.File, name string) {
		if err := unix.Renameat(int(root.Fd()), name, int(root.Fd()), name+".owned"); err != nil {
			t.Fatalf("move validated fallback reservation: %v", err)
		}
		fd, err := unix.Openat(
			int(root.Fd()),
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		if err != nil {
			t.Fatalf("create fallback replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), name)
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap fallback replacement")
		}
		if _, err := replacement.Write([]byte("rollback replacement")); err != nil {
			t.Fatal(err)
		}
		if err := replacement.Close(); err != nil {
			t.Fatal(err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("回滚替换"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if !treeContainsFileContents(t, config.OutputDir, "rollback replacement") {
		t.Fatal("fallback rollback deleted the replacement raced in after validation")
	}
}

func TestRunnerRollbackPreservesReplacementOfFinalGuard(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, reserveErr := output.ReserveAvailablePathAt(root, dir, base, extension)
		if reserveErr != nil {
			return nil, reserveErr
		}
		return &failingReservation{inner: reservation, releaseErr: errors.New("simulated release failure")}, nil
	}
	runner.copyOutput = func(_ context.Context, destination io.Writer, _ io.Reader) error {
		_, _ = destination.Write([]byte("partial"))
		return errors.New("simulated copy failure")
	}
	var replacementInfo os.FileInfo
	runner.beforeRollbackGuardRemove = func(parent *os.File, name string) {
		if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), name+".owned"); err != nil {
			t.Fatalf("move rollback final guard: %v", err)
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			t.Fatalf("create rollback final guard replacement: %v", err)
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("open rollback final guard replacement: %v", err)
		}
		replacement := os.NewFile(uintptr(fd), name)
		if replacement == nil {
			_ = unix.Close(fd)
			t.Fatal("wrap rollback final guard replacement")
		}
		replacementInfo, err = replacement.Stat()
		if closeErr := replacement.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("inspect rollback final guard replacement: %v", err)
		}
	}

	path, err := runner.Run(context.Background(), validRequest("回滚最终隔离"))
	if path != "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want output error", path, err)
	}
	assertRunnerCode(t, err, CodeOutput)
	if !treeContainsSameFile(t, config.OutputDir, replacementInfo) {
		t.Fatal("rollback deleted the replacement final guard inode")
	}
}

func TestRunnerMarksCompletePathPublishedWhenPublishAndReleaseReportFailure(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)
	runner.reserveOutput = func(root *os.File, dir, base, extension string) (outputReservation, error) {
		reservation, err := output.ReserveAvailablePathAt(root, dir, base, extension)
		if err != nil {
			return nil, err
		}
		return &failingReservation{
			inner:               reservation,
			releaseErr:          errors.New("simulated finalized release failure"),
			publishAfterSuccess: errors.New("simulated close warning"),
		}, nil
	}

	path, err := runner.Run(context.Background(), validRequest("发布状态"))
	if path == "" || err == nil {
		t.Fatalf("Run() = (%q, %v), want published path plus warning", path, err)
	}
	publishedPath, ok := output.PublishedPath(err)
	if !ok || publishedPath != path {
		t.Fatalf("PublishedPath() = (%q, %t), want %q, true", publishedPath, ok, path)
	}
	assertRunnerCode(t, err, CodeOutput)
	assertFileContents(t, path, "video bytes")
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerRejectsUnsafeOrAmbiguousStagedOutput(t *testing.T) {
	target := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		mode     string
		extraEnv []string
	}{
		{name: "no final file", mode: "no-output"},
		{name: "multiple final files", mode: "multiple"},
		{name: "zero byte file", mode: "zero-byte"},
		{name: "symbolic link", mode: "symlink", extraEnv: []string{"WVH_FAKE_TARGET=" + target}},
		{name: "directory", mode: "directory"},
		{name: "unsupported extension", mode: "unsupported"},
		{name: "unexpected part file", mode: "extra-part"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig(t)
			runner, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			runner.commandFactory = fakeCommandFactory(test.mode, test.extraEnv)
			path, err := runner.Run(context.Background(), validRequest("拒绝输出"))
			if path != "" || err == nil {
				t.Fatalf("Run() = (%q, %v), want output error", path, err)
			}
			assertRunnerCode(t, err, CodeOutput)
			assertNoPublishedVideo(t, config.OutputDir)
			assertNoPlatformStaging(t, config.OutputDir)
		})
	}
}

func TestRunnerNeverOverwritesExistingFileOrSymlink(t *testing.T) {
	config := testConfig(t)
	existing := filepath.Join(config.OutputDir, "冲突视频.mp4")
	if err := os.WriteFile(existing, []byte("keep existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(config.OutputDir, "target.txt")
	if err := os.WriteFile(target, []byte("keep target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.OutputDir, "冲突视频 (2).mp4")); err != nil {
		t.Fatal(err)
	}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)

	path, err := runner.Run(context.Background(), validRequest("冲突视频"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Base(path) != "冲突视频 (3).mp4" {
		t.Fatalf("published path = %q", path)
	}
	assertFileContents(t, existing, "keep existing")
	assertFileContents(t, target, "keep target")
}

func TestRunnerConcurrentSameTitleDownloadsUseUniqueNames(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)

	const workers = 8
	start := make(chan struct{})
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			path, err := runner.Run(context.Background(), validRequest("并发平台视频"))
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	close(start)
	group.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Errorf("Run() error = %v", err)
	}
	seen := make(map[string]bool)
	for path := range paths {
		if seen[path] {
			t.Errorf("duplicate published path: %q", path)
		}
		seen[path] = true
	}
	if len(seen) != workers {
		t.Fatalf("unique published files = %d, want %d", len(seen), workers)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func TestRunnerPublishesSingleVideoAndReportsMonotonicProgress(t *testing.T) {
	config := testConfig(t)
	var progress []float64
	config.OnProgress = func(update Progress) {
		progress = append(progress, update.Percent)
	}
	runner, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	runner.commandFactory = fakeCommandFactory("success", nil)

	path, err := runner.Run(context.Background(), Request{
		URL:     testYouTubeURL,
		Title:   "成功视频",
		Quality: QualityBest,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if filepath.Dir(path) != config.OutputDir || filepath.Base(path) != "成功视频.mp4" {
		t.Fatalf("published path = %q", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if string(contents) != "video bytes" {
		t.Fatalf("published contents = %q", contents)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect published file: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("published mode = %v, want regular 0600", info.Mode())
	}
	if want := []float64{4.95, 49.5, 51.975, 79.2, 99}; !reflect.DeepEqual(progress, want) {
		t.Fatalf("progress = %#v, want %#v", progress, want)
	}
	assertNoPlatformStaging(t, config.OutputDir)
}

func fakeCommandFactory(mode string, extraEnv []string) commandFactory {
	return func(_ string, args []string, env []string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestYTDLPHelperProcess$", "--"}, args...)
		command := exec.Command(os.Args[0], helperArgs...)
		command.Env = append(append([]string{}, env...), "WVH_FAKE_YTDLP=1", "WVH_FAKE_MODE="+mode)
		command.Env = append(command.Env, extraEnv...)
		return command
	}
}

func helperValueAfter(args []string, flag string) string {
	for index := 0; index < len(args)-1; index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}

func assertNoPlatformStaging(t *testing.T, outputDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(outputDir, ".web-video-platform-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("platform staging remains: %#v", matches)
	}
}

func validRequest(title string) Request {
	return Request{URL: testYouTubeURL, Title: title, Quality: QualityBest}
}

func assertRunnerCode(t *testing.T, err error, want Code) {
	t.Helper()
	var runnerError *Error
	if !errors.As(err, &runnerError) || runnerError.Code != want {
		t.Fatalf("error = %#v, want code %q", err, want)
	}
}

func assertRunnerErrorChainContainsCode(t *testing.T, err error, want Code) {
	t.Helper()
	var walk func(error) bool
	walk = func(current error) bool {
		if current == nil {
			return false
		}
		if runnerError, ok := current.(*Error); ok && runnerError.Code == want {
			return true
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if walk(child) {
					return true
				}
			}
			return false
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			return walk(wrapped.Unwrap())
		}
		return false
	}
	if !walk(err) {
		t.Fatalf("error chain %v does not contain code %q", err, want)
	}
}

func assertNoPublishedVideo(t *testing.T, outputDir string) {
	t.Helper()
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".web-video-platform-") {
			t.Fatalf("unexpected published output: %q", entry.Name())
		}
	}
}

func treeContainsSameFile(t *testing.T, root string, want os.FileInfo) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if want != nil && os.SameFile(want, info) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func treeContainsFileContents(t *testing.T, root, want string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(contents) == want {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("contents of %q = %q, want %q", path, contents, want)
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q is not empty: %#v", path, entries)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	var lastContents []byte
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			lastContents = contents
			pid, parseErr := strconv.Atoi(string(contents))
			if parseErr == nil && pid > 0 {
				return pid
			}
			if parseErr != nil {
				lastErr = parseErr
			} else {
				lastErr = errors.New("pid marker is not positive")
			}
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid marker was not valid before timeout: path %q, contents %q, error %v", path, lastContents, lastErr)
	return 0
}

func TestWaitForPIDFileRetriesIncompleteMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid.marker")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		writeDone <- os.WriteFile(path, []byte("12345"), 0o600)
	}()

	if pid := waitForPIDFile(t, path); pid != 12345 {
		t.Fatalf("waitForPIDFile() = %d, want 12345", pid)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process %d still exists after Run returned: %v", pid, err)
	}
}

func TestBuildArgsUsesExactQualitySelectors(t *testing.T) {
	runner, stagingDir := newTestRunner(t)

	tests := []struct {
		quality  Quality
		selector string
	}{
		{quality: QualityBest, selector: "bv*+ba/b"},
		{quality: Quality1080, selector: "bv*[height<=1080]+ba/b[height<=1080]"},
		{quality: Quality720, selector: "bv*[height<=720]+ba/b[height<=720]"},
	}

	for _, test := range tests {
		t.Run(string(test.quality), func(t *testing.T) {
			args, err := runner.buildArgs(Request{
				URL:     testYouTubeURL,
				Title:   "Test video",
				Quality: test.quality,
			}, stagingDir)
			if err != nil {
				t.Fatalf("buildArgs() error = %v", err)
			}
			if got := valueAfter(t, args, "--format"); got != test.selector {
				t.Fatalf("format selector = %q, want %q", got, test.selector)
			}
		})
	}
}

func TestBuildArgsRejectsMissingOrUnknownQuality(t *testing.T) {
	runner, stagingDir := newTestRunner(t)

	for _, quality := range []Quality{"", "4k", "BEST", " best "} {
		t.Run(string(quality), func(t *testing.T) {
			_, err := runner.buildArgs(Request{
				URL:     testYouTubeURL,
				Title:   "Test video",
				Quality: quality,
			}, stagingDir)
			if err == nil {
				t.Fatalf("buildArgs() accepted quality %q", quality)
			}
			if strings.Contains(err.Error(), testYouTubeURL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}
}

func TestBuildArgsUsesOnlyFixedSafeArgumentArray(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	request := Request{URL: testYouTubeURL, Title: "Test video", Quality: Quality1080}

	args, err := runner.buildArgs(request, stagingDir)
	if err != nil {
		t.Fatalf("buildArgs() error = %v", err)
	}

	want := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--max-downloads", "1",
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", "download:WVH_PROGRESS:%(info.format_id)#j\t%(progress._percent_str)s",
		"--merge-output-format", "mp4/mkv",
		"--ffmpeg-location", runner.ffmpegPath,
		"--paths", "home:" + stagingDir,
		"--output", "media.%(ext)s",
		"--format", "bv*[height<=1080]+ba/b[height<=1080]",
		request.URL,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if args[len(args)-1] != request.URL {
		t.Fatalf("final argument = %q, want canonical page URL", args[len(args)-1])
	}
}

func TestBuildArgsContainsNoCookieConfigPluginUpdateOrPlaylistExpansionFlags(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	args, err := runner.buildArgs(Request{
		URL:     testYouTubeURL,
		Title:   "Test video",
		Quality: QualityBest,
	}, stagingDir)
	if err != nil {
		t.Fatalf("buildArgs() error = %v", err)
	}

	forbidden := map[string]struct{}{
		"--cookies":              {},
		"--cookies-from-browser": {},
		"--config-locations":     {},
		"--plugin-dirs":          {},
		"--update":               {},
		"-U":                     {},
		"--yes-playlist":         {},
	}
	for _, arg := range args {
		if _, found := forbidden[arg]; found {
			t.Fatalf("unsafe argument present: %q", arg)
		}
		for flag := range forbidden {
			if strings.HasPrefix(arg, flag+"=") {
				t.Fatalf("unsafe argument present: %q", arg)
			}
		}
	}
}

func TestBuildArgsRejectsNonCanonicalURLAndUnsafeStagingDirectory(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	tests := []struct {
		name       string
		requestURL string
		stagingDir string
	}{
		{
			name:       "non canonical URL",
			requestURL: testYouTubeURL + "&list=PLsecret",
			stagingDir: stagingDir,
		},
		{
			name:       "staging directory outside output root",
			requestURL: testYouTubeURL,
			stagingDir: t.TempDir(),
		},
		{
			name:       "relative staging directory",
			requestURL: testYouTubeURL,
			stagingDir: "relative-staging",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runner.buildArgs(Request{
				URL:     test.requestURL,
				Title:   "Test video",
				Quality: QualityBest,
			}, test.stagingDir)
			if err == nil {
				t.Fatal("buildArgs() accepted unsafe input")
			}
			if strings.Contains(err.Error(), test.requestURL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}
}

func TestParseProgressAcceptsPrefixWhitespaceDecimalsAndPercentSign(t *testing.T) {
	tests := []struct {
		line string
		want float64
	}{
		{line: "WVH_PROGRESS:\"format-1\"\t42", want: 20.79},
		{line: "WVH_PROGRESS:\"format-1\"\t 42.5%", want: 21.0375},
		{line: "WVH_PROGRESS:\"format-1\"\t 7.25 % ", want: 3.58875},
		{line: "WVH_PROGRESS:\"format-1\"\t-10%", want: 0},
		{line: "WVH_PROGRESS:\"format-1\"\t100%", want: 49.5},
		{line: "WVH_PROGRESS:\"format-1\"\t999.5%", want: 49.5},
	}

	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			got, ok := parseProgressLine(test.line, progressState{})
			if !ok || !got.hasPrevious || math.Abs(got.percent-test.want) > 1e-9 {
				t.Fatalf("parseProgressLine(%q) = (%#v, %v), want percent %v", test.line, got, ok, test.want)
			}
		})
	}
}

func TestProgressParsingIsMonotonic(t *testing.T) {
	state := progressState{}
	var reported []float64
	for _, line := range []string{
		"WVH_PROGRESS:\"format-1\"\t10%",
		"WVH_PROGRESS:\"format-1\"\t8%",
		"WVH_PROGRESS:\"format-1\"\t10%",
		"WVH_PROGRESS:\"format-1\"\t63.4%",
		"WVH_PROGRESS:\"format-1\"\t101%",
		"WVH_PROGRESS:\"format-1\"\t80%",
	} {
		next, ok := parseProgressLine(line, state)
		if !ok {
			continue
		}
		state = next
		reported = append(reported, next.percent)
	}

	want := []float64{4.95, 31.383, 49.5}
	if !reflect.DeepEqual(reported, want) {
		t.Fatalf("reported progress = %#v, want %#v", reported, want)
	}
}

func TestProgressParsingMapsTwoControlledStreamsMonotonically(t *testing.T) {
	lines := []string{
		"WVH_PROGRESS:\"video-137\"\t10%",
		"WVH_PROGRESS:\"video-137\"\t100%",
		"WVH_PROGRESS:\"audio-140\"\t5%",
		"WVH_PROGRESS:\"audio-140\"\t60%",
		"WVH_PROGRESS:\"audio-140\"\t100%",
	}
	want := []float64{4.95, 49.5, 51.975, 79.2, 99}
	state := progressState{}
	var reported []float64
	for _, line := range lines {
		next, ok := parseProgressLine(line, state)
		if !ok {
			t.Fatalf("parseProgressLine(%q) was ignored", line)
		}
		state = next
		reported = append(reported, next.percent)
	}
	if !reflect.DeepEqual(reported, want) {
		t.Fatalf("two-stream progress = %#v, want %#v", reported, want)
	}
	for index, percent := range reported {
		if percent > 99 || index > 0 && percent < reported[index-1] {
			t.Fatalf("progress is not monotonic and bounded: %#v", reported)
		}
	}
}

func TestBuildArgsUsesJSONEscapedControlledStreamProgressTemplate(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	args, err := runner.buildArgs(validRequest("双流进度"), stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	want := "download:WVH_PROGRESS:%(info.format_id)#j\t%(progress._percent_str)s"
	if got := helperValueAfter(args, "--progress-template"); got != want {
		t.Fatalf("progress template = %q, want %q", got, want)
	}
}

func TestProgressParsingReportsInitialZeroOnlyOnce(t *testing.T) {
	state := progressState{}

	first, ok := parseProgressLine("WVH_PROGRESS:\"format-1\"\t0%", state)
	if !ok || !first.hasPrevious || first.percent != 0 {
		t.Fatalf("first zero progress = (%#v, %v), want reported zero", first, ok)
	}
	second, ok := parseProgressLine("WVH_PROGRESS:\"format-1\"\t0.0%", first)
	if ok || second != first {
		t.Fatalf("repeated zero progress = (%#v, %v), want unchanged and ignored", second, ok)
	}
}

func TestParseProgressIgnoresUntrustedMalformedAndNonFiniteLines(t *testing.T) {
	tests := []string{
		"",
		"  WVH_PROGRESS:42%",
		"download:WVH_PROGRESS:42%",
		"[download] 42%",
		"WVH_PROGRESS:",
		"WVH_PROGRESS:%",
		"WVH_PROGRESS:NaN%",
		"WVH_PROGRESS:+Inf%",
		"WVH_PROGRESS:-Inf%",
		"WVH_PROGRESS:12%%",
		"WVH_PROGRESS:1 2%",
		"WVH_PROGRESS:42% trailing",
		"WVH_PROGRESS:\"video\"\tNaN%",
		"WVH_PROGRESS:\"bad\\nid\"\t42%",
		"WVH_PROGRESS:\"../../../\"\t42%",
	}

	for _, line := range tests {
		t.Run(line, func(t *testing.T) {
			previous := progressState{percent: 17, hasPrevious: true}
			got, ok := parseProgressLine(line, previous)
			if ok || got != previous {
				t.Fatalf("parseProgressLine(%q) = (%#v, %v), want unchanged state", line, got, ok)
			}
		})
	}
}

func TestParseProgressEnforcesMaximumLineLengthBoundary(t *testing.T) {
	prefix := "WVH_PROGRESS:"
	stream := "\"format-1\"\t"
	value := "42%"
	atLimit := prefix + stream + strings.Repeat(" ", maxProgressLineBytes-len(prefix)-len(stream)-len(value)) + value
	overLimit := atLimit + " "

	if len(atLimit) != maxProgressLineBytes {
		t.Fatalf("test line length = %d, want %d", len(atLimit), maxProgressLineBytes)
	}
	if got, ok := parseProgressLine(atLimit, progressState{}); !ok || !got.hasPrevious || got.percent != 20.79 {
		t.Fatalf("at-limit progress = (%#v, %v), want reported 20.79", got, ok)
	}
	previous := progressState{percent: 17, hasPrevious: true}
	if got, ok := parseProgressLine(overLimit, previous); ok || got != previous {
		t.Fatalf("over-limit progress = (%#v, %v), want unchanged state", got, ok)
	}
}

func TestParseProgressRejectsNonFinitePreviousValue(t *testing.T) {
	for _, previous := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		state := progressState{percent: previous, hasPrevious: true}
		got, ok := parseProgressLine("WVH_PROGRESS:\"format-1\"\t50%", state)
		if ok || !math.IsNaN(got.percent) && !math.IsInf(got.percent, 0) {
			t.Fatalf("non-finite previous value was accepted: (%#v, %v)", got, ok)
		}
	}
}

func TestNewValidatesRequiredPathsWithoutLeakingRequestData(t *testing.T) {
	valid := testConfig(t)
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty binary", mutate: func(config *Config) { config.BinaryPath = "" }},
		{name: "relative binary", mutate: func(config *Config) { config.BinaryPath = "yt-dlp" }},
		{name: "unclean binary", mutate: func(config *Config) { config.BinaryPath += "/../yt-dlp" }},
		{name: "empty ffmpeg", mutate: func(config *Config) { config.FFmpegPath = "" }},
		{name: "relative ffmpeg", mutate: func(config *Config) { config.FFmpegPath = "ffmpeg" }},
		{name: "control in ffmpeg", mutate: func(config *Config) { config.FFmpegPath += "\nsecret" }},
		{name: "empty output", mutate: func(config *Config) { config.OutputDir = "" }},
		{name: "relative output", mutate: func(config *Config) { config.OutputDir = "downloads" }},
		{name: "output is file", mutate: func(config *Config) { config.OutputDir = config.BinaryPath }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted invalid configuration")
			} else if strings.Contains(err.Error(), testYouTubeURL) {
				t.Fatalf("error leaked unrelated request URL: %v", err)
			}
		})
	}
}

func TestRunValidatesRequestWithoutExecuting(t *testing.T) {
	runner, _ := newTestRunner(t)
	tests := []struct {
		name    string
		request Request
	}{
		{name: "empty URL", request: Request{Title: "Title", Quality: QualityBest}},
		{name: "unsupported URL", request: Request{URL: "https://example.com/video", Title: "Title", Quality: QualityBest}},
		{name: "non canonical URL", request: Request{URL: testYouTubeURL + "&feature=share", Title: "Title", Quality: QualityBest}},
		{name: "empty title", request: Request{URL: testYouTubeURL, Quality: QualityBest}},
		{name: "whitespace title", request: Request{URL: testYouTubeURL, Title: " \t ", Quality: QualityBest}},
		{name: "control in title", request: Request{URL: testYouTubeURL, Title: "Title\nsecret", Quality: QualityBest}},
		{name: "invalid UTF-8 title", request: Request{URL: testYouTubeURL, Title: string([]byte{0xff}), Quality: QualityBest}},
		{name: "oversized title", request: Request{URL: testYouTubeURL, Title: strings.Repeat("a", maxTitleBytes+1), Quality: QualityBest}},
		{name: "empty quality", request: Request{URL: testYouTubeURL, Title: "Title"}},
		{name: "unknown quality", request: Request{URL: testYouTubeURL, Title: "Title", Quality: "4k"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := runner.Run(context.Background(), test.request)
			if err == nil || path != "" {
				t.Fatalf("Run() = (%q, %v), want safe validation error", path, err)
			}
			if test.request.URL != "" && strings.Contains(err.Error(), test.request.URL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}
}

func newTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stagingDir := filepath.Join(config.OutputDir, ".wvh-test-stage")
	if err := os.Mkdir(stagingDir, 0o700); err != nil {
		t.Fatalf("create staging directory: %v", err)
	}
	return runner, stagingDir
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		BinaryPath: filepath.Join(root, "yt-dlp_macos"),
		FFmpegPath: filepath.Join(root, "ffmpeg"),
		OutputDir:  root,
	}
}

func valueAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index < len(args)-1; index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("flag %q not found in %#v", flag, args)
	return ""
}
