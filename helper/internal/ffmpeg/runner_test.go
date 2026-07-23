package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const validMediaManifest = "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nsegment.ts\n#EXT-X-ENDLIST\n"

const helperProgressRecords = 20_000

type publicResolver struct{}

func (publicResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

type fakeCommand struct {
	stdout   io.ReadCloser
	pipeErr  error
	stderr   io.Writer
	env      []string
	startErr error
	waitErr  error
	waitFunc func() error
	onStart  func() error
	onWait   func(io.Writer)
}

func (c *fakeCommand) StdoutPipe() (io.ReadCloser, error) {
	if c.pipeErr != nil {
		return nil, c.pipeErr
	}
	if c.stdout == nil {
		c.stdout = io.NopCloser(strings.NewReader("progress=end\n"))
	}
	return c.stdout, nil
}

type errorReadCloser struct {
	io.Reader
	closeErr error
}

func (r *errorReadCloser) Close() error { return r.closeErr }

func (c *fakeCommand) SetStderr(writer io.Writer) { c.stderr = writer }
func (c *fakeCommand) SetEnv(env []string)        { c.env = append([]string(nil), env...) }

func (c *fakeCommand) Start() error {
	if c.startErr != nil {
		return c.startErr
	}
	if c.onStart != nil {
		return c.onStart()
	}
	return nil
}

func (c *fakeCommand) Wait() error {
	if c.onWait != nil {
		c.onWait(c.stderr)
	}
	if c.waitFunc != nil {
		return c.waitFunc()
	}
	return c.waitErr
}

func successfulFactory(capturedName *string, capturedArgs *[]string) commandFactory {
	return func(_ context.Context, name string, args ...string) command {
		*capturedName = name
		*capturedArgs = append([]string(nil), args...)
		return &fakeCommand{onStart: func() error {
			return os.WriteFile(args[len(args)-1], []byte("video"), 0o600)
		}}
	}
}

func newTestRunner(t *testing.T, factory commandFactory, progress ProgressFunc) *Runner {
	t.Helper()
	runner, err := newRunner(internalConfig{
		outputDir:      t.TempDir(),
		resolver:       publicResolver{},
		commandFactory: factory,
		onProgress:     progress,
		ffmpegPath:     "ffmpeg-test",
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	return runner
}

func TestRunnerBuildsExplicitSafeArguments(t *testing.T) {
	var name string
	var args []string
	runner := newTestRunner(t, successfulFactory(&name, &args), nil)
	source := "https://cdn.example/video.m3u8?token=a;$(touch%20owned)&quote='x'"

	path, err := runner.Run(context.Background(), Request{
		SourceURL: source,
		Title:     "标题;$(touch owned)",
		Manifest:  []byte(validMediaManifest),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if name != "ffmpeg-test" {
		t.Fatalf("command name = %q", name)
	}
	want := []string{
		"-protocol_whitelist", "http,https,tcp,tls",
		"-nostdin", "-y", "-i", source,
		"-map", "0", "-c", "copy", "-movflags", "+faststart",
		"-progress", "pipe:1", "-nostats", args[len(args)-1],
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("command args = %#v, want %#v", args, want)
	}
	if args[5] != source {
		t.Fatalf("source URL was split or changed: %q", args[5])
	}
	for _, forbiddenProtocol := range []string{"crypto", "file", "data"} {
		if strings.Contains(args[1], forbiddenProtocol) {
			t.Fatalf("protocol whitelist contains forbidden protocol %q: %q", forbiddenProtocol, args[1])
		}
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"-headers", "-cookies", "Cookie:", "Authorization:"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("command args contain forbidden authentication option %q: %#v", forbidden, args)
		}
	}
	if strings.Contains(args[len(args)-1], "touch") || strings.Contains(args[len(args)-1], "标题") {
		t.Fatalf("private staging path contains the untrusted title: %q", args[len(args)-1])
	}
	if filepath.Base(path) != "标题;$(touch owned).mp4" {
		t.Fatalf("published file = %q", filepath.Base(path))
	}
}

func TestRunnerRemovesProxyEnvironmentFromChild(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8888")
	t.Setenv("https_proxy", "http://127.0.0.1:9999")
	t.Setenv("FFREPORT", "file=/tmp/ffmpeg-report.log:level=48")
	var child *fakeCommand
	runner := newTestRunner(t, func(_ context.Context, _ string, args ...string) command {
		child = &fakeCommand{onStart: func() error {
			return os.WriteFile(args[len(args)-1], []byte("video"), 0o600)
		}}
		return child
	}, nil)
	if _, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)}); err != nil {
		t.Fatal(err)
	}
	if child.env == nil {
		t.Fatal("child environment was not explicitly sanitized")
	}
	for _, entry := range child.env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "ffreport":
			t.Fatalf("sensitive environment variable was inherited by FFmpeg: %q", entry)
		}
	}
}

