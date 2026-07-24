//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"web-video-harbor/helper/internal/api"
	"web-video-harbor/helper/internal/tasks"
)

const smokeToken = "integration-test-token"

type exactFixtureResolver struct {
	hostPort string
}

func (r exactFixtureResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP(host)}}, nil
}

// AllowExactHostPort is deliberately implemented only by this test object.
// Production resolvers do not provide this capability.
func (r exactFixtureResolver) AllowExactHostPort(hostPort string) bool {
	return hostPort == r.hostPort
}

type noReveal struct{}

func (noReveal) Reveal(context.Context, string) error { return nil }

func TestHelperAllowsOnlyInjectedExactFixtureHost(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("fixture"))
	}))
	t.Cleanup(fixture.Close)

	parsed, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	resolver := exactFixtureResolver{hostPort: parsed.Host}
	downloadDir := t.TempDir()
	manager := tasks.NewManager()
	engine, err := api.NewEngine(manager, downloadDir, resolver, "ffmpeg", "")
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })

	server, err := api.New(api.Options{
		Token: smokeToken, Version: "integration", FFmpegAvailable: true,
		DownloadDir: downloadDir, Inspector: api.NewMediaInspector(resolver),
		Tasks: engine, Revealer: noReveal{},
	})
	if err != nil {
		t.Fatalf("create helper API: %v", err)
	}
	helper := httptest.NewServer(server.Handler())
	t.Cleanup(helper.Close)

	response := postJSON(t, helper.URL+"/v1/inspect", map[string]string{"url": fixture.URL + "/direct.mp4"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("inspect exact fixture host status = %d, want 200; body=%s", response.StatusCode, body)
	}

	otherURL := "http://127.0.0.1:1/direct.mp4"
	response = postJSON(t, helper.URL+"/v1/inspect", map[string]string{"url": otherURL})
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("inspect non-allowlisted host status = %d, want 400; body=%s", response.StatusCode, body)
	}
	var failure map[string]string
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatalf("decode unsafe response: %v", err)
	}
	if failure["code"] != "unsafe_source" {
		t.Fatalf("unsafe response code = %q, want unsafe_source", failure["code"])
	}

	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatalf("read download dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("inspect created download files: %v", entries)
	}
	if !filepath.IsAbs(downloadDir) {
		t.Fatalf("temporary download directory is not absolute: %q", downloadDir)
	}
}

