package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"web-video-harbor/helper/internal/download"
	"web-video-harbor/helper/internal/ffmpeg"
	"web-video-harbor/helper/internal/hls"
	"web-video-harbor/helper/internal/output"
	"web-video-harbor/helper/internal/tasks"
	"web-video-harbor/helper/internal/ytdlp"
)

type directDownloaderFunc func(context.Context, string, string) (string, error)

func (f directDownloaderFunc) Download(ctx context.Context, rawURL, title string) (string, error) {
	return f(ctx, rawURL, title)
}

type hlsRunnerFunc func(context.Context, ffmpeg.Request) (string, error)

func (f hlsRunnerFunc) Run(ctx context.Context, request ffmpeg.Request) (string, error) {
	return f(ctx, request)
}

type manifestInspectorFunc func(context.Context, string) (ManifestInspection, error)

func (f manifestInspectorFunc) Inspect(ctx context.Context, rawURL string) (ManifestInspection, error) {
	return f(ctx, rawURL)
}

type platformRunnerFunc func(context.Context, ytdlp.Request) (string, error)

func (f platformRunnerFunc) Run(ctx context.Context, request ytdlp.Request) (string, error) {
	return f(ctx, request)
}

type publishedTestError struct {
	path string
	err  error
}

func (e *publishedTestError) Error() string         { return e.err.Error() }
func (e *publishedTestError) Unwrap() error         { return e.err }
func (e *publishedTestError) PublishedPath() string { return e.path }

func TestEngineStartsMP4AsynchronouslyAndReportsProgress(t *testing.T) {
	manager := tasks.NewManager()
	started := make(chan struct{})
	release := make(chan struct{})

	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(progress download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(ctx context.Context, rawURL, title string) (string, error) {
				close(started)
				progress(download.Progress{DownloadedBytes: 5, TotalBytes: 10})
				select {
				case <-release:
					return "/downloads/video.mp4", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://media.example/video.mp4", Title: "video", MediaType: "mp4",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if task.Status != tasks.Queued {
		t.Fatalf("Start status = %q, want %q", task.Status, tasks.Queued)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("downloader did not start asynchronously")
	}
	wantStatus(t, manager, task.ID, tasks.Downloading)
	wantProgress(t, manager, task.ID, 50)

	close(release)
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/video.mp4" {
		t.Fatalf("output path = %q", completed.OutputPath)
	}
}

func TestEngineStartPlatformCanonicalizesBeforeCreatingTask(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{manager: manager})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	task, err := engine.Start(context.Background(), JobSpec{
		URL:       "https://www.youtube.com/watch?v=_mVb1D8wHxg&utm_source=test",
		Title:     "video",
		MediaType: "platform",
		Quality:   "1080",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if task.URL != "https://www.youtube.com/watch?v=_mVb1D8wHxg" {
		t.Fatalf("task URL = %q", task.URL)
	}
}

func TestEngineStartPlatformRejectsUnsupportedURLBeforeCreatingTask(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{manager: manager})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	if _, err := engine.Start(context.Background(), JobSpec{
		URL:       "https://youtube.example/watch?v=_mVb1D8wHxg",
		MediaType: "platform",
		Quality:   "best",
	}); err == nil {
		t.Fatal("Start accepted an unsupported platform URL")
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("invalid platform URL created tasks: %#v", got)
	}
}

func TestEngineStartPlatformRejectsUnknownQualityBeforeCreatingTask(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{manager: manager})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	if _, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "4k",
	}); err == nil {
		t.Fatal("Start accepted an unknown platform quality")
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("invalid platform quality created tasks: %#v", got)
	}
}

