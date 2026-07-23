package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/tasks"
)

const testToken = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

type fakeInspector struct {
	result Inspection
	err    error
	urls   []string
}

func (f *fakeInspector) Inspect(_ context.Context, rawURL string) (Inspection, error) {
	f.urls = append(f.urls, rawURL)
	return f.result, f.err
}

type fakeTasks struct {
	mu         sync.Mutex
	items      map[string]tasks.Task
	startSpecs []JobSpec
	retryIDs   []string
	startCtx   context.Context
	retryCtx   context.Context
	err        error
}

func newFakeTasks() *fakeTasks { return &fakeTasks{items: make(map[string]tasks.Task)} }

func (f *fakeTasks) Start(ctx context.Context, spec JobSpec) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return tasks.Task{}, f.err
	}
	f.startSpecs = append(f.startSpecs, spec)
	f.startCtx = ctx
	t := tasks.Task{ID: "new-id", URL: spec.URL, Title: spec.Title, Status: tasks.Queued}
	f.items[t.ID] = t
	return t, nil
}

func (f *fakeTasks) List() []tasks.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]tasks.Task, 0, len(f.items))
	for _, task := range f.items {
		result = append(result, task)
	}
	return result
}

func (f *fakeTasks) Get(id string) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.items[id]
	if !ok {
		return tasks.Task{}, &tasks.NotFoundError{ID: id}
	}
	return t, nil
}

func (f *fakeTasks) Cancel(id string) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.items[id]
	if !ok {
		return tasks.Task{}, &tasks.NotFoundError{ID: id}
	}
	t.Status = tasks.Canceled
	f.items[id] = t
	return t, nil
}

func (f *fakeTasks) Retry(ctx context.Context, id string) (tasks.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return tasks.Task{}, f.err
	}
	if _, ok := f.items[id]; !ok {
		return tasks.Task{}, &tasks.NotFoundError{ID: id}
	}
	f.retryIDs = append(f.retryIDs, id)
	f.retryCtx = ctx
	t := tasks.Task{ID: "retry-id", Status: tasks.Queued}
	f.items[t.ID] = t
	return t, nil
}

type fakeRevealer struct {
	paths []string
	err   error
}

func (f *fakeRevealer) Reveal(_ context.Context, path string) error {
	f.paths = append(f.paths, path)
	return f.err
}

func newTestServer(t *testing.T, mutate func(*Options)) (*Server, *fakeTasks, *fakeInspector, *fakeRevealer, string) {
	t.Helper()
	dir := t.TempDir()
	service := newFakeTasks()
	inspector := &fakeInspector{result: Inspection{MediaType: "mp4"}}
	revealer := &fakeRevealer{}
	opts := Options{Token: testToken, Version: "1.2.3", FFmpegAvailable: true, DownloadDir: dir, Inspector: inspector, Tasks: service, Revealer: revealer}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv, service, inspector, revealer, dir
}