func TestHelperDownloadWorkflow(t *testing.T) {
	repoRoot := os.Getenv("SMOKE_REPO_ROOT")
	generatedRoot := os.Getenv("SMOKE_FIXTURE_ROOT")
	downloadDir := os.Getenv("SMOKE_DOWNLOAD_DIR")
	resultsPath := os.Getenv("SMOKE_RESULTS_PATH")
	ffmpegPath := os.Getenv("SMOKE_FFMPEG_PATH")
	if repoRoot == "" || generatedRoot == "" || downloadDir == "" || resultsPath == "" || ffmpegPath == "" {
		t.Skip("run through scripts/run-smoke-test.zsh to provide generated fixture paths")
	}
	for _, directory := range []string{repoRoot, generatedRoot, downloadDir} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("required smoke directory %q is unavailable: %v", directory, err)
		}
	}

	fixture := newFixtureServer(t, filepath.Join(repoRoot, "tests", "fixtures", "site"), generatedRoot)
	parsedFixture, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	resolver := exactFixtureResolver{hostPort: parsedFixture.Host}
	manager := tasks.NewManager()
	engine, err := api.NewEngine(manager, downloadDir, resolver, ffmpegPath, "")
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := engine.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown engine: %v", err)
		}
	})
	helperAPI, err := api.New(api.Options{
		Token: smokeToken, Version: "integration", FFmpegAvailable: true,
		DownloadDir: downloadDir, Inspector: api.NewMediaInspector(resolver),
		Tasks: engine, Revealer: noReveal{},
	})
	if err != nil {
		t.Fatalf("create helper API: %v", err)
	}
	helperURL := startFixedHelperServer(t, helperAPI)

	var health struct {
		Ready   bool   `json:"ready"`
		Version string `json:"version"`
		FFmpeg  bool   `json:"ffmpeg"`
		PID     int    `json:"pid"`
	}
	getJSON(t, helperURL+"/health", false, &health)
	if !health.Ready || !health.FFmpeg || health.Version != "integration" || health.PID <= 1 {
		t.Fatalf("unexpected health response: %+v", health)
	}

	directURL := fixture.URL + "/direct.mp4"
	wechatURL := fixture.URL + "/wechat-stream?id=fixture"
	singleHLSURL := fixture.URL + "/extensionless.m3u8"
	masterURL := fixture.URL + "/master.m3u8"
	for _, mediaURL := range []string{directURL, wechatURL} {
		inspection := inspect(t, helperURL, mediaURL, http.StatusOK)
		if inspection.MediaType != "mp4" {
			t.Fatalf("inspect %s media type = %q, want mp4", mediaURL, inspection.MediaType)
		}
	}
	singleInspection := inspect(t, helperURL, singleHLSURL, http.StatusOK)
	if singleInspection.MediaType != "hls" || len(singleInspection.Variants) != 1 || singleInspection.Variants[0].Label != "原始画质" {
		t.Fatalf("unexpected single HLS inspection: %+v", singleInspection)
	}
	masterInspection := inspect(t, helperURL, masterURL, http.StatusOK)
	if masterInspection.MediaType != "hls" || len(masterInspection.Variants) != 2 {
		t.Fatalf("unexpected master HLS inspection: %+v", masterInspection)
	}
	if masterInspection.Variants[0].Label != "1080p" || masterInspection.Variants[1].Label != "720p" {
		t.Fatalf("master variants not quality sorted: %+v", masterInspection.Variants)
	}
	if masterInspection.Variants[0].URL != fixture.URL+"/1080/index.m3u8" {
		t.Fatalf("1080p variant URL = %q", masterInspection.Variants[0].URL)
	}

	encryptedResponse := postJSON(t, helperURL+"/v1/inspect", map[string]string{"url": fixture.URL + "/encrypted.m3u8"})
	assertErrorCode(t, encryptedResponse, http.StatusUnprocessableEntity, "encrypted_hls")

	direct := createTask(t, helperURL, api.JobSpec{URL: directURL, Title: "集成测试-直接视频", MediaType: "mp4"})
	direct = waitForStatus(t, helperURL, direct.ID, tasks.Completed, 15*time.Second)
	assertCompletedOutput(t, direct, downloadDir)

	single := createTask(t, helperURL, api.JobSpec{URL: singleHLSURL, Title: "集成测试-单清晰度", MediaType: "hls"})
	single = waitForStatus(t, helperURL, single.ID, tasks.Completed, 20*time.Second)
	assertCompletedOutput(t, single, downloadDir)

	multi := createTask(t, helperURL, api.JobSpec{URL: masterInspection.Variants[0].URL, Title: "集成测试-多清晰度-1080p", MediaType: "hls"})
	multi = waitForStatus(t, helperURL, multi.ID, tasks.Completed, 20*time.Second)
	assertCompletedOutput(t, multi, downloadDir)

	cancel := createTask(t, helperURL, api.JobSpec{URL: fixture.URL + "/slow.mp4", Title: "集成测试-取消", MediaType: "mp4"})
	waitForDownloadingProgress(t, helperURL, cancel.ID, 10*time.Second)
	cancelResponse := postWithoutBody(t, helperURL+"/v1/tasks/"+url.PathEscape(cancel.ID)+"/cancel")
	decodeStatus(t, cancelResponse, http.StatusOK, &cancel)
	if cancel.Status != tasks.Canceled {
		t.Fatalf("cancel status = %q, want canceled", cancel.Status)
	}
	cancel = waitForStatus(t, helperURL, cancel.ID, tasks.Canceled, 5*time.Second)
	if cancel.OutputPath != "" {
		t.Fatalf("canceled task published output %q", cancel.OutputPath)
	}
	assertNoDownloadStaging(t, downloadDir)

	var listed []tasks.Task
	getJSON(t, helperURL+"/v1/tasks", true, &listed)
	if len(listed) != 4 {
		t.Fatalf("task count = %d, want 4", len(listed))
	}

	results := map[string]string{
		"direct":      direct.OutputPath,
		"single_hls":  single.OutputPath,
		"master_1080": multi.OutputPath,
	}
	resultBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("encode smoke results: %v", err)
	}
	if err := os.WriteFile(resultsPath, append(resultBytes, '\n'), 0o600); err != nil {
		t.Fatalf("write smoke results: %v", err)
	}
	runExtensionHelperFallback(t, repoRoot, fixture.URL, downloadDir)
	runChromeExtensionSmoke(t, repoRoot, fixture.URL, downloadDir)
}