func TestRunnerRejectsEncryptedManifestBeforeCommand(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "media key", manifest: "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:4,\nsegment.ts\n"},
		{name: "session key", manifest: "#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var invoked atomic.Bool
			runner := newTestRunner(t, func(context.Context, string, ...string) command {
				invoked.Store(true)
				return &fakeCommand{}
			}, nil)
			_, err := runner.Run(context.Background(), Request{
				SourceURL: "https://cdn.example/video.m3u8",
				Title:     "video",
				Manifest:  []byte(tt.manifest),
			})
			assertCode(t, err, CodeEncrypted)
			if invoked.Load() {
				t.Fatal("command factory was invoked for an encrypted playlist")
			}
		})
	}
}

func TestRunnerAllowsEncryptionMethodNone(t *testing.T) {
	var invoked atomic.Bool
	runner := newTestRunner(t, func(_ context.Context, _ string, args ...string) command {
		invoked.Store(true)
		return &fakeCommand{onStart: func() error {
			return os.WriteFile(args[len(args)-1], []byte("video"), 0o600)
		}}
	}, nil)
	manifest := "#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:4,\nsegment.ts\n"
	if _, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(manifest)}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !invoked.Load() {
		t.Fatal("command was not invoked for METHOD=NONE")
	}
}

func TestRunnerRequiresPreflightManifest(t *testing.T) {
	var invoked atomic.Bool
	runner := newTestRunner(t, func(context.Context, string, ...string) command {
		invoked.Store(true)
		return &fakeCommand{}
	}, nil)
	_, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video"})
	assertCode(t, err, CodeManifest)
	if invoked.Load() {
		t.Fatal("command was invoked without mandatory preflight bytes")
	}
}

func TestRunnerValidatesPublicSourceBeforeCommand(t *testing.T) {
	var invoked atomic.Bool
	runner := newTestRunner(t, func(context.Context, string, ...string) command {
		invoked.Store(true)
		return &fakeCommand{}
	}, nil)
	_, err := runner.Run(context.Background(), Request{
		SourceURL: "http://127.0.0.1/private.m3u8",
		Title:     "video",
		Manifest:  []byte(validMediaManifest),
	})
	assertCode(t, err, CodeUnsafeSource)
	if invoked.Load() {
		t.Fatal("command was invoked for a private source URL")
	}
}