func perform(t *testing.T, handler http.Handler, method, path string, body []byte, token, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("X-Video-Helper-Token", token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeObject(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", rr.Body.String(), err)
	}
	return value
}

func TestHealthIsUnauthenticatedAndMinimal(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	rr := perform(t, srv.Handler(), http.MethodGet, "/health", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	got := decodeObject(t, rr)
	if len(got) != 4 || got["ready"] != true || got["version"] != "1.2.3" || got["ffmpeg"] != true || got["pid"] != float64(os.Getpid()) {
		t.Fatalf("health = %#v", got)
	}
	if got["pid"].(float64) <= 1 {
		t.Fatalf("health PID is not a real process: %#v", got)
	}
	for _, secret := range []string{testToken, "download", "task", "url", "path"} {
		if strings.Contains(strings.ToLower(rr.Body.String()), strings.ToLower(secret)) {
			t.Fatalf("health leaked %q: %s", secret, rr.Body.String())
		}
	}
}

func TestV1RoutesRequireExactToken(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	for _, token := range []string{"", testToken + "x", testToken[:len(testToken)-1], strings.ToUpper(testToken)} {
		rr := perform(t, srv.Handler(), http.MethodGet, "/v1/tasks", nil, token, "")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d", token, rr.Code)
		}
		if strings.Contains(rr.Body.String(), testToken) {
			t.Fatal("authentication response leaked token")
		}
	}
	if rr := perform(t, srv.Handler(), http.MethodGet, "/v1/tasks", nil, testToken, ""); rr.Code != http.StatusOK {
		t.Fatalf("valid token status = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCORSOnlyAcceptsChromeExtensionOrigins(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	valid := "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	rr := perform(t, srv.Handler(), http.MethodGet, "/v1/tasks", nil, testToken, valid)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid origin status = %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != valid {
		t.Fatalf("allow origin = %q", got)
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed")
	}
	for _, origin := range []string{"https://example.com", "null", "chrome-extension://abc", "chrome-extension://abcdefghijklmnopabcdefghijklmnop/", "chrome-extension://abcdefghijklmnopabcdefghijklmnoq", "chrome-extension://abcdefghijklmnopabcdefghijklmnop.evil"} {
		rr := perform(t, srv.Handler(), http.MethodGet, "/v1/tasks", nil, testToken, origin)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("origin %q status = %d", origin, rr.Code)
		}
		if rr.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("rejected origin reflected: %q", origin)
		}
	}
}

func TestPreflightIsLimited(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	req := httptest.NewRequest(http.MethodOptions, "/v1/tasks", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-video-helper-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("origin = %q", got)
	}
	if strings.Contains(rr.Header().Get("Access-Control-Allow-Origin"), "*") || rr.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("permissive CORS headers")
	}

	for _, tc := range []struct{ method, headers string }{{"DELETE", "x-video-helper-token"}, {"POST", "authorization"}} {
		req := httptest.NewRequest(http.MethodOptions, "/v1/tasks", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", tc.method)
		req.Header.Set("Access-Control-Request-Headers", tc.headers)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("preflight %#v status = %d", tc, rr.Code)
		}
	}
}

func TestStrictMethodsJSONAndSecurityHeaders(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodPost, "/health", 405}, {http.MethodGet, "/v1/inspect", 405}, {http.MethodPut, "/v1/tasks", 405}, {http.MethodGet, "/v1/tasks/x/cancel", 405}} {
		rr := perform(t, srv.Handler(), tc.method, tc.path, nil, testToken, "")
		if rr.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rr.Code, tc.want)
		}
		if rr.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" || rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("missing security headers: %#v", rr.Header())
		}
	}

	for _, body := range [][]byte{
		[]byte(`{"url":"https://media.example/video.mp4","extra":true}`),
		[]byte(`{"url":"https://media.example/video.mp4"} {}`),
		bytes.Repeat([]byte("x"), maxJSONBody+1),
	} {
		rr := perform(t, srv.Handler(), http.MethodPost, "/v1/inspect", body, testToken, "")
		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("body len=%d status=%d body=%s", len(body), rr.Code, rr.Body.String())
		}
	}
}

func TestInspectReturnsMP4AndHLSVariants(t *testing.T) {
	for _, tc := range []Inspection{
		{MediaType: "mp4"},
		{MediaType: "hls", Variants: []hls.Variant{{URL: "https://cdn.example/720.m3u8?sig=secret", Label: "720p", Height: 720}}},
	} {
		srv, _, inspector, _, _ := newTestServer(t, func(o *Options) { o.Inspector = &fakeInspector{result: tc} })
		inspector = srv.inspector.(*fakeInspector)
		body := []byte(`{"url":"https://media.example/master.m3u8?sig=secret"}`)
		rr := perform(t, srv.Handler(), http.MethodPost, "/v1/inspect", body, testToken, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("inspect status = %d: %s", rr.Code, rr.Body.String())
		}
		if len(inspector.urls) != 1 {
			t.Fatalf("inspect calls = %#v", inspector.urls)
		}
		got := decodeObject(t, rr)
		if got["mediaType"] != tc.MediaType {
			t.Fatalf("result = %#v", got)
		}
	}
}