func startFixedHelperServer(t *testing.T, helperAPI *api.Server) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:17432")
	if err != nil {
		t.Fatalf("listen on extension helper address 127.0.0.1:17432: %v", err)
	}
	httpServer := helperAPI.HTTPServer(listener.Addr().String())
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown helper HTTP server: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve helper HTTP server: %v", err)
		}
	})
	return "http://" + listener.Addr().String()
}

func runExtensionHelperFallback(t *testing.T, repoRoot, fixtureURL, downloadDir string) {
	t.Helper()
	resultsPath := filepath.Join(repoRoot, "work", "smoke-extension-helper-results.json")
	logPath := filepath.Join(repoRoot, "work", "smoke-browser", "extension-helper-fallback.log")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repoRoot, "tests", "integration", "extension_helper_smoke.cjs"))
	command.Env = append(os.Environ(),
		"SMOKE_REPO_ROOT="+repoRoot,
		"SMOKE_FIXTURE_URL="+fixtureURL,
		"SMOKE_HELPER_TOKEN="+smokeToken,
		"SMOKE_DOWNLOAD_DIR="+downloadDir,
		"SMOKE_EXTENSION_RESULTS_PATH="+resultsPath,
	)
	output, err := command.CombinedOutput()
	if writeErr := os.WriteFile(logPath, output, 0o600); writeErr != nil {
		t.Fatalf("write extension/helper fallback log: %v", writeErr)
	}
	if err != nil {
		t.Fatalf("extension/helper fallback smoke: %v; output=%s", err, output)
	}
}

func runChromeExtensionSmoke(t *testing.T, repoRoot, fixtureURL, downloadDir string) {
	t.Helper()
	chromePath := os.Getenv("SMOKE_CHROME_PATH")
	browserRoot := os.Getenv("SMOKE_BROWSER_ROOT")
	resultsPath := os.Getenv("SMOKE_BROWSER_RESULTS_PATH")
	if chromePath == "" || browserRoot == "" || resultsPath == "" {
		t.Log("Chrome CDP smoke skipped: browser environment was not provided")
		return
	}
	logPath := filepath.Join(browserRoot, "chrome-smoke-run.log")
	pidPath := filepath.Join(browserRoot, fmt.Sprintf("chrome-%d-%d.pid", os.Getpid(), time.Now().UnixNano()))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repoRoot, "tests", "integration", "chrome_extension_smoke.mjs"))
	command.Env = append(os.Environ(),
		"SMOKE_REPO_ROOT="+repoRoot,
		"SMOKE_FIXTURE_URL="+fixtureURL,
		"SMOKE_HELPER_TOKEN="+smokeToken,
		"SMOKE_DOWNLOAD_DIR="+downloadDir,
		"SMOKE_BROWSER_ROOT="+browserRoot,
		"SMOKE_BROWSER_RESULTS_PATH="+resultsPath,
		"SMOKE_CHROME_PATH="+chromePath,
		"SMOKE_CHROME_PID_PATH="+pidPath,
	)
	output, err := superviseChromeCommand(command, pidPath, browserRoot)
	if writeErr := os.WriteFile(logPath, output, 0o600); writeErr != nil {
		t.Fatalf("write Chrome smoke log: %v", writeErr)
	}
	if err != nil {
		t.Fatalf("Chrome CDP extension smoke: %v; output=%s", err, output)
	}
}

func newFixtureServer(t *testing.T, siteRoot, generatedRoot string) *httptest.Server {
	t.Helper()
	static := http.FileServer(http.Dir(siteRoot))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/direct.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeFile(w, r, filepath.Join(generatedRoot, "direct.mp4"))
		case r.URL.Path == "/wechat-stream":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeFile(w, r, filepath.Join(generatedRoot, "direct.mp4"))
		case r.URL.Path == "/slow.mp4":
			serveSlowMP4(w, r)
		case r.URL.Path == "/encrypted.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:2,\nsegment.ts\n")
		case r.URL.Path == "/hls-extensionless/0" || r.URL.Path == "/hls-extensionless/1":
			segmentName := "segment000.ts"
			if strings.HasSuffix(r.URL.Path, "/1") {
				segmentName = "segment001.ts"
			}
			w.Header().Set("Content-Type", "video/mp2t")
			http.ServeFile(w, r, filepath.Join(generatedRoot, "hls", "720", segmentName))
		case strings.HasPrefix(r.URL.Path, "/720/segment") || strings.HasPrefix(r.URL.Path, "/1080/segment"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) != 2 || (parts[0] != "720" && parts[0] != "1080") || filepath.Base(parts[1]) != parts[1] {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "video/mp2t")
			http.ServeFile(w, r, filepath.Join(generatedRoot, "hls", parts[0], parts[1]))
		default:
			static.ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func serveSlowMP4(w http.ResponseWriter, r *http.Request) {
	const total = 32 * 1024 * 1024
	chunk := bytes.Repeat([]byte{0x5a}, 32*1024)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", fmt.Sprint(total))
	flusher, _ := w.(http.Flusher)
	for written := 0; written < total; written += len(chunk) {
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(8 * time.Millisecond):
		}
	}
}