func TestRunnerMapsMissingFFmpegToChineseError(t *testing.T) {
	runner, err := newRunner(internalConfig{
		outputDir:  t.TempDir(),
		resolver:   publicResolver{},
		ffmpegPath: filepath.Join(t.TempDir(), "missing-ffmpeg"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	assertCode(t, err, CodeFFmpegMissing)
	if err == nil || err.Error() != "未安装 FFmpeg" {
		t.Fatalf("Run() error = %v, want 未安装 FFmpeg", err)
	}
	assertNoStagingFiles(t, runner.outputDir)
}

func TestRunnerCancellationKillsProcessAndCleansStaging(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "blocking-ffmpeg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := newRunner(internalConfig{outputDir: dir, resolver: publicResolver{}, ffmpegPath: script})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = runner.Run(ctx, Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	assertCode(t, err, CodeCanceled)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("canceled process returned after %v, child may not have been terminated", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline cause", err)
	}
	assertNoStagingFiles(t, dir)
}

func TestRunnerBoundsAndRedactsStderrOnProcessFailure(t *testing.T) {
	dir := t.TempDir()
	source := "https://cdn.example/video.m3u8?signature=very-secret-value"
	runner, err := newRunner(internalConfig{
		outputDir:  dir,
		resolver:   publicResolver{},
		ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{
				onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("partial"), 0o600) },
				onWait: func(writer io.Writer) {
					_, _ = io.WriteString(writer, source+"\n"+strings.Repeat("x", maxStderrBytes*2)+"TAIL")
				},
				waitErr: errors.New("exit status 1"),
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{SourceURL: source, Title: "video", Manifest: []byte(validMediaManifest)})
	assertCode(t, err, CodeProcess)
	var runErr *Error
	if !errors.As(err, &runErr) {
		t.Fatalf("Run() error type = %T", err)
	}
	if len(runErr.Stderr) > maxStderrBytes {
		t.Fatalf("stderr bytes = %d, want <= %d", len(runErr.Stderr), maxStderrBytes)
	}
	if strings.Contains(runErr.Stderr, source) || strings.Contains(err.Error(), source) {
		t.Fatal("error exposed the full signed source URL")
	}
	if !strings.HasSuffix(runErr.Stderr, "TAIL") {
		t.Fatalf("stderr did not retain the tail: %q", runErr.Stderr)
	}
	assertNoStagingFiles(t, dir)
}

func TestRunnerRedactsSignedChildAndSegmentURLsFromStderr(t *testing.T) {
	dir := t.TempDir()
	source := "https://cdn.example/top.m3u8?signature=top-secret"
	child := "https://child.example/quality.m3u8?token=child-secret&expires=999"
	segment := "HTTPS://media.example/segment-001.ts?signature=segment-secret"
	key := "http://keys.example/key.bin?auth=key-secret"
	runner, err := newRunner(internalConfig{
		outputDir:  dir,
		resolver:   publicResolver{},
		ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{
				onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("partial"), 0o600) },
				onWait: func(writer io.Writer) {
					_, _ = io.WriteString(writer, "opening 'ht")
					_, _ = io.WriteString(writer, strings.TrimPrefix(child, "ht")+"' for reading\n")
					_, _ = io.WriteString(writer, "segment failed: "+segment+" retrying\n")
					_, _ = io.WriteString(writer, "key request: "+key+" denied\n")
				},
				waitErr: errors.New("exit status 1"),
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{SourceURL: source, Title: "video", Manifest: []byte(validMediaManifest)})
	assertCode(t, err, CodeProcess)
	var runErr *Error
	if !errors.As(err, &runErr) {
		t.Fatalf("Run() error type = %T", err)
	}
	for _, leaked := range []string{child, segment, key, "child-secret", "segment-secret", "key-secret"} {
		if strings.Contains(runErr.Stderr, leaked) {
			t.Errorf("stderr leaked %q: %q", leaked, runErr.Stderr)
		}
	}
	lowerStderr := strings.ToLower(runErr.Stderr)
	for _, scheme := range []string{"http://", "https://"} {
		if strings.Contains(lowerStderr, scheme) {
			t.Errorf("stderr leaked URL scheme %q: %q", scheme, runErr.Stderr)
		}
	}
	for _, contextText := range []string{"opening", "for reading", "segment failed", "retrying", "key request", "denied"} {
		if !strings.Contains(runErr.Stderr, contextText) {
			t.Errorf("stderr lost non-sensitive context %q: %q", contextText, runErr.Stderr)
		}
	}
	if len(runErr.Stderr) > maxStderrBytes {
		t.Fatalf("stderr bytes = %d, want <= %d", len(runErr.Stderr), maxStderrBytes)
	}
}

func TestRunnerRedactsIPv6AndParenthesizedURLsFromStderr(t *testing.T) {
	dir := t.TempDir()
	ipv6URL := "https://[2001:db8::1]/segment.ts?token=ipv6-secret"
	pathURL := "https://media.example/video_(draft).ts?token=path-secret"
	queryURL := "https://media.example/segment.ts?token=abc(def)ghi&signature=query-secret"
	runner, err := newRunner(internalConfig{
		outputDir:  dir,
		resolver:   publicResolver{},
		ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{
				onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("partial"), 0o600) },
				onWait: func(writer io.Writer) {
					_, _ = io.WriteString(writer, "IPv6 child: HT")
					_, _ = io.WriteString(writer, strings.TrimPrefix(ipv6URL, "ht"))
					_, _ = io.WriteString(writer, " rejected\npath child: "+pathURL+" rejected\n")
					_, _ = io.WriteString(writer, "query child: "+queryURL+" rejected\n")
				},
				waitErr: errors.New("exit status 1"),
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/top.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	assertCode(t, err, CodeProcess)
	var runErr *Error
	if !errors.As(err, &runErr) {
		t.Fatalf("Run() error type = %T", err)
	}
	for _, leaked := range []string{
		ipv6URL, pathURL, queryURL,
		"2001:db8::1", "ipv6-secret", "path-secret", "query-secret", "abc(def)ghi",
	} {
		if strings.Contains(runErr.Stderr, leaked) {
			t.Errorf("stderr leaked %q: %q", leaked, runErr.Stderr)
		}
	}
	for _, contextText := range []string{"IPv6 child", "path child", "query child", "rejected"} {
		if !strings.Contains(runErr.Stderr, contextText) {
			t.Errorf("stderr lost non-sensitive context %q: %q", contextText, runErr.Stderr)
		}
	}
}

func TestRunnerPublishesOnlyAfterSuccessfulWait(t *testing.T) {
	dir := t.TempDir()
	var args []string
	runner, err := newRunner(internalConfig{
		outputDir:  dir,
		resolver:   publicResolver{},
		ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, gotArgs ...string) command {
			args = append([]string(nil), gotArgs...)
			return &fakeCommand{
				onStart: func() error { return os.WriteFile(gotArgs[len(gotArgs)-1], []byte("complete"), 0o600) },
				onWait: func(io.Writer) {
					if _, statErr := os.Stat(filepath.Join(dir, "video.mp4")); !errors.Is(statErr, os.ErrNotExist) {
						t.Errorf("final output existed before Wait completed: %v", statErr)
					}
				},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "complete" {
		t.Fatalf("published contents = %q, error = %v", contents, err)
	}
	if _, err := os.Stat(args[len(args)-1]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private part remains after publication: %v", err)
	}
	assertNoStagingFiles(t, dir)
}

func TestRunnerDoesNotOverwriteExistingFileOrSymlink(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "video (2).mp4")); err != nil {
		t.Fatal(err)
	}
	runner, err := newRunner(internalConfig{
		outputDir: dir, resolver: publicResolver{}, ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("new"), 0o600) }}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "video (3).mp4" {
		t.Fatalf("published path = %q", path)
	}
	for path, want := range map[string]string{existing: "keep", target: "target"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("protected file %q = %q, error = %v", path, got, readErr)
		}
	}
}

func TestRunnerAtomicallyChoosesConcurrentNames(t *testing.T) {
	dir := t.TempDir()
	const workers = 12
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			runner, err := newRunner(internalConfig{
				outputDir: dir, resolver: publicResolver{}, ffmpegPath: "ffmpeg-test",
				commandFactory: func(_ context.Context, _ string, args ...string) command {
					return &fakeCommand{onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("video"), 0o600) }}
				},
			})
			if err != nil {
				errs <- err
				return
			}
			path, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "same", Manifest: []byte(validMediaManifest)})
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	group.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Errorf("Run() error = %v", err)
	}
	seen := make(map[string]bool)
	for path := range paths {
		if seen[path] {
			t.Errorf("duplicate output path %q", path)
		}
		seen[path] = true
	}
	if len(seen) != workers {
		t.Fatalf("unique outputs = %d, want %d", len(seen), workers)
	}
}

