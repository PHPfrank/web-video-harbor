package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectExportedConstructorCannotInjectUnsafeHTTPClient(t *testing.T) {
	configType := reflect.TypeOf(Config{})
	if _, ok := configType.FieldByName("Client"); ok {
		t.Fatal("exported Config exposes an injectable HTTP client")
	}
	if _, ok := configType.FieldByName("skipURLCheck"); ok {
		t.Fatal("production Config contains a URL-check bypass")
	}

	resolver := staticResolver{addresses: map[string][]net.IPAddr{}}
	downloader, err := New(Config{OutputDir: t.TempDir(), Resolver: resolver})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	transport, ok := downloader.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("production transport type = %T, want *http.Transport", downloader.client.Transport)
	}
	if transport.Proxy != nil || transport.DialContext == nil || downloader.client.CheckRedirect == nil {
		t.Fatal("production constructor did not install safe transport and redirect policy")
	}
}

func TestDirectStreamsToPartAndPublishesOnlyWhenComplete(t *testing.T) {
	firstWritten := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first-"))
		w.(http.Flusher).Flush()
		close(firstWritten)
		<-finish
		_, _ = w.Write([]byte("second"))
	}))
	defer server.Close()

	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 1}, nil, nil)
	type result struct {
		path string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		path, err := downloader.Download(context.Background(), server.URL+"/movie.mp4?token=secret", "stream")
		resultCh <- result{path: path, err: err}
	}()

	select {
	case <-firstWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("server never started streaming")
	}
	part := waitForPartFile(t, dir)
	info, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("part file did not receive the first streamed chunk")
	}
	if _, err := os.Stat(filepath.Join(dir, "stream.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final file was exposed before completion: %v", err)
	}

	close(finish)
	got := <-resultCh
	if got.err != nil {
		t.Fatalf("Download() error = %v", got.err)
	}
	if got.path != filepath.Join(dir, "stream.mp4") {
		t.Fatalf("Download() path = %q", got.path)
	}
	contents, err := os.ReadFile(got.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first-second" {
		t.Fatalf("downloaded contents = %q", contents)
	}
	assertNoPartFiles(t, dir)
}

func TestDirectReportsProgressWithKnownAndUnknownLength(t *testing.T) {
	tests := []struct {
		name           string
		contentLength  string
		wantTotalBytes int64
	}{
		{name: "known", contentLength: "10", wantTotalBytes: 10},
		{name: "unknown", wantTotalBytes: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.contentLength != "" {
					w.Header().Set("Content-Length", tt.contentLength)
				}
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte("0123456789"))
			}))
			defer server.Close()

			var updates []Progress
			downloader := newTestDownloader(t, t.TempDir(), server.Client(), RetryPolicy{MaxAttempts: 1}, func(progress Progress) {
				updates = append(updates, progress)
			}, nil)
			if _, err := downloader.Download(context.Background(), server.URL+"/video.mp4", "progress"); err != nil {
				t.Fatalf("Download() error = %v", err)
			}
			if len(updates) == 0 {
				t.Fatal("progress callback was not called")
			}
			last := updates[len(updates)-1]
			if last.DownloadedBytes != 10 || last.TotalBytes != tt.wantTotalBytes {
				t.Fatalf("last progress = %+v", last)
			}
		})
	}
}

func TestDirectProgressDoesNotMoveBackwardAcrossRetries(t *testing.T) {
	transport := &progressRetryTransport{}
	client := &http.Client{Transport: transport}
	var updates []Progress
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, func(progress Progress) {
		updates = append(updates, progress)
	}, instantSleep)

	if _, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "monotonic"); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if transport.attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", transport.attempts.Load())
	}
	for index := 1; index < len(updates); index++ {
		if updates[index].DownloadedBytes < updates[index-1].DownloadedBytes {
			t.Fatalf("progress moved backward: %+v", updates)
		}
	}
	if len(updates) == 0 {
		t.Fatal("progress callback was not called")
	}
	last := updates[len(updates)-1]
	if last.DownloadedBytes != 10 || last.TotalBytes != 10 {
		t.Fatalf("final progress = %+v, want 10/10", last)
	}
}