func inspect(t *testing.T, helperURL, mediaURL string, status int) api.Inspection {
	t.Helper()
	response := postJSON(t, helperURL+"/v1/inspect", map[string]string{"url": mediaURL})
	var result api.Inspection
	decodeStatus(t, response, status, &result)
	return result
}

func createTask(t *testing.T, helperURL string, spec api.JobSpec) tasks.Task {
	t.Helper()
	response := postJSON(t, helperURL+"/v1/tasks", spec)
	var task tasks.Task
	decodeStatus(t, response, http.StatusCreated, &task)
	return task
}

func waitForDownloadingProgress(t *testing.T, helperURL, id string, timeout time.Duration) tasks.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var task tasks.Task
		getJSON(t, helperURL+"/v1/tasks/"+url.PathEscape(id), true, &task)
		if task.Status == tasks.Downloading && task.Progress > 0 && task.Progress < 100 {
			return task
		}
		if task.Status == tasks.Failed || task.Status == tasks.Completed || task.Status == tasks.Canceled {
			t.Fatalf("task reached %q before cancellable progress: %+v", task.Status, task)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not report downloading progress within %s", id, timeout)
	return tasks.Task{}
}

func waitForStatus(t *testing.T, helperURL, id string, wanted tasks.Status, timeout time.Duration) tasks.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var task tasks.Task
	for time.Now().Before(deadline) {
		getJSON(t, helperURL+"/v1/tasks/"+url.PathEscape(id), true, &task)
		if task.Status == wanted {
			return task
		}
		if task.Status == tasks.Failed {
			t.Fatalf("task failed while waiting for %q: %+v", wanted, task)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("task status = %q, want %q within %s: %+v", task.Status, wanted, timeout, task)
	return tasks.Task{}
}

func assertCompletedOutput(t *testing.T, task tasks.Task, downloadDir string) {
	t.Helper()
	if task.Status != tasks.Completed || task.Progress != 100 {
		t.Fatalf("task did not complete at 100%%: %+v", task)
	}
	info, err := os.Stat(task.OutputPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("completed output %q is invalid: info=%v err=%v", task.OutputPath, info, err)
	}
	relative, err := filepath.Rel(downloadDir, task.OutputPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatalf("output escaped smoke download directory: path=%q rel=%q err=%v", task.OutputPath, relative, err)
	}
}

func assertNoDownloadStaging(t *testing.T, downloadDir string) {
	t.Helper()
	if err := waitForNoDownloadStaging(downloadDir, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func waitForNoDownloadStaging(downloadDir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastLeftovers []string
	var lastErr error
	for {
		lastLeftovers, lastErr = downloadStagingLeftovers(downloadDir)
		if lastErr == nil && len(lastLeftovers) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("canceled download cleanup timed out: leftovers=%v inspect_error=%v", lastLeftovers, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func downloadStagingLeftovers(downloadDir string) ([]string, error) {
	var leftovers []string
	err := filepath.WalkDir(downloadDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".web-video-") || strings.HasSuffix(name, ".part") {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	return leftovers, err
}

func assertErrorCode(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	var failure map[string]string
	decodeStatus(t, response, status, &failure)
	if failure["code"] != code {
		t.Fatalf("error code = %q, want %q; response=%v", failure["code"], code, failure)
	}
}

func decodeStatus(t *testing.T, response *http.Response, status int, target any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP status = %d, want %d; body=%s", response.StatusCode, status, body)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode HTTP response: %v", err)
	}
}

func getJSON(t *testing.T, endpoint string, authenticated bool, target any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("create GET request: %v", err)
	}
	if authenticated {
		request.Header.Set("X-Video-Helper-Token", smokeToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send GET request: %v", err)
	}
	decodeStatus(t, response, http.StatusOK, target)
}

func postWithoutBody(t *testing.T, endpoint string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatalf("create POST request: %v", err)
	}
	request.Header.Set("X-Video-Helper-Token", smokeToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send POST request: %v", err)
	}
	return response
}

func postJSON(t *testing.T, endpoint string, input any) *http.Response {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Video-Helper-Token", smokeToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	return response
}