func TestParseProgressRecords(t *testing.T) {
	input := strings.Join([]string{
		"out_time_us=1500000",
		"out_time_ms=999",
		"total_size=4096",
		"speed=1.25x",
		"unknown=value",
		"progress=continue",
		"out_time_ms=2250000",
		"total_size=8192",
		"speed=2.0x",
		"progress=end",
		"",
	}, "\r\n")
	var got []Progress
	if err := parseProgress(strings.NewReader(input), func(progress Progress) { got = append(got, progress) }); err != nil {
		t.Fatalf("parseProgress() error = %v", err)
	}
	want := []Progress{
		{OutTime: 1500 * time.Millisecond, TotalSize: 4096, Speed: "1.25x"},
		{OutTime: 2250 * time.Millisecond, TotalSize: 8192, Speed: "2.0x", Done: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestParseProgressToleratesMalformedValuesAndIncompleteRecords(t *testing.T) {
	input := "out_time_us=bad\ntotal_size=nope\nspeed=fast\nprogress=continue\n" +
		"out_time_us=10\ntotal_size=20\n"
	var got []Progress
	if err := parseProgress(strings.NewReader(input), func(progress Progress) { got = append(got, progress) }); err != nil {
		t.Fatalf("parseProgress() error = %v", err)
	}
	want := []Progress{{Speed: "fast"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestParseProgressIgnoresOverflowingTimestamp(t *testing.T) {
	input := "out_time_us=9223372036854775807\nprogress=continue\n"
	var got []Progress
	if err := parseProgress(strings.NewReader(input), func(progress Progress) { got = append(got, progress) }); err != nil {
		t.Fatalf("parseProgress() error = %v", err)
	}
	want := []Progress{{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestRunnerReportsParsedProgress(t *testing.T) {
	var got []Progress
	var mu sync.Mutex
	runner := newTestRunner(t, func(_ context.Context, _ string, args ...string) command {
		return &fakeCommand{
			stdout:  io.NopCloser(strings.NewReader("out_time_us=1000000\ntotal_size=9\nprogress=continue\nprogress=end\n")),
			onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("video"), 0o600) },
		}
	}, func(progress Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, progress)
	})
	if _, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []Progress{{OutTime: time.Second, TotalSize: 9}, {Done: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestRunnerSerializesCallbacksAcrossConcurrentRuns(t *testing.T) {
	dir := t.TempDir()
	var active atomic.Int32
	var overlapped atomic.Bool
	runner, err := newRunner(internalConfig{
		outputDir: dir, resolver: publicResolver{}, ffmpegPath: "ffmpeg-test",
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{
				stdout:  io.NopCloser(strings.NewReader("progress=continue\nprogress=end\n")),
				onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("video"), 0o600) },
			}
		},
		onProgress: func(Progress) {
			if active.Add(1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
			errCh <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if overlapped.Load() {
		t.Fatal("one Runner invoked its progress callback concurrently")
	}
}

func TestRunnerClassifiesPipeStartAndCloseFailures(t *testing.T) {
	tests := []struct {
		name string
		make func(args []string) command
		want Code
	}{
		{
			name: "stdout pipe",
			make: func([]string) command { return &fakeCommand{pipeErr: errors.New("pipe failed")} },
			want: CodeProcess,
		},
		{
			name: "start",
			make: func([]string) command { return &fakeCommand{startErr: errors.New("start failed")} },
			want: CodeProcess,
		},
		{
			name: "stdout close",
			make: func(args []string) command {
				return &fakeCommand{
					stdout:  &errorReadCloser{Reader: strings.NewReader("progress=end\n"), closeErr: errors.New("close failed")},
					onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("video"), 0o600) },
				}
			},
			want: CodeProgress,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newTestRunner(t, func(_ context.Context, _ string, args ...string) command { return tt.make(args) }, nil)
			_, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
			assertCode(t, err, tt.want)
			assertNoStagingFiles(t, runner.outputDir)
		})
	}
}

func TestRunnerClosesProgressPipeOnOversizedToken(t *testing.T) {
	reader, writer := io.Pipe()
	writerStarted := make(chan struct{})
	writerDone := make(chan struct{})
	runner := newTestRunner(t, func(_ context.Context, _ string, _ ...string) command {
		return &fakeCommand{
			stdout: reader,
			onStart: func() error {
				go func() {
					defer close(writerDone)
					defer writer.Close()
					close(writerStarted)
					_, _ = io.WriteString(writer, strings.Repeat("x", maxProgressTokenSize*2))
				}()
				return nil
			},
			waitFunc: func() error {
				<-writerDone
				return errors.New("child exited after progress pipe closed")
			},
		}
	}, nil)

	type result struct {
		path string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		path, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
		resultCh <- result{path: path, err: err}
	}()
	<-writerStarted

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case got := <-resultCh:
		assertCode(t, got.err, CodeProgress)
		if got.path != "" {
			t.Fatalf("Run() path = %q after progress failure", got.path)
		}
		select {
		case <-writerDone:
		default:
			t.Fatal("Run() returned before the child writer was released")
		}
		assertNoStagingFiles(t, runner.outputDir)
	case <-timer.C:
		_ = reader.CloseWithError(errors.New("test timeout cleanup"))
		_ = writer.Close()
		select {
		case <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("Run() remained blocked after forced pipe cleanup")
		}
		t.Fatal("Run() blocked because an oversized progress token did not close or drain the pipe")
	}
}

func TestRunnerReadsAllProgressBeforeWaitingForRealProcess(t *testing.T) {
	t.Setenv("GO_WANT_FFMPEG_PROGRESS_HELPER", "1")
	t.Setenv("FFMPEG_PROGRESS_HELPER_BINARY", os.Args[0])
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ffmpeg-progress-helper")
	script := "#!/bin/sh\nexec \"$FFMPEG_PROGRESS_HELPER_BINARY\" -test.run '^TestFFmpegProgressHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	var records atomic.Int64
	var done atomic.Bool
	runner, err := newRunner(internalConfig{
		outputDir:  dir,
		resolver:   publicResolver{},
		ffmpegPath: wrapper,
		onProgress: func(progress Progress) {
			records.Add(1)
			if progress.Done {
				done.Store(true)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := records.Load(), int64(helperProgressRecords+1); got != want {
		t.Fatalf("progress records = %d, want %d", got, want)
	}
	if !done.Load() {
		t.Fatal("final progress=end record was not read")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "video" {
		t.Fatalf("published contents = %q, error = %v", contents, err)
	}
}

func TestRunnerFinishesProgressParserBeforeWait(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	events := make(chan string, 2)
	runner, err := newRunner(internalConfig{
		outputDir:  t.TempDir(),
		resolver:   publicResolver{},
		ffmpegPath: "ffmpeg-test",
		progressParser: func(io.Reader, ProgressFunc) error {
			events <- "parse"
			return nil
		},
		commandFactory: func(_ context.Context, _ string, args ...string) command {
			return &fakeCommand{
				onStart: func() error { return os.WriteFile(args[len(args)-1], []byte("video"), 0o600) },
				waitFunc: func() error {
					events <- "wait"
					return nil
				},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{SourceURL: "https://cdn.example/video.m3u8", Title: "video", Manifest: []byte(validMediaManifest)}); err != nil {
		t.Fatal(err)
	}
	first, second := <-events, <-events
	if first != "parse" || second != "wait" {
		t.Fatalf("runner events = [%s %s], want [parse wait]", first, second)
	}
}

func TestFFmpegProgressHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FFMPEG_PROGRESS_HELPER") != "1" {
		return
	}
	partPath := os.Args[len(os.Args)-1]
	if err := os.WriteFile(partPath, []byte("video"), 0o600); err != nil {
		os.Exit(2)
	}
	for index := range helperProgressRecords {
		_, _ = fmt.Fprintf(os.Stdout, "out_time_us=%d\ntotal_size=%d\nprogress=continue\n", index, index)
	}
	_, _ = fmt.Fprintln(os.Stdout, "progress=end")
	os.Exit(0)
}

func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	var runErr *Error
	if !errors.As(err, &runErr) {
		t.Fatalf("error = %v (%T), want *Error", err, err)
	}
	if runErr.Code != want {
		t.Fatalf("error code = %q, want %q (error: %v)", runErr.Code, want, err)
	}
}

func assertNoStagingFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".web-video-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging entries remain: %v", matches)
	}
}