func TestDirectCancellationStopsTransferAndCleansPart(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("chunk"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 3}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := downloader.Download(ctx, server.URL+"/video.mp4", "cancel")
		done <- err
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		assertDownloadCode(t, err, CodeCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Download() error does not wrap context cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Download() did not stop after cancellation")
	}
	assertDirectoryEmpty(t, dir)
}

func TestDirectHTTPErrorIsTypedAndDoesNotLeakSignedURL(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, err := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 3}, nil, nil).
		Download(context.Background(), server.URL+"/private.mp4?token=do-not-log", "missing")
	assertDownloadCode(t, err, CodeHTTPStatus)
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
	if strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("error leaked signed URL: %v", err)
	}
	assertDirectoryEmpty(t, dir)
}

func TestDirectRetriesTransientNetworkErrorsExactlyThreeTotalAttempts(t *testing.T) {
	transport := &sequenceTransport{networkFailures: 3}
	client := &http.Client{Transport: transport}
	var sleeps []time.Duration
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{
		MaxAttempts: 3,
		Backoff: func(failedAttempt int) time.Duration {
			return time.Duration(failedAttempt) * time.Second
		},
	}, nil, func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	})

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4?token=hidden", "network")
	assertDownloadCode(t, err, CodeNetwork)
	if transport.attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", transport.attempts.Load())
	}
	if got := len(sleeps); got != 2 || sleeps[0] != time.Second || sleeps[1] != 2*time.Second {
		t.Fatalf("retry sleeps = %v", sleeps)
	}
	if strings.Contains(err.Error(), "token=hidden") {
		t.Fatalf("error leaked signed URL: %v", err)
	}
	assertDirectoryEmpty(t, dir)
}

func TestDirectDoesNotRetryPermanentNetworkError(t *testing.T) {
	transport := &permanentFailureTransport{}
	client := &http.Client{Transport: transport}
	downloader := newTestDownloader(t, t.TempDir(), client, RetryPolicy{MaxAttempts: 3}, nil, func(context.Context, time.Duration) error {
		t.Fatal("permanent network errors must not sleep for retry")
		return nil
	})

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "permanent")
	assertDownloadCode(t, err, CodeNetwork)
	if transport.attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", transport.attempts.Load())
	}
}

func TestDirectDoesNotRetryPermanentResponseReadError(t *testing.T) {
	transport := &readFailureTransport{err: errors.New("permanent response corruption")}
	client := &http.Client{Transport: transport}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "permanent-read")
	assertDownloadCode(t, err, CodeTransfer)
	if transport.attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", transport.attempts.Load())
	}
	assertDirectoryEmpty(t, dir)
}

func TestDirectRetriesOnlyExplicitlyTransientResponseReadErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "timeout", err: readTimeoutError{}},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &readFailureTransport{err: tt.err}
			client := &http.Client{Transport: transport}
			dir := t.TempDir()
			downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

			_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "transient-read")
			assertDownloadCode(t, err, CodeTransfer)
			if transport.attempts.Load() != 3 {
				t.Fatalf("attempts = %d, want 3", transport.attempts.Load())
			}
			assertDirectoryEmpty(t, dir)
		})
	}
}

func TestDirectDoesNotRetryLocalPartWriteFailureAndCleansStaging(t *testing.T) {
	transport := &successfulBodyTransport{}
	client := &http.Client{Transport: transport}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)
	downloader.partWriter = func(io.Writer) io.Writer {
		return writerFunc(func([]byte) (int, error) {
			return 0, errors.New("local disk write failed")
		})
	}

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "disk-failure")
	if transport.attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", transport.attempts.Load())
	}
	assertDownloadCode(t, err, CodeOutput)
	assertDirectoryEmpty(t, dir)
}

func TestDirectDoesNotRetryPermanentResponseCloseError(t *testing.T) {
	transport := &closeFailureTransport{err: errors.New("permanent response close failure")}
	client := &http.Client{Transport: transport}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "permanent-close")
	if transport.attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", transport.attempts.Load())
	}
	if transport.closes.Load() != 1 {
		t.Fatalf("body closes = %d, want 1", transport.closes.Load())
	}
	assertDownloadCode(t, err, CodeTransfer)
	assertDirectoryEmpty(t, dir)
}

