package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"web-video-downloader/helper/internal/download"
	"web-video-downloader/helper/internal/ffmpeg"
	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/tasks"
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