func TestInspectErrorsAreSafe(t *testing.T) {
	internal := errors.New("GET https://cdn.example/master.m3u8?token=secret: /Users/person/file")
	srv, _, _, _, _ := newTestServer(t, func(o *Options) {
		o.Inspector = &fakeInspector{err: &InspectError{Code: InspectUnsafe, Message: "视频地址不安全或无效", Status: http.StatusBadRequest, Err: internal}}
	})
	rr := perform(t, srv.Handler(), http.MethodPost, "/v1/inspect", []byte(`{"url":"https://cdn.example/master.m3u8?token=secret"}`), testToken, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	for _, secret := range []string{"token=secret", "/Users/person", "cdn.example"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Fatalf("error leaked %q: %s", secret, rr.Body.String())
		}
	}
}

func TestCreateListGetCancelAndRetry(t *testing.T) {
	srv, service, _, _, _ := newTestServer(t, nil)
	createBody := []byte(`{"url":"https://media.example/video.mp4","title":"示例视频","mediaType":"mp4"}`)
	rr := perform(t, srv.Handler(), http.MethodPost, "/v1/tasks", createBody, testToken, "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(service.startSpecs) != 1 || service.startSpecs[0].MediaType != "mp4" {
		t.Fatalf("start specs = %#v", service.startSpecs)
	}

	for _, path := range []string{"/v1/tasks", "/v1/tasks/new-id"} {
		rr := perform(t, srv.Handler(), http.MethodGet, path, nil, testToken, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rr.Code, rr.Body.String())
		}
	}
	rr = perform(t, srv.Handler(), http.MethodPost, "/v1/tasks/new-id/cancel", nil, testToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", rr.Code, rr.Body.String())
	}
	rr = perform(t, srv.Handler(), http.MethodPost, "/v1/tasks/new-id/retry", nil, testToken, "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("retry status = %d: %s", rr.Code, rr.Body.String())
	}
	if len(service.retryIDs) != 1 || service.retryIDs[0] != "new-id" {
		t.Fatalf("retry IDs = %#v", service.retryIDs)
	}
}

