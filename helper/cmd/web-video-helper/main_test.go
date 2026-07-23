package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "web-video-downloader/helper/internal/config"
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

func TestPrintTokenUsesConfigOverrideAndDoesNotStartServer(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "portable", "settings.json")
	var stdout, stderr bytes.Buffer
	serveCalls := 0
	deps := defaultAppDeps()
	deps.serve = func(context.Context, appconfig.Config, string, string) error { serveCalls++; return nil }

	exitCode := runContext(context.Background(), []string{"--config", configPath, "--print-token"}, &stdout, &stderr, deps)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr=%s", exitCode, stderr.String())
	}
	if serveCalls != 0 {
		t.Fatalf("server started %d times", serveCalls)
	}
	cfg, err := appconfig.Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stdout.String() != cfg.Token+"\n" {
		t.Fatalf("stdout = %q, want only token", stdout.String())
	}
	if strings.Contains(stderr.String(), cfg.Token) {
		t.Fatal("stderr leaked token")
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Application Support", "网页视频下载器", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default config was touched: %v", err)
	}
}

func TestNormalStartLogsSafeStateAndRedactsToken(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "config.json")
	cfg, err := appconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	var gotConfig appconfig.Config
	gotFFmpeg := ""
	deps := defaultAppDeps()
	deps.lookPath = func(name string) (string, error) {
		if name != "ffmpeg" {
			t.Fatalf("LookPath(%q)", name)
		}
		return "/usr/local/bin/ffmpeg", nil
	}
	deps.serve = func(_ context.Context, got appconfig.Config, _ string, ffmpegPath string) error {
		gotConfig, gotFFmpeg = got, ffmpegPath
		return nil
	}
	exitCode := runContext(context.Background(), []string{"--config", configPath}, &stdout, &stderr, deps)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr=%s", exitCode, stderr.String())
	}
	if gotConfig != cfg || gotFFmpeg != "/usr/local/bin/ffmpeg" {
		t.Fatalf("serve config=%#v ffmpeg=%q", gotConfig, gotFFmpeg)
	}
	logText := stdout.String() + stderr.String()
	if !strings.Contains(logText, cfg.Address) || !strings.Contains(logText, cfg.DownloadDir) || !strings.Contains(logText, "FFmpeg: 可用") {
		t.Fatalf("startup log = %q", logText)
	}
	if strings.Contains(logText, cfg.Token) {
		t.Fatal("normal startup leaked token")
	}
}

func TestNormalStartContinuesWhenFFmpegMissing(t *testing.T) {
	home := privateTempDir(t)
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "config.json")
	var stdout, stderr bytes.Buffer
	deps := defaultAppDeps()
	deps.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	called := false
	deps.serve = func(_ context.Context, _ appconfig.Config, _ string, ffmpegPath string) error {
		called = true
		if ffmpegPath != "" {
			t.Fatalf("ffmpeg path=%q", ffmpegPath)
		}
		return nil
	}
	if exit := runContext(context.Background(), []string{"--config", configPath}, &stdout, &stderr, deps); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if !called || !strings.Contains(stdout.String(), "FFmpeg: 未安装") {
		t.Fatalf("called=%v stdout=%q", called, stdout.String())
	}
}

func TestRunRejectsBadArgumentsAndBadConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{name: "unknown flag", args: []string{"--unknown"}, want: 2},
		{name: "extra arg", args: []string{"extra"}, want: 2},
		{name: "missing config value", args: []string{"--config"}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := runContext(context.Background(), tc.args, &stdout, &stderr, defaultAppDeps()); got != tc.want {
				t.Fatalf("exit=%d stderr=%s", got, stderr.String())
			}
		})
	}

	home := privateTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "bad.json")
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := runContext(context.Background(), []string{"--config", path}, &stdout, &stderr, defaultAppDeps()); got != 1 {
		t.Fatalf("bad config exit=%d", got)
	}
	if !strings.Contains(stderr.String(), "无法加载配置") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestServeHelperGracefullyClosesListenerOnCancellation(t *testing.T) {
	dir := t.TempDir()
	cfg := appconfig.Config{Address: appconfig.DefaultAddress, Token: "test-token", DownloadDir: dir}
	listener := newBlockingListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveHelper(ctx, cfg, "test", "", func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != appconfig.DefaultAddress {
				t.Errorf("listen(%q,%q)", network, address)
			}
			return listener, nil
		})
	}()
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not begin serving")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveHelper() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open")
	}
}

type blockingListener struct {
	accepted chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{accepted: make(chan struct{}), closed: make(chan struct{})}
}
func (l *blockingListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.accepted) })
	<-l.closed
	return nil, net.ErrClosed
}
func (l *blockingListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *blockingListener) Addr() net.Addr { return testAddr(appconfig.DefaultAddress) }
func (l *blockingListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod private temp dir: %v", err)
	}
	return dir
}