func TestDirectRetriesTransientResponseCloseErrorExactlyThreeTimes(t *testing.T) {
	transport := &closeFailureTransport{err: readTimeoutError{}}
	client := &http.Client{Transport: transport}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "transient-close")
	if transport.attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", transport.attempts.Load())
	}
	if transport.closes.Load() != 3 {
		t.Fatalf("body closes = %d, want 3", transport.closes.Load())
	}
	assertDownloadCode(t, err, CodeTransfer)
	assertDirectoryEmpty(t, dir)
}

func TestDirectRetriesTransientHTTPStatusesExactlyThreeTotalAttempts(t *testing.T) {
	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusNotImplemented,
		http.StatusServiceUnavailable,
		http.StatusHTTPVersionNotSupported,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			var sleeps atomic.Int32
			dir := t.TempDir()
			downloader := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 3}, nil, func(context.Context, time.Duration) error {
				sleeps.Add(1)
				return nil
			})

			_, err := downloader.Download(context.Background(), server.URL+"/video.mp4", "retry")
			assertDownloadCode(t, err, CodeHTTPStatus)
			if attempts.Load() != 3 || sleeps.Load() != 2 {
				t.Fatalf("attempts = %d, sleeps = %d; want 3 and 2", attempts.Load(), sleeps.Load())
			}
			assertDirectoryEmpty(t, dir)
		})
	}
}

func TestDirectRetryAfterUsesBoundedServerHintAndInjectedClock(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		status     int
		retryAfter string
		wantDelay  time.Duration
	}{
		{name: "429 delta seconds", status: http.StatusTooManyRequests, retryAfter: "120", wantDelay: 2 * time.Minute},
		{name: "503 HTTP date", status: http.StatusServiceUnavailable, retryAfter: now.Add(90 * time.Second).Format(http.TimeFormat), wantDelay: 90 * time.Second},
		{name: "server delay capped", status: http.StatusTooManyRequests, retryAfter: "999999", wantDelay: 5 * time.Minute},
		{name: "shorter than local ignored", status: http.StatusServiceUnavailable, retryAfter: "1", wantDelay: 2 * time.Second},
		{name: "invalid ignored", status: http.StatusTooManyRequests, retryAfter: "later", wantDelay: 2 * time.Second},
		{name: "expired date ignored", status: http.StatusServiceUnavailable, retryAfter: now.Add(-time.Minute).Format(http.TimeFormat), wantDelay: 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &statusThenSuccessTransport{status: tt.status, retryAfter: tt.retryAfter}
			client := &http.Client{Transport: transport}
			var sleeps []time.Duration
			dir := t.TempDir()
			downloader := newTestDownloader(t, dir, client, RetryPolicy{
				MaxAttempts: 3,
				Backoff: func(int) time.Duration {
					return 2 * time.Second
				},
			}, nil, func(_ context.Context, delay time.Duration) error {
				sleeps = append(sleeps, delay)
				return nil
			})
			downloader.now = func() time.Time { return now }

			path, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "retry-after")
			if err != nil {
				t.Fatalf("Download() error = %v", err)
			}
			if transport.attempts.Load() != 2 {
				t.Fatalf("attempts = %d, want 2", transport.attempts.Load())
			}
			if len(sleeps) != 1 || sleeps[0] != tt.wantDelay {
				t.Fatalf("retry sleeps = %v, want [%v]", sleeps, tt.wantDelay)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("published output missing: %v", err)
			}
		})
	}
}

func TestDirectDoesNotRetryOrdinary4xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	downloader := newTestDownloader(t, t.TempDir(), server.Client(), RetryPolicy{MaxAttempts: 3}, nil, func(context.Context, time.Duration) error {
		t.Fatal("ordinary 4xx must not sleep for retry")
		return nil
	})
	_, err := downloader.Download(context.Background(), server.URL+"/video.mp4", "forbidden")
	assertDownloadCode(t, err, CodeHTTPStatus)
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestDirectDoesNotRetryStatusOutside5xxRange(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode:    600,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("extension status")),
			ContentLength: 16,
		}, nil
	})}
	downloader := newTestDownloader(t, t.TempDir(), client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "extension")
	assertDownloadCode(t, err, CodeHTTPStatus)
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestDirectAcceptsOnlyHTTP200OK(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusNoContent, http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return &http.Response{
					StatusCode:    status,
					Header:        make(http.Header),
					Body:          io.NopCloser(strings.NewReader("not a complete video")),
					ContentLength: 20,
				}, nil
			})}
			dir := t.TempDir()
			downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

			_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "incomplete-status")
			assertDownloadCode(t, err, CodeHTTPStatus)
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
			assertDirectoryEmpty(t, dir)
		})
	}
}

