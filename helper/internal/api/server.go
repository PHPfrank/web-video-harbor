// Package api exposes the authenticated loopback HTTP API used by the browser
// extension. It deliberately keeps unsafe HTTP injection seams package-private.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"web-video-harbor/helper/internal/hls"
	"web-video-harbor/helper/internal/tasks"
)

const (
	maxJSONBody            = 64 * 1024
	maxTitleRunes          = 200
	maxURLBytes            = 8192
	maxHealthVersionLength = 64
)

var extensionOriginPattern = regexp.MustCompile(`^chrome-extension://[a-p]{32}$`)
var healthVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// Inspection is the safe media metadata returned to the extension.
type Inspection struct {
	MediaType string        `json:"mediaType"`
	Variants  []hls.Variant `json:"variants,omitempty"`
}

// InspectCode identifies stable inspection failures.
type InspectCode string

const (
	InspectUnsafe      InspectCode = "unsafe_source"
	InspectHTTP        InspectCode = "http_status"
	InspectUnsupported InspectCode = "unsupported_media"
	InspectMalformed   InspectCode = "invalid_manifest"
	InspectEncrypted   InspectCode = "encrypted_hls"
	InspectTooLarge    InspectCode = "response_too_large"
	InspectNetwork     InspectCode = "network"
)

// InspectError contains a safe message and keeps diagnostics out of JSON.
type InspectError struct {
	Code    InspectCode
	Message string
	Status  int
	Err     error
}

func (e *InspectError) Error() string { return e.Message }
func (e *InspectError) Unwrap() error { return e.Err }

// MediaInspector inspects a remote candidate without exposing response bytes.
type MediaInspector interface {
	Inspect(context.Context, string) (Inspection, error)
}

// TaskService is the narrow task/engine surface needed by the HTTP API.
type TaskService interface {
	Start(context.Context, JobSpec) (tasks.Task, error)
	List() []tasks.Task
	Get(string) (tasks.Task, error)
	Cancel(string) (tasks.Task, error)
	Retry(context.Context, string) (tasks.Task, error)
}

// Revealer displays a completed file in the platform file manager.
type Revealer interface {
	Reveal(context.Context, string) error
}

type commandRunner func(context.Context, string, ...string) error

// FinderRevealer invokes Finder directly, without a shell.
type FinderRevealer struct{ run commandRunner }

func (f FinderRevealer) Reveal(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("reveal target is not a regular file")
	}
	run := f.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	return run(ctx, "/usr/bin/open", "-R", path)
}

// Options configures an API handler.
type Options struct {
	Token                       string
	Version                     string
	FFmpegAvailable             bool
	PlatformDownloaderAvailable bool
	PlatformDownloaderVersion   string
	JavaScriptRuntimeAvailable  bool
	JavaScriptRuntimeVersion    string
	DownloadDir                 string
	Inspector                   MediaInspector
	Tasks                       TaskService
	Revealer                    Revealer
}

type platformDownloaderStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
}

// Server owns the immutable API configuration.
type Server struct {
	tokenHash          [32]byte
	version            string
	processID          int
	ffmpegAvailable    bool
	platformDownloader platformDownloaderStatus
	javascriptRuntime  platformDownloaderStatus
	downloadDir        string
	inspector          MediaInspector
	tasks              TaskService
	revealer           Revealer
	handler            http.Handler
}