func TestEnginePlatformDownloaderMissingRejectsSynchronously(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{manager: manager, platformUnavailable: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err == nil {
		t.Fatal("Start accepted a platform task without the bundled downloader")
	}
	var safe interface{ SafeMessage() string }
	if !errors.As(err, &safe) || safe.SafeMessage() != "安装包缺少平台解析器" {
		t.Fatalf("Start error = %v, safe = %#v", err, safe)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("missing platform downloader created tasks: %#v", got)
	}
}

func TestEnginePlatformFFmpegMissingRejectsSynchronously(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := NewEngine(manager, t.TempDir(), nil, "", ytdlp.ProbeResult{Path: "/Applications/WebVideoHarbor/yt-dlp_macos"}, ytdlp.RuntimeResult{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err == nil {
		t.Fatal("Start accepted a platform task without FFmpeg")
	}
	var safe interface{ SafeMessage() string }
	if !errors.As(err, &safe) || safe.SafeMessage() != "未安装 FFmpeg，请先安装后重试" {
		t.Fatalf("Start error = %v, safe = %#v", err, safe)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("missing FFmpeg created tasks: %#v", got)
	}
}

func TestEnginePlatformRuntimeMissingRejectsSynchronously(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{manager: manager, platformRuntimeUnavailable: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err == nil {
		t.Fatal("Start accepted a platform task without the JavaScript runtime")
	}
	var safe interface{ SafeMessage() string }
	if !errors.As(err, &safe) || safe.SafeMessage() != "安装包缺少 JavaScript 解析组件" {
		t.Fatalf("Start error = %v, safe = %#v", err, safe)
	}
}

func TestNewEngineRejectsPathOnlyPlatformIdentity(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := NewEngine(manager, t.TempDir(), nil, "/usr/local/bin/ffmpeg", ytdlp.ProbeResult{
		Path:    "/Applications/WebVideoHarbor/yt-dlp_macos",
		Version: "2026.07.04",
	}, ytdlp.RuntimeResult{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	var unavailable *PlatformDownloaderUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Start error = %v, want fail-closed platform identity error", err)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("path-only platform identity created tasks: %#v", got)
	}
}

func TestEnginePlatformPassesCanonicalRequestToFreshRunnerAndCompletes(t *testing.T) {
	manager := tasks.NewManager()
	requests := make(chan ytdlp.Request, 1)
	factoryCalls := 0
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			factoryCalls++
			return platformRunnerFunc(func(_ context.Context, request ytdlp.Request) (string, error) {
				requests <- request
				return "/downloads/platform.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	task, err := engine.Start(context.Background(), JobSpec{
		URL:       "https://www.bilibili.com/video/BV1K3Gz6pEoo/?spm_id_from=333.1007",
		Title:     "Bilibili title",
		MediaType: "platform",
		Quality:   "720",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/platform.mp4" {
		t.Fatalf("output path = %q", completed.OutputPath)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	wantRequest := ytdlp.Request{
		URL:     "https://www.bilibili.com/video/BV1K3Gz6pEoo",
		Title:   "Bilibili title",
		Quality: ytdlp.Quality720,
	}
	if got := <-requests; got != wantRequest {
		t.Fatalf("request = %#v, want %#v", got, wantRequest)
	}
}

func TestEnginePlatformNilRunnerFailsSafely(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, manager, task.ID, tasks.Failed)
	if failed.ErrorCode != "download_failed" || failed.Error != "视频下载失败，请稍后重试" {
		t.Fatalf("failed task = %#v", failed)
	}
}

func TestEnginePlatformFactoryErrorFailsSafelyWithoutRunning(t *testing.T) {
	manager := tasks.NewManager()
	runCalled := false
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				runCalled = true
				return "/downloads/should-not-run.mp4", nil
			}), errors.New("factory includes a secret path")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, manager, task.ID, tasks.Failed)
	if failed.ErrorCode != "download_failed" || failed.Error != "视频下载失败，请稍后重试" {
		t.Fatalf("failed task = %#v", failed)
	}
	if runCalled {
		t.Fatal("runner was called despite factory error")
	}
}

func TestEnginePlatformEmptySuccessPathFailsSafely(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				return "", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, manager, task.ID, tasks.Failed)
	if failed.ErrorCode != "download_failed" || failed.Error != "视频下载失败，请稍后重试" {
		t.Fatalf("failed task = %#v", failed)
	}
}

func TestEnginePlatformProgressIsMonotonicAndEntersMergingAt99BeforeCompletion(t *testing.T) {
	manager := tasks.NewManager()
	progressReady := make(chan struct{})
	release := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(progress ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				progress(ytdlp.Progress{Percent: 40})
				progress(ytdlp.Progress{Percent: 20})
				progress(ytdlp.Progress{Percent: 100})
				close(progressReady)
				<-release
				return "/downloads/platform.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-progressReady:
	case <-time.After(time.Second):
		t.Fatal("platform runner did not report progress")
	}
	wantProgress(t, manager, task.ID, 99)
	active, err := manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != tasks.Merging {
		t.Fatalf("status before output = %q, want %q", active.Status, tasks.Merging)
	}
	close(release)
	waitStatus(t, manager, task.ID, tasks.Completed)
	wantProgress(t, manager, task.ID, 100)
}

func TestEnginePlatformProgressRejectsNonFiniteValues(t *testing.T) {
	manager := tasks.NewManager()
	progressReady := make(chan struct{})
	release := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(progress ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				progress(ytdlp.Progress{Percent: 25})
				progress(ytdlp.Progress{Percent: math.Inf(1)})
				progress(ytdlp.Progress{Percent: math.Inf(-1)})
				progress(ytdlp.Progress{Percent: math.NaN()})
				close(progressReady)
				<-release
				return "/downloads/platform.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-progressReady:
	case <-time.After(time.Second):
		t.Fatal("platform runner did not report progress")
	}
	wantProgress(t, manager, task.ID, 25)
	close(release)
	waitStatus(t, manager, task.ID, tasks.Completed)
}

func TestEnginePlatformProgressHandlesConcurrentOutOfOrderCallbacks(t *testing.T) {
	manager := tasks.NewManager()
	progressReady := make(chan struct{})
	release := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(progress ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				start := make(chan struct{})
				var callbacks sync.WaitGroup
				for _, percent := range []float64{80, 10, 60, 99, 40} {
					callbacks.Add(1)
					go func(value float64) {
						defer callbacks.Done()
						<-start
						progress(ytdlp.Progress{Percent: value})
					}(percent)
				}
				close(start)
				callbacks.Wait()
				close(progressReady)
				<-release
				return "/downloads/platform.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-progressReady:
	case <-time.After(time.Second):
		t.Fatal("concurrent platform progress callbacks did not finish")
	}
	wantProgress(t, manager, task.ID, 99)
	close(release)
	waitStatus(t, manager, task.ID, tasks.Completed)
}

func TestEnginePlatformCancelSignalsRunnerContext(t *testing.T) {
	manager := tasks.NewManager()
	canceled := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(ctx context.Context, _ ytdlp.Request) (string, error) {
				<-ctx.Done()
				close(canceled)
				return "", ctx.Err()
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "best",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager, task.ID, tasks.Downloading)
	if _, err := engine.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("platform runner context was not canceled")
	}
	wantStatus(t, manager, task.ID, tasks.Canceled)
}

func TestEnginePlatformRetryUsesCanonicalStoredSpecAndFreshRunner(t *testing.T) {
	manager := tasks.NewManager()
	requests := make(chan ytdlp.Request, 2)
	factoryCalls := 0
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			factoryCalls++
			attempt := factoryCalls
			return platformRunnerFunc(func(_ context.Context, request ytdlp.Request) (string, error) {
				requests <- request
				if attempt == 1 {
					return "", &ytdlp.Error{Code: ytdlp.CodeNetwork, Message: "safe internal message"}
				}
				return "/downloads/retried-platform.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := engine.Start(context.Background(), JobSpec{
		URL:       "https://www.youtube.com/watch?v=_mVb1D8wHxg&feature=share",
		Title:     "same title",
		MediaType: "platform",
		Quality:   "1080",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager, original.ID, tasks.Failed)
	retry, err := engine.Retry(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitStatus(t, manager, retry.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/retried-platform.mp4" {
		t.Fatalf("OutputPath = %q", completed.OutputPath)
	}
	wantRequest := ytdlp.Request{
		URL:     "https://www.youtube.com/watch?v=_mVb1D8wHxg",
		Title:   "same title",
		Quality: ytdlp.Quality1080,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if got := <-requests; got != wantRequest {
			t.Fatalf("attempt %d request = %#v, want %#v", attempt, got, wantRequest)
		}
	}
	if factoryCalls != 2 {
		t.Fatalf("factory calls = %d, want 2", factoryCalls)
	}
}

func TestEnginePlatformPublishedPathCompletesDespiteCleanupWarning(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				path := "/downloads/published-platform.mp4"
				return path, &publishedTestError{path: path, err: errors.New("cleanup failed")}
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "720",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/published-platform.mp4" {
		t.Fatalf("OutputPath = %q", completed.OutputPath)
	}
}

func TestEnginePlatformJoinedPublishedPathWinsCancelAndReturnedPath(t *testing.T) {
	manager := tasks.NewManager()
	published := make(chan struct{})
	returnResult := make(chan struct{})
	const realPath = "/downloads/actually-published-platform.mp4"
	engine, err := newEngine(engineDeps{
		manager: manager,
		newPlatformRunner: func(ytdlp.ProgressFunc) (platformRunner, error) {
			return platformRunnerFunc(func(context.Context, ytdlp.Request) (string, error) {
				close(published)
				<-returnResult
				return "/downloads/untrusted-return-value.mp4", errors.Join(
					errors.New("secondary cleanup warning"),
					output.NewPublishedError(realPath, errors.New("primary cleanup warning")),
				)
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: "https://youtu.be/_mVb1D8wHxg", MediaType: "platform", Quality: "720",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("platform runner did not publish")
	}
	if _, err := engine.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	close(returnResult)
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != realPath {
		t.Fatalf("OutputPath = %q, want PublishedPath %q", completed.OutputPath, realPath)
	}
}

func TestEngineHLSUsesExactInspectedManifestAndMergingState(t *testing.T) {
	manager := tasks.NewManager()
	manifest := []byte("#EXTM3U\n#EXTINF:4,\nsegment.ts\n")
	originalURL := "https://media.example/old/master.m3u8"
	finalURL := "https://cdn.example/new/master.m3u8"
	runnerStarted := make(chan ffmpeg.Request, 1)
	release := make(chan struct{})

	engine, err := newEngine(engineDeps{
		manager: manager,
		inspector: manifestInspectorFunc(func(_ context.Context, rawURL string) (ManifestInspection, error) {
			if rawURL != originalURL {
				t.Fatalf("inspect URL = %q", rawURL)
			}
			return ManifestInspection{Playlist: &hls.Playlist{}, Manifest: append([]byte(nil), manifest...), SourceURL: finalURL}, nil
		}),
		newHLSRunner: func(_ ffmpeg.ProgressFunc) (hlsRunner, error) {
			return hlsRunnerFunc(func(ctx context.Context, request ffmpeg.Request) (string, error) {
				runnerStarted <- request
				select {
				case <-release:
					return "/downloads/hls.mp4", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	task, err := engine.Start(context.Background(), JobSpec{
		URL: originalURL, Title: "HLS title", MediaType: "hls",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	var request ffmpeg.Request
	select {
	case request = <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("HLS runner did not start")
	}
	wantStatus(t, manager, task.ID, tasks.Merging)
	if request.SourceURL != finalURL || request.Title != "HLS title" {
		t.Fatalf("request = %#v", request)
	}
	if !bytes.Equal(request.Manifest, manifest) {
		t.Fatalf("manifest = %q, want exact %q", request.Manifest, manifest)
	}

	close(release)
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/hls.mp4" {
		t.Fatalf("output path = %q", completed.OutputPath)
	}
}

func TestEngineHLSRetryUsesFreshFinalManifestURL(t *testing.T) {
	manager := tasks.NewManager()
	manifest := []byte("#EXTM3U\n#EXTINF:4,\nrelative.ts\n")
	finalURLs := []string{"https://cdn-one.example/new/master.m3u8", "https://cdn-two.example/new/master.m3u8"}
	var mu sync.Mutex
	inspectCalls := 0
	runCalls := 0
	requests := make(chan ffmpeg.Request, 2)
	engine, err := newEngine(engineDeps{
		manager: manager,
		inspector: manifestInspectorFunc(func(_ context.Context, rawURL string) (ManifestInspection, error) {
			if rawURL != "https://origin.example/old/master.m3u8" {
				return ManifestInspection{}, errors.New("unexpected source URL")
			}
			mu.Lock()
			defer mu.Unlock()
			result := ManifestInspection{Playlist: &hls.Playlist{}, Manifest: append([]byte(nil), manifest...), SourceURL: finalURLs[inspectCalls]}
			inspectCalls++
			return result, nil
		}),
		newHLSRunner: func(_ ffmpeg.ProgressFunc) (hlsRunner, error) {
			mu.Lock()
			attempt := runCalls
			runCalls++
			mu.Unlock()
			return hlsRunnerFunc(func(_ context.Context, request ffmpeg.Request) (string, error) {
				requests <- request
				if attempt == 0 {
					return "", errors.New("first run failed")
				}
				return "/downloads/retried.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	original, err := engine.Start(context.Background(), JobSpec{URL: "https://origin.example/old/master.m3u8", Title: "视频", MediaType: "hls"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, manager, original.ID, tasks.Failed)
	if got := (<-requests).SourceURL; got != finalURLs[0] {
		t.Fatalf("first SourceURL = %q, want %q", got, finalURLs[0])
	}
	retry, err := engine.Retry(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitStatus(t, manager, retry.ID, tasks.Completed)
	if got := (<-requests).SourceURL; got != finalURLs[1] {
		t.Fatalf("retry SourceURL = %q, want %q", got, finalURLs[1])
	}
}

func TestEngineCancelSignalsTaskContext(t *testing.T) {
	manager := tasks.NewManager()
	workerCanceled := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(ctx context.Context, _, _ string) (string, error) {
				<-ctx.Done()
				close(workerCanceled)
				return "", ctx.Err()
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/v.mp4", MediaType: "mp4"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, manager, task.ID, tasks.Downloading)
	if _, err := engine.Cancel(task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("task context was not canceled")
	}
	wantStatus(t, manager, task.ID, tasks.Canceled)
}

func TestEngineShutdownCancelsAndWaitsForWorkerCleanup(t *testing.T) {
	manager := tasks.NewManager()
	started := make(chan struct{})
	cleaned := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(ctx context.Context, _, _ string) (string, error) {
				close(started)
				<-ctx.Done()
				close(cleaned)
				return "", ctx.Err()
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/video.mp4", MediaType: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("Shutdown returned before cleanup")
	}
	wantStatus(t, manager, task.ID, tasks.Canceled)
	var closed *EngineClosedError
	if _, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/new.mp4", MediaType: "mp4"}); !errors.As(err, &closed) {
		t.Fatalf("Start after shutdown error = %T %v", err, err)
	}
}

func TestEngineShutdownHonorsTimeoutThenCanFinish(t *testing.T) {
	manager := tasks.NewManager()
	started := make(chan struct{})
	release := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(context.Context, string, string) (string, error) {
				close(started)
				<-release
				return "", context.Canceled
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/video.mp4", MediaType: "mp4"}); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := engine.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	close(release)
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

func TestEngineStartRacingShutdownIsSafe(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(ctx context.Context, _, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var callers sync.WaitGroup
	for index := 0; index < 40; index++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, _ = engine.Start(context.Background(), JobSpec{URL: "https://media.example/video.mp4", MediaType: "mp4"})
		}()
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- engine.Shutdown(context.Background()) }()
	callers.Wait()
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	for _, task := range manager.List() {
		if task.Status != tasks.Canceled {
			t.Fatalf("task %s status = %s", task.ID, task.Status)
		}
	}
}

func TestPublishedMP4WinsConcurrentCancel(t *testing.T) {
	manager := tasks.NewManager()
	published := make(chan struct{})
	returnPath := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(context.Context, string, string) (string, error) {
				close(published)
				<-returnPath
				return "/downloads/published.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/video.mp4", MediaType: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	<-published
	if _, err := engine.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	close(returnPath)
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/published.mp4" {
		t.Fatalf("OutputPath = %q", completed.OutputPath)
	}
}

func TestPublishedHLSWinsConcurrentCancel(t *testing.T) {
	manager := tasks.NewManager()
	published := make(chan struct{})
	returnPath := make(chan struct{})
	engine, err := newEngine(engineDeps{
		manager: manager,
		inspector: manifestInspectorFunc(func(context.Context, string) (ManifestInspection, error) {
			return ManifestInspection{Playlist: &hls.Playlist{}, Manifest: []byte("#EXTM3U\n#EXTINF:1,\na.ts\n"), SourceURL: "https://cdn.example/final.m3u8"}, nil
		}),
		newHLSRunner: func(_ ffmpeg.ProgressFunc) (hlsRunner, error) {
			return hlsRunnerFunc(func(context.Context, ffmpeg.Request) (string, error) {
				close(published)
				<-returnPath
				return "/downloads/published-hls.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://origin.example/master.m3u8", MediaType: "hls"})
	if err != nil {
		t.Fatal(err)
	}
	<-published
	if _, err := engine.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	close(returnPath)
	completed := waitStatus(t, manager, task.ID, tasks.Completed)
	if completed.OutputPath != "/downloads/published-hls.mp4" {
		t.Fatalf("OutputPath = %q", completed.OutputPath)
	}
}

func TestPublishedCleanupWarningsCompleteDirectAndHLSJobs(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		configure func(*engineDeps, string, chan struct{}, chan struct{})
	}{
		{
			name:      "direct",
			mediaType: "mp4",
			configure: func(deps *engineDeps, path string, published, release chan struct{}) {
				deps.newDownloader = func(download.ProgressFunc) (directDownloader, error) {
					return directDownloaderFunc(func(context.Context, string, string) (string, error) {
						if err := os.WriteFile(path, []byte("direct"), 0o600); err != nil {
							return "", err
						}
						close(published)
						<-release
						return path, &publishedTestError{path: path, err: errors.New("cleanup failed")}
					}), nil
				}
			},
		},
		{
			name:      "hls",
			mediaType: "hls",
			configure: func(deps *engineDeps, path string, published, release chan struct{}) {
				deps.inspector = manifestInspectorFunc(func(context.Context, string) (ManifestInspection, error) {
					return ManifestInspection{Playlist: &hls.Playlist{}, Manifest: []byte("#EXTM3U\n#EXTINF:1,\na.ts\n"), SourceURL: "https://cdn.example/final.m3u8"}, nil
				})
				deps.newHLSRunner = func(ffmpeg.ProgressFunc) (hlsRunner, error) {
					return hlsRunnerFunc(func(context.Context, ffmpeg.Request) (string, error) {
						if err := os.WriteFile(path, []byte("hls"), 0o600); err != nil {
							return "", err
						}
						close(published)
						<-release
						return path, &publishedTestError{path: path, err: errors.New("directory sync failed")}
					}), nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name+".mp4")
			published := make(chan struct{})
			release := make(chan struct{})
			manager := tasks.NewManager()
			deps := engineDeps{manager: manager}
			tt.configure(&deps, path, published, release)
			engine, err := newEngine(deps)
			if err != nil {
				t.Fatal(err)
			}
			task, err := engine.Start(context.Background(), JobSpec{
				URL:       "https://media.example/video." + tt.mediaType,
				Title:     tt.name,
				MediaType: tt.mediaType,
			})
			if err != nil {
				t.Fatal(err)
			}
			<-published
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat published output: %v", err)
			}
			if _, err := engine.Cancel(task.ID); err != nil {
				t.Fatal(err)
			}
			close(release)
			completed := waitStatus(t, manager, task.ID, tasks.Completed)
			if completed.OutputPath != path {
				t.Fatalf("OutputPath = %q, want %q", completed.OutputPath, path)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatalf("published output disappeared: %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("completed task no longer owns the originally published inode")
			}
		})
	}
}

func TestShutdownCancellationNeverCompletesWithoutPublishedPath(t *testing.T) {
	manager := tasks.NewManager()
	started := make(chan struct{})
	engine, err := newEngine(engineDeps{manager: manager, newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
		return directDownloaderFunc(func(ctx context.Context, _, _ string) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/video.mp4", MediaType: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != tasks.Canceled || got.OutputPath != "" {
		t.Fatalf("task = %#v", got)
	}
}

func TestEngineFailureStoresOnlySafeChineseMessage(t *testing.T) {
	manager := tasks.NewManager()
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			return directDownloaderFunc(func(context.Context, string, string) (string, error) {
				return "", errors.New("GET https://secret.example/video.mp4?token=private /Users/person")
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	task, err := engine.Start(context.Background(), JobSpec{URL: "https://secret.example/video.mp4?token=private", MediaType: "mp4"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	failed := waitStatus(t, manager, task.ID, tasks.Failed)
	if failed.Error != "视频下载失败，请稍后重试" {
		t.Fatalf("public error = %q", failed.Error)
	}
	for _, secret := range []string{"secret.example", "token=private", "/Users/person"} {
		if strings.Contains(failed.Error, secret) {
			t.Fatalf("public error leaked %q: %q", secret, failed.Error)
		}
	}
}

func TestSafeFailureMappingUsesOnlyAllowlistedMessages(t *testing.T) {
	secret := "https://signed.example/video?token=secret /Users/person"
	tests := []struct {
		name          string
		err           error
		code, message string
	}{
		{name: "encrypted", err: &ffmpeg.Error{Code: ffmpeg.CodeEncrypted, Message: secret}, code: "encrypted_hls", message: "不支持加密或 DRM 视频"},
		{name: "ffmpeg missing", err: &ffmpeg.Error{Code: ffmpeg.CodeFFmpegMissing, Message: secret}, code: "ffmpeg_missing", message: "未安装 FFmpeg，请先安装后重试"},
		{name: "unsafe direct", err: &download.Error{Code: download.CodeUnsafeSource, Message: secret}, code: "unsafe_source", message: "视频下载地址不安全或无效"},
		{name: "output", err: &download.Error{Code: download.CodeOutput, Message: secret}, code: "output", message: "无法保存视频文件"},
		{name: "manifest encrypted", err: newManifestError("encrypted_hls", secret, errors.New(secret)), code: "encrypted_hls", message: "不支持加密或 DRM 视频"},
		{name: "unknown", err: errors.New(secret), code: "download_failed", message: "视频下载失败，请稍后重试"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCode, gotMessage := safeFailure(tc.err)
			if gotCode != tc.code || gotMessage != tc.message {
				t.Fatalf("safeFailure = %q %q", gotCode, gotMessage)
			}
			if strings.Contains(gotMessage, "signed.example") || strings.Contains(gotMessage, "token=secret") || strings.Contains(gotMessage, "/Users/person") {
				t.Fatalf("message leaked: %q", gotMessage)
			}
		})
	}
}

func TestSafeFailureMapsPlatformCodesToAllowlistedChineseMessages(t *testing.T) {
	tests := []struct {
		code    ytdlp.Code
		message string
	}{
		{code: ytdlp.CodeCanceled, message: "下载已取消"},
		{code: ytdlp.CodeLoginRequired, message: "当前视频需要登录，v0.2.0 暂不支持"},
		{code: ytdlp.CodeVerificationRequired, message: "YouTube 要求浏览器验证；为保护账号隐私，网页视频港不会读取登录信息"},
		{code: ytdlp.CodeNetworkFiltered, message: "当前网络阻止了本地下载连接，请联系网络管理员或更换网络"},
		{code: ytdlp.CodeJavaScriptRuntime, message: "视频解析组件不完整，请重新安装网页视频港"},
		{code: ytdlp.CodeAccessLimited, message: "当前内容受会员、付费或私有访问限制"},
		{code: ytdlp.CodeGeoRestricted, message: "当前网络所在地区无法访问此视频"},
		{code: ytdlp.CodeExtractor, message: "平台解析规则已变化，请升级网页视频港"},
		{code: ytdlp.CodeFFmpegMissing, message: "未安装 FFmpeg，请先安装后重试"},
		{code: ytdlp.CodeNetwork, message: "网络连接失败，请稍后重试"},
		{code: ytdlp.CodeOutput, message: "无法保存视频文件"},
		{code: ytdlp.CodeProcess, message: "平台暂时拒绝了下载，请稍后重试"},
	}
	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			gotCode, gotMessage := safeFailure(&ytdlp.Error{
				Code: tc.code, Message: "secret URL and local path",
			})
			if gotCode != string(tc.code) || gotMessage != tc.message {
				t.Fatalf("safeFailure = %q %q", gotCode, gotMessage)
			}
			if strings.Contains(gotMessage, "secret") {
				t.Fatalf("message leaked internal details: %q", gotMessage)
			}
		})
	}
}

func TestEngineRetryUsesStoredSpecAndFreshWorker(t *testing.T) {
	manager := tasks.NewManager()
	var mu sync.Mutex
	factoryCalls := 0
	seen := make([]JobSpec, 0, 2)
	engine, err := newEngine(engineDeps{
		manager: manager,
		newDownloader: func(_ download.ProgressFunc) (directDownloader, error) {
			mu.Lock()
			factoryCalls++
			attempt := factoryCalls
			mu.Unlock()
			return directDownloaderFunc(func(_ context.Context, rawURL, title string) (string, error) {
				mu.Lock()
				seen = append(seen, JobSpec{URL: rawURL, Title: title, MediaType: "mp4"})
				mu.Unlock()
				if attempt == 1 {
					return "", errors.New("first attempt failed")
				}
				return "/downloads/retry.mp4", nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	spec := JobSpec{URL: "https://media.example/video.mp4", Title: "same title", MediaType: "mp4"}
	original, err := engine.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, manager, original.ID, tasks.Failed)

	retry, err := engine.Retry(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.ID == original.ID || retry.Status != tasks.Queued {
		t.Fatalf("retry task = %#v", retry)
	}
	waitStatus(t, manager, retry.ID, tasks.Completed)

	mu.Lock()
	defer mu.Unlock()
	if factoryCalls != 2 {
		t.Fatalf("factory calls = %d, want 2 fresh workers", factoryCalls)
	}
	if len(seen) != 2 || seen[0] != spec || seen[1] != spec {
		t.Fatalf("seen specs = %#v", seen)
	}
}

func TestEngineRetryRejectsTaskWithoutStoredSpec(t *testing.T) {
	manager := tasks.NewManager()
	task, err := manager.Create("https://media.example/unowned.mp4", "unowned")
	if err != nil {
		t.Fatalf("create manager task: %v", err)
	}
	if _, err := manager.Fail(task.ID, "失败", errors.New("failure")); err != nil {
		t.Fatalf("fail manager task: %v", err)
	}
	engine, err := newEngine(engineDeps{manager: manager})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	_, err = engine.Retry(context.Background(), task.ID)
	var missing *SpecNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("Retry error = %T %v, want SpecNotFoundError", err, err)
	}
	if message := missing.SafeMessage(); message != "无法重试：缺少原任务信息" {
		t.Fatalf("safe message = %q", message)
	}
	if len(manager.List()) != 1 {
		t.Fatalf("retry created task without spec: %#v", manager.List())
	}
}

func TestEngineRejectsUnnormalizedMediaType(t *testing.T) {
	engine, err := newEngine(engineDeps{manager: tasks.NewManager()})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	for _, mediaType := range []string{"m3u8", "MP4", " hls ", ""} {
		if _, err := engine.Start(context.Background(), JobSpec{URL: "https://media.example/video", MediaType: mediaType}); err == nil {
			t.Fatalf("media type %q was accepted", mediaType)
		}
	}
}

type staticResolver struct{ addresses []net.IPAddr }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestManifestInspectorReturnsExactValidatedHLSBody(t *testing.T) {
	manifest := []byte("#EXTM3U\n#EXTINF:4,\nsegment.ts\n")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl; charset=utf-8"}},
			Body:       io.NopCloser(bytes.NewReader(manifest)),
			Request:    request,
		}, nil
	})}
	inspector := newManifestInspector(staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}, client)

	result, err := inspector.Inspect(context.Background(), "https://media.example/video.m3u8")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.Playlist == nil || len(result.Playlist.Variants) != 1 {
		t.Fatalf("playlist = %#v", result.Playlist)
	}
	if !bytes.Equal(result.Manifest, manifest) {
		t.Fatalf("manifest = %q, want exact %q", result.Manifest, manifest)
	}
	if result.SourceURL != "https://media.example/video.m3u8" {
		t.Fatalf("SourceURL = %q", result.SourceURL)
	}
}

func TestManifestInspectorRejectsBadStatusContentTypeAndOversize(t *testing.T) {
	valid := []byte("#EXTM3U\n#EXTINF:4,\nsegment.ts\n")
	tests := []struct {
		name        string
		status      int
		contentType string
		body        []byte
	}{
		{name: "status", status: http.StatusNotFound, contentType: "application/vnd.apple.mpegurl", body: valid},
		{name: "content type", status: http.StatusOK, contentType: "video/mp4", body: valid},
		{name: "oversize", status: http.StatusOK, contentType: "application/vnd.apple.mpegurl", body: bytes.Repeat([]byte("x"), maxManifestBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": []string{test.contentType}}, Body: io.NopCloser(bytes.NewReader(test.body)), Request: request}, nil
			})}
			inspector := newManifestInspector(staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}, client)
			_, err := inspector.Inspect(context.Background(), "https://media.example/video.m3u8?token=secret")
			if err == nil {
				t.Fatal("Inspect succeeded")
			}
			for _, secret := range []string{"media.example", "token=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestNewManifestInspectorUsesSafeTransport(t *testing.T) {
	inspector := NewManifestInspector(nil)
	transport, ok := inspector.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", inspector.client.Transport)
	}
	if transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("unsafe transport configuration: proxySet=%t dialSet=%t", transport.Proxy != nil, transport.DialContext != nil)
	}
}

func wantStatus(t *testing.T, manager *tasks.Manager, id string, want tasks.Status) {
	t.Helper()
	task, err := manager.Get(id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != want {
		t.Fatalf("status = %q, want %q", task.Status, want)
	}
}

func wantProgress(t *testing.T, manager *tasks.Manager, id string, want float64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.Get(id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.Progress == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("progress did not reach %v", want)
}

func waitStatus(t *testing.T, manager *tasks.Manager, id string, want tasks.Status) tasks.Task {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.Get(id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.Status == want {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task did not reach %q", want)
	return tasks.Task{}
}