func TestDirectRejectsEmptyHTTP200Body(t *testing.T) {
	for _, contentLength := range []int64{0, -1} {
		t.Run(fmt.Sprintf("content-length-%d", contentLength), func(t *testing.T) {
			var attempts atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          io.NopCloser(strings.NewReader("")),
					ContentLength: contentLength,
				}, nil
			})}
			dir := t.TempDir()
			downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

			_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "empty")
			assertDownloadCode(t, err, CodeTransfer)
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
			assertDirectoryEmpty(t, dir)
		})
	}
}

func TestDirectSafeClientRejectsPrivateRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/private.mp4", http.StatusFound)
	}))
	defer server.Close()

	resolver := staticResolver{addresses: map[string][]net.IPAddr{
		"media.example": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	client := newSafeHTTPClient(resolver, redirectingDialer{address: server.Listener.Addr().String()})
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 1}, nil, nil)

	_, err := downloader.Download(context.Background(), "http://media.example/video.mp4", "redirect")
	assertDownloadCode(t, err, CodeUnsafeSource)
	assertDirectoryEmpty(t, dir)
}

func TestDirectCleansPartAfterAllFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("temporary"))
	}))
	defer server.Close()
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	if _, err := downloader.Download(context.Background(), server.URL+"/video.mp4", "failure"); err == nil {
		t.Fatal("Download() error = nil")
	}
	assertDirectoryEmpty(t, dir)
}

func TestDirectFinalizationNeverOverwritesExistingSymlinkOrConcurrentTarget(t *testing.T) {
	body := []byte("new video")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	dir := t.TempDir()
	existing := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkTarget := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(symlinkTarget, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(dir, "movie (2).mp4")); err != nil {
		t.Fatal(err)
	}

	downloader := newTestDownloader(t, dir, server.Client(), RetryPolicy{MaxAttempts: 1}, nil, nil)
	const workers = 2
	start := make(chan struct{})
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			path, err := downloader.Download(context.Background(), server.URL+"/video.mp4", "movie")
			if err != nil {
				errs <- err
				return
			}
			results <- path
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("Download() error = %v", err)
	}

	seen := map[string]bool{}
	for path := range results {
		if seen[path] {
			t.Errorf("concurrent downloads published the same path %q", path)
		}
		seen[path] = true
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(body) {
			t.Errorf("published contents at %q = %q, %v", path, got, err)
		}
	}
	if len(seen) != workers {
		t.Fatalf("published paths = %d, want %d", len(seen), workers)
	}
	if got, _ := os.ReadFile(existing); string(got) != "existing" {
		t.Fatalf("existing file was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(symlinkTarget); string(got) != "keep" {
		t.Fatalf("symlink target was overwritten: %q", got)
	}
	assertNoPartFiles(t, dir)
}

func TestDirectRejectsShortContentLengthAndCleansPart(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("short")),
			ContentLength: 100,
		}, nil
	})}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 1}, nil, nil)

	_, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "short")
	assertDownloadCode(t, err, CodeTransfer)
	assertDirectoryEmpty(t, dir)
}

func TestDirectAlwaysClosesResponseBodies(t *testing.T) {
	var bodies []*trackedBody
	var mu sync.Mutex
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := &trackedBody{Reader: strings.NewReader("body")}
		mu.Lock()
		bodies = append(bodies, body)
		attempt := len(bodies)
		mu.Unlock()
		status := http.StatusServiceUnavailable
		if attempt == 3 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: body, ContentLength: 4}, nil
	})}
	dir := t.TempDir()
	downloader := newTestDownloader(t, dir, client, RetryPolicy{MaxAttempts: 3}, nil, instantSleep)

	if _, err := downloader.Download(context.Background(), "https://media.example/video.mp4", "bodies"); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	for i, body := range bodies {
		if !body.closed.Load() {
			t.Errorf("response body %d was not closed", i+1)
		}
	}
}