// New validates dependencies and constructs the API handler.
func New(options Options) (*Server, error) {
	if options.Token == "" {
		return nil, errors.New("API token is required")
	}
	if options.Inspector == nil {
		return nil, errors.New("media inspector is required")
	}
	if options.Tasks == nil {
		return nil, errors.New("task service is required")
	}
	if options.Revealer == nil {
		return nil, errors.New("file revealer is required")
	}
	absDir, err := filepath.Abs(options.DownloadDir)
	if err != nil {
		return nil, fmt.Errorf("resolve download directory: %w", err)
	}
	info, err := os.Lstat(absDir)
	if err != nil {
		return nil, fmt.Errorf("inspect download directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("download directory must be a real directory")
	}

	platformVersion := safeHealthVersion(options.PlatformDownloaderVersion)
	platformAvailable := options.PlatformDownloaderAvailable && platformVersion != ""
	if !platformAvailable {
		platformVersion = ""
	}
	runtimeVersion := safeHealthVersion(options.JavaScriptRuntimeVersion)
	runtimeAvailable := options.JavaScriptRuntimeAvailable && runtimeVersion != ""
	if !runtimeAvailable {
		runtimeVersion = ""
	}

	s := &Server{
		tokenHash: sha256.Sum256([]byte(options.Token)), version: options.Version,
		processID:       os.Getpid(),
		ffmpegAvailable: options.FFmpegAvailable,
		platformDownloader: platformDownloaderStatus{
			Available: platformAvailable,
			Version:   platformVersion,
		},
		javascriptRuntime: platformDownloaderStatus{
			Available: runtimeAvailable,
			Version:   runtimeVersion,
		},
		downloadDir: filepath.Clean(absDir),
		inspector:   options.Inspector, tasks: options.Tasks, revealer: options.Revealer,
	}
	s.handler = http.HandlerFunc(s.serveHTTP)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

// HTTPServer returns a server with bounded resource timeouts. The caller owns
// Listen and Shutdown so startup failures cannot be hidden.
func (s *Server) HTTPServer(address string) *http.Server {
	return &http.Server{
		Addr: address, Handler: s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !validExtensionOrigin(origin) {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "不允许此网页访问本地助手")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		s.handlePreflight(w, r, origin)
		return
	}
	if r.URL.Path == "/health" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ready":              true,
			"version":            s.version,
			"ffmpeg":             s.ffmpegAvailable,
			"platformDownloader": s.platformDownloader,
			"javascriptRuntime":  s.javascriptRuntime,
			"pid":                s.processID,
		})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		writeError(w, http.StatusNotFound, "not_found", "未找到请求的内容")
		return
	}
	if !s.authenticated(r.Header.Get("X-Video-Helper-Token")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "配对密钥无效")
		return
	}
	s.routeV1(w, r)
}

