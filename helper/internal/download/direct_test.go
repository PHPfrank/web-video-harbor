package download

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestDirectRetriesTransientHTTPStatusesExactlyThreeTotalAttempts(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
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
	downloader, err := New(Config{
		Client:       client,
		OutputDir:    dir,
		Retry:        retry,
		OnProgress:   progress,
		Sleep:        sleep,
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