func newTestDownloader(t *testing.T, dir string, client *http.Client, retry RetryPolicy, progress ProgressFunc, sleep SleepFunc) *Downloader {
	t.Helper()
	downloader, err := newDownloader(internalConfig{
		client:       client,
		outputDir:    dir,
		retry:        retry,
		onProgress:   progress,
		sleep:        sleep,
		skipURLCheck: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return downloader
}

func waitForPartFile(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dir, ".web-video-*", "download.part"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			if info, err := os.Stat(matches[0]); err == nil && info.Size() > 0 {
				return matches[0]
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("part file did not appear")
	return ""
}

func assertNoPartFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".web-video-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("download staging paths remain: %v", matches)
	}
}

func assertDirectoryEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("output directory is not empty: %v", entries)
	}
}

func assertDownloadCode(t *testing.T, err error, want Code) {
	t.Helper()
	var downloadErr *Error
	if !errors.As(err, &downloadErr) {
		t.Fatalf("error type = %T, want *Error (%v)", err, err)
	}
	if downloadErr.Code != want {
		t.Fatalf("error code = %q, want %q (%v)", downloadErr.Code, want, err)
	}
}

func instantSleep(context.Context, time.Duration) error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary network failure" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

type sequenceTransport struct {
	networkFailures int32
	attempts        atomic.Int32
}

type permanentFailureTransport struct {
	attempts atomic.Int32
}

type readFailureTransport struct {
	err      error
	attempts atomic.Int32
}

type successfulBodyTransport struct {
	attempts atomic.Int32
}

type statusThenSuccessTransport struct {
	status     int
	retryAfter string
	attempts   atomic.Int32
}

type progressRetryTransport struct {
	attempts atomic.Int32
}

func (t *progressRetryTransport) RoundTrip(*http.Request) (*http.Response, error) {
	attempt := t.attempts.Add(1)
	if attempt == 1 {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(&errorAfterReader{data: []byte("12345678"), err: io.ErrUnexpectedEOF}),
			ContentLength: -1,
		}, nil
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(&chunkReader{chunks: [][]byte{[]byte("1234"), []byte("567890")}}),
		ContentLength: 10,
	}, nil
}

type chunkReader struct {
	chunks [][]byte
}

func (r *chunkReader) Read(destination []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(destination, chunk), nil
}

func (t *statusThenSuccessTransport) RoundTrip(*http.Request) (*http.Response, error) {
	attempt := t.attempts.Add(1)
	status := http.StatusOK
	body := "video"
	header := make(http.Header)
	if attempt == 1 {
		status = t.status
		body = "retry later"
		header.Set("Retry-After", t.retryAfter)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}, nil
}

type closeFailureTransport struct {
	err      error
	attempts atomic.Int32
	closes   atomic.Int32
}

func (t *closeFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          &closeFailureBody{Reader: strings.NewReader("video"), err: t.err, closes: &t.closes},
		ContentLength: 5,
	}, nil
}

type closeFailureBody struct {
	io.Reader
	err    error
	closes *atomic.Int32
}

func (b *closeFailureBody) Close() error {
	b.closes.Add(1)
	return b.err
}

func (t *successfulBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader("video")),
		ContentLength: 5,
	}, nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(data []byte) (int, error) { return f(data) }

func (t *readFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(&errorAfterReader{data: []byte("partial"), err: t.err}),
		ContentLength: -1,
	}, nil
}

type errorAfterReader struct {
	data []byte
	err  error
}

func (r *errorAfterReader) Read(destination []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(destination, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

type readTimeoutError struct{}

func (readTimeoutError) Error() string   { return "response read timed out" }
func (readTimeoutError) Timeout() bool   { return true }
func (readTimeoutError) Temporary() bool { return true }

func (t *permanentFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return nil, errors.New("certificate is permanently invalid")
}

func (t *sequenceTransport) RoundTrip(*http.Request) (*http.Response, error) {
	attempt := t.attempts.Add(1)
	if attempt <= t.networkFailures {
		return nil, temporaryError{}
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader("video")),
		ContentLength: 5,
	}, nil
}

type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return nil
}

type staticResolver struct {
	addresses map[string][]net.IPAddr
}

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r.addresses[host], nil
}

type redirectingDialer struct {
	address string
}

func (d redirectingDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, d.address)
}