func TestAsyncStartsAreDetachedFromRequestCancellation(t *testing.T) {
	srv, service, _, _, _ := newTestServer(t, nil)
	parent, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"url":"https://media.example/video.mp4","title":"视频","mediaType":"mp4"}`)).WithContext(parent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Video-Helper-Token", testToken)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rr.Code, rr.Body.String())
	}
	cancel()
	if err := service.startCtx.Err(); err != nil {
		t.Fatalf("task inherited request cancellation: %v", err)
	}

	parent, cancel = context.WithCancel(context.Background())
	req = httptest.NewRequest(http.MethodPost, "/v1/tasks/new-id/retry", nil).WithContext(parent)
	req.Header.Set("X-Video-Helper-Token", testToken)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("retry status = %d: %s", rr.Code, rr.Body.String())
	}
	cancel()
	if err := service.retryCtx.Err(); err != nil {
		t.Fatalf("retry inherited request cancellation: %v", err)
	}
}

func TestCreateValidatesRequiredFieldsAndLengths(t *testing.T) {
	srv, service, _, _, _ := newTestServer(t, nil)
	for _, body := range []string{
		`{}`, `{"url":"https://x.example/a.mp4","title":"x","mediaType":"webm"}`,
		`{"url":"","title":"x","mediaType":"mp4"}`,
		`{"url":"https://x.example/a.mp4","title":"","mediaType":"mp4"}`,
		`{"url":"https://x.example/a.mp4","title":"` + strings.Repeat("a", maxTitleRunes+1) + `","mediaType":"mp4"}`,
		`{"url":"https://x.example/` + strings.Repeat("a", maxURLBytes) + `","title":"x","mediaType":"mp4"}`,
	} {
		rr := perform(t, srv.Handler(), http.MethodPost, "/v1/tasks", []byte(body), testToken, "")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %d bytes status = %d: %s", len(body), rr.Code, rr.Body.String())
		}
	}
	if len(service.startSpecs) != 0 {
		t.Fatalf("invalid specs started: %#v", service.startSpecs)
	}
}

func TestUnknownRouteAndTaskHaveStableSafeErrors(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	for _, path := range []string{"/missing", "/v1/tasks/missing", "/v1/tasks/missing/cancel", "/v1/tasks/missing/retry"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "cancel") || strings.HasSuffix(path, "retry") {
			method = http.MethodPost
		}
		rr := perform(t, srv.Handler(), method, path, nil, testToken, "")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d: %s", path, rr.Code, rr.Body.String())
		}
		got := decodeObject(t, rr)
		if got["message"] != "未找到请求的内容" {
			t.Fatalf("%s error = %#v", path, got)
		}
	}
}

func TestRevealOnlyCompletedExactRegularFileInsideDownloadDir(t *testing.T) {
	srv, service, _, revealer, dir := newTestServer(t, nil)
	good := filepath.Join(dir, "视频.mp4")
	if err := os.WriteFile(good, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.items["good"] = tasks.Task{ID: "good", Status: tasks.Completed, OutputPath: good}
	rr := perform(t, srv.Handler(), http.MethodPost, "/v1/tasks/good/reveal", nil, testToken, "")
	if rr.Code != http.StatusOK || len(revealer.paths) != 1 || revealer.paths[0] != good {
		t.Fatalf("good reveal = %d paths=%#v body=%s", rr.Code, revealer.paths, rr.Body.String())
	}

	outside := filepath.Join(t.TempDir(), "secret.mp4")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	symlink := filepath.Join(dir, "link.mp4")
	_ = os.Symlink(outside, symlink)
	insideDir := filepath.Join(dir, "folder")
	_ = os.Mkdir(insideDir, 0o700)
	service.items["active"] = tasks.Task{ID: "active", Status: tasks.Downloading, OutputPath: good}
	service.items["outside"] = tasks.Task{ID: "outside", Status: tasks.Completed, OutputPath: outside}
	service.items["traversal"] = tasks.Task{ID: "traversal", Status: tasks.Completed, OutputPath: filepath.Join(dir, "..", filepath.Base(filepath.Dir(outside)), filepath.Base(outside))}
	service.items["symlink"] = tasks.Task{ID: "symlink", Status: tasks.Completed, OutputPath: symlink}
	service.items["directory"] = tasks.Task{ID: "directory", Status: tasks.Completed, OutputPath: insideDir}
	for _, id := range []string{"active", "outside", "traversal", "symlink", "directory"} {
		rr := perform(t, srv.Handler(), http.MethodPost, "/v1/tasks/"+id+"/reveal", nil, testToken, "")
		if rr.Code != http.StatusConflict {
			t.Fatalf("reveal %s status=%d body=%s", id, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), dir) || strings.Contains(rr.Body.String(), outside) {
			t.Fatalf("reveal leaked path: %s", rr.Body.String())
		}
	}
}

func TestFinderRevealerRechecksFileAndUsesExplicitOpenBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "视频.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotArgs []string
	revealer := FinderRevealer{run: func(_ context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}}
	if err := revealer.Reveal(context.Background(), path); err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}
	if gotName != "/usr/bin/open" || len(gotArgs) != 2 || gotArgs[0] != "-R" || gotArgs[1] != path {
		t.Fatalf("command = %q %#v", gotName, gotArgs)
	}

	link := filepath.Join(dir, "link.mp4")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	gotName = ""
	if err := revealer.Reveal(context.Background(), link); err == nil {
		t.Fatal("Reveal symlink succeeded")
	}
	if gotName != "" {
		t.Fatal("command ran for symlink")
	}
}

func TestHTTPServerUsesConservativeTimeouts(t *testing.T) {
	srv, _, _, _, _ := newTestServer(t, nil)
	httpServer := srv.HTTPServer("127.0.0.1:0")
	if httpServer.Addr != "127.0.0.1:0" || httpServer.Handler == nil {
		t.Fatalf("HTTP server = %#v", httpServer)
	}
	for name, d := range map[string]time.Duration{"readHeader": httpServer.ReadHeaderTimeout, "read": httpServer.ReadTimeout, "write": httpServer.WriteTimeout, "idle": httpServer.IdleTimeout} {
		if d <= 0 || d > 2*time.Minute {
			t.Fatalf("%s timeout = %s", name, d)
		}
	}
}