func safeHealthVersion(version string) string {
	if len(version) == 0 || len(version) > maxHealthVersionLength || !healthVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func validExtensionOrigin(origin string) bool {
	if !extensionOriginPattern.MatchString(origin) {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "chrome-extension" && parsed.Host != "" && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}

func (s *Server) authenticated(token string) bool {
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], s.tokenHash[:]) == 1
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request, origin string) {
	if origin == "" || !strings.HasPrefix(r.URL.Path, "/v1/") || allowedMethods(r.URL.Path) == "" {
		writeError(w, http.StatusForbidden, "preflight_rejected", "跨域预检请求无效")
		return
	}
	requestedMethod := r.Header.Get("Access-Control-Request-Method")
	if !methodListed(requestedMethod, allowedMethods(r.URL.Path)) {
		writeError(w, http.StatusForbidden, "preflight_rejected", "跨域预检请求无效")
		return
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		header = strings.ToLower(strings.TrimSpace(header))
		if header != "" && header != "content-type" && header != "x-video-helper-token" {
			writeError(w, http.StatusForbidden, "preflight_rejected", "跨域预检请求无效")
			return
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", allowedMethods(r.URL.Path))
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Video-Helper-Token")
	w.WriteHeader(http.StatusNoContent)
}

func methodListed(method, list string) bool {
	for _, candidate := range strings.Split(list, ",") {
		if strings.TrimSpace(candidate) == method {
			return true
		}
	}
	return false
}

func allowedMethods(path string) string {
	switch {
	case path == "/v1/inspect":
		return http.MethodPost
	case path == "/v1/tasks":
		return http.MethodGet + ", " + http.MethodPost
	case strings.HasPrefix(path, "/v1/tasks/"):
		parts := splitTaskPath(path)
		if len(parts) == 1 {
			return http.MethodGet
		}
		if len(parts) == 2 && (parts[1] == "cancel" || parts[1] == "retry" || parts[1] == "reveal") {
			return http.MethodPost
		}
	}
	return ""
}

func (s *Server) routeV1(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/inspect":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleInspect(w, r)
	case "/v1/tasks":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.tasks.List())
		case http.MethodPost:
			s.handleCreate(w, r)
		default:
			methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	default:
		s.handleTaskRoute(w, r)
	}
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if !decodeStrictJSON(w, r, &input) {
		return
	}
	input.URL = strings.TrimSpace(input.URL)
	if !validInputURL(input.URL) {
		writeError(w, http.StatusBadRequest, "invalid_request", "视频地址格式无效")
		return
	}
	result, err := s.inspector.Inspect(r.Context(), input.URL)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var spec JobSpec
	if !decodeStrictJSON(w, r, &spec) {
		return
	}
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Title = strings.TrimSpace(spec.Title)
	spec.MediaType = strings.ToLower(strings.TrimSpace(spec.MediaType))
	spec.Quality = strings.ToLower(strings.TrimSpace(spec.Quality))
	validMediaQuality := false
	switch spec.MediaType {
	case "mp4", "hls":
		validMediaQuality = spec.Quality == ""
	case "platform":
		validMediaQuality = spec.Quality == "best" || spec.Quality == "1080" || spec.Quality == "720"
	}
	if !validInputURL(spec.URL) || spec.Title == "" || !utf8.ValidString(spec.Title) || utf8.RuneCountInString(spec.Title) > maxTitleRunes || !validMediaQuality {
		writeError(w, http.StatusBadRequest, "invalid_request", "下载任务参数无效")
		return
	}
	task, err := s.tasks.Start(context.WithoutCancel(r.Context()), spec)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func validInputURL(raw string) bool {
	if raw == "" || len(raw) > maxURLBytes || !utf8.ValidString(raw) {
		return false
	}
	parsed, err := url.ParseRequestURI(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
}

func splitTaskPath(path string) []string {
	if !strings.HasPrefix(path, "/v1/tasks/") {
		return nil
	}
	rest := strings.TrimPrefix(path, "/v1/tasks/")
	if rest == "" || strings.HasSuffix(rest, "/") {
		return nil
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" {
		return nil
	}
	return parts
}

func (s *Server) handleTaskRoute(w http.ResponseWriter, r *http.Request) {
	parts := splitTaskPath(r.URL.Path)
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "未找到请求的内容")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		task, err := s.tasks.Get(id)
		if err != nil {
			s.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var task tasks.Task
	var err error
	switch parts[1] {
	case "cancel":
		task, err = s.tasks.Cancel(id)
	case "retry":
		task, err = s.tasks.Retry(context.WithoutCancel(r.Context()), id)
		if err == nil {
			writeJSON(w, http.StatusCreated, task)
			return
		}
	case "reveal":
		s.handleReveal(w, r, id)
		return
	default:
		writeError(w, http.StatusNotFound, "not_found", "未找到请求的内容")
		return
	}
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request, id string) {
	task, err := s.tasks.Get(id)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if task.Status != tasks.Completed || !s.safeOutputPath(task.OutputPath) {
		writeError(w, http.StatusConflict, "not_revealable", "任务文件尚不可显示")
		return
	}
	if err := s.revealer.Reveal(r.Context(), task.OutputPath); err != nil {
		writeError(w, http.StatusInternalServerError, "reveal_failed", "无法在 Finder 中显示文件")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revealed": true})
}

func (s *Server) safeOutputPath(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	rel, err := filepath.Rel(s.downloadDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	realDir, err := filepath.EvalSymlinks(s.downloadDir)
	if err != nil {
		return false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	realRel, err := filepath.Rel(realDir, realPath)
	return err == nil && realRel != "." && realRel != ".." && !strings.HasPrefix(realRel, ".."+string(filepath.Separator)) && !filepath.IsAbs(realRel)
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	var notFound *tasks.NotFoundError
	if errors.As(err, &notFound) {
		writeError(w, http.StatusNotFound, "not_found", "未找到请求的内容")
		return
	}
	var transition *tasks.TransitionError
	if errors.As(err, &transition) {
		writeError(w, http.StatusConflict, "invalid_state", "任务当前状态不允许此操作")
		return
	}
	var inspectErr *InspectError
	if errors.As(err, &inspectErr) {
		status := inspectErr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadRequest
		}
		writeError(w, status, string(inspectErr.Code), inspectErr.Message)
		return
	}
	var platformFFmpegMissing *PlatformFFmpegUnavailableError
	if errors.As(err, &platformFFmpegMissing) {
		writeError(w, http.StatusConflict, "ffmpeg_missing", platformFFmpegMissing.SafeMessage())
		return
	}
	var safe interface{ SafeMessage() string }
	if errors.As(err, &safe) {
		writeError(w, http.StatusConflict, "task_error", safe.SafeMessage())
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "本地助手暂时无法完成此操作")
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "请求必须使用 JSON 格式")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "请求 JSON 格式无效")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "请求 JSON 只能包含一个对象")
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, methods string) {
	w.Header().Set("Allow", methods)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此请求方法")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
