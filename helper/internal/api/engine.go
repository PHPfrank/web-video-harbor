package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"web-video-harbor/helper/internal/download"
	"web-video-harbor/helper/internal/ffmpeg"
	"web-video-harbor/helper/internal/hls"
	"web-video-harbor/helper/internal/media"
	"web-video-harbor/helper/internal/output"
	"web-video-harbor/helper/internal/platformurl"
	"web-video-harbor/helper/internal/safety"
	"web-video-harbor/helper/internal/tasks"
	"web-video-harbor/helper/internal/ytdlp"
)

const maxManifestBytes = 2 * 1024 * 1024

// JobSpec is the minimal, non-sensitive information retained for a task and
// its retries. Browser credentials and request headers are deliberately not
// part of the model.
type JobSpec struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	MediaType string `json:"mediaType"`
	Quality   string `json:"quality,omitempty"`
}

type directDownloader interface {
	Download(context.Context, string, string) (string, error)
}

type hlsRunner interface {
	Run(context.Context, ffmpeg.Request) (string, error)
}

type platformRunner interface {
	Run(context.Context, ytdlp.Request) (string, error)
}

type manifestInspector interface {
	Inspect(context.Context, string) (ManifestInspection, error)
}

// ManifestInspection binds the exact fetched bytes to the final URL after
// redirects. Relative playlist references must be resolved from SourceURL.
type ManifestInspection struct {
	Playlist  *hls.Playlist
	Manifest  []byte
	SourceURL string
}

type engineDeps struct {
	manager                    *tasks.Manager
	inspector                  manifestInspector
	newDownloader              func(download.ProgressFunc) (directDownloader, error)
	newHLSRunner               func(ffmpeg.ProgressFunc) (hlsRunner, error)
	newPlatformRunner          func(ytdlp.ProgressFunc) (platformRunner, error)
	platformUnavailable        bool
	platformFFmpegUnavailable  bool
	platformRuntimeUnavailable bool
}

// Engine owns asynchronous task execution while Manager remains the source of
// truth for public task state.
type Engine struct {
	manager                    *tasks.Manager
	inspector                  manifestInspector
	newDownloader              func(download.ProgressFunc) (directDownloader, error)
	newHLSRunner               func(ffmpeg.ProgressFunc) (hlsRunner, error)
	newPlatformRunner          func(ytdlp.ProgressFunc) (platformRunner, error)
	platformUnavailable        bool
	platformFFmpegUnavailable  bool
	platformRuntimeUnavailable bool

	mu      sync.RWMutex
	specs   map[string]JobSpec
	rootCtx context.Context
	cancel  context.CancelFunc
	closing bool
	wg      sync.WaitGroup
}

// EngineClosedError reports that the service no longer accepts new work.
type EngineClosedError struct{}

func (*EngineClosedError) Error() string { return "task engine is shutting down" }
func (*EngineClosedError) SafeMessage() string {
	return "本地助手正在退出，无法创建新任务"
}

// PlatformDownloaderUnavailableError reports a damaged or incomplete helper
// installation without exposing local bundle paths.
type PlatformDownloaderUnavailableError struct{}

func (*PlatformDownloaderUnavailableError) Error() string {
	return "bundled platform downloader is unavailable"
}
func (*PlatformDownloaderUnavailableError) SafeMessage() string {
	return "安装包缺少平台解析器"
}

// PlatformFFmpegUnavailableError reports that platform downloads cannot run
// without the separately discovered FFmpeg executable.
type PlatformFFmpegUnavailableError struct{}

func (*PlatformFFmpegUnavailableError) Error() string {
	return "FFmpeg is unavailable for platform downloads"
}
func (*PlatformFFmpegUnavailableError) SafeMessage() string {
	return "未安装 FFmpeg，请先安装后重试"
}

// PlatformRuntimeUnavailableError reports an incomplete JavaScript challenge
// runtime without exposing its bundled path.
type PlatformRuntimeUnavailableError struct{}

func (*PlatformRuntimeUnavailableError) Error() string {
	return "bundled JavaScript runtime is unavailable"
}
func (*PlatformRuntimeUnavailableError) SafeMessage() string {
	return "安装包缺少 JavaScript 解析组件"
}

// SpecNotFoundError means the task exists in Manager but was not created by
// this Engine, so retrying it cannot safely reconstruct its media type.
type SpecNotFoundError struct {
	ID string
}

func (e *SpecNotFoundError) Error() string {
	return fmt.Sprintf("job specification for task %q not found", e.ID)
}

// SafeMessage is suitable for authenticated API responses and omits the ID.
func (e *SpecNotFoundError) SafeMessage() string { return "无法重试：缺少原任务信息" }

// ManifestInspector safely fetches and validates one root HLS manifest.
type ManifestInspector struct {
	resolver safety.Resolver
	client   *http.Client
}

// NewManifestInspector constructs a production inspector with a transport
// that validates every DNS answer and redirect before dialing.
func NewManifestInspector(resolver safety.Resolver) *ManifestInspector {
	return newManifestInspector(resolver, &http.Client{
		Transport:     safety.NewSafeTransport(resolver, nil),
		CheckRedirect: safety.SafeRedirectPolicy(resolver),
		Timeout:       30 * time.Second,
	})
}

func newManifestInspector(resolver safety.Resolver, client *http.Client) *ManifestInspector {
	return &ManifestInspector{resolver: resolver, client: client}
}

// Inspect returns the parsed playlist and the exact bytes supplied to the
// FFmpeg runner's independently enforced preflight.
func (i *ManifestInspector) Inspect(ctx context.Context, rawURL string) (ManifestInspection, error) {
	if ctx == nil {
		return ManifestInspection{}, newManifestError("canceled", "操作已取消", context.Canceled)
	}
	if _, err := safety.ValidateRemoteURL(ctx, rawURL, i.resolver); err != nil {
		if ctx.Err() != nil {
			return ManifestInspection{}, newManifestError("canceled", "操作已取消", ctx.Err())
		}
		return ManifestInspection{}, newManifestError("unsafe_source", "视频地址不安全或无效", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ManifestInspection{}, newManifestError("unsafe_source", "视频地址格式无效", err)
	}
	response, err := i.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ManifestInspection{}, newManifestError("canceled", "操作已取消", ctx.Err())
		}
		return ManifestInspection{}, newManifestError("network", "无法读取视频播放列表", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ManifestInspection{}, newManifestError("http_status", "视频播放列表暂时不可用", fmt.Errorf("unexpected HTTP status %d", response.StatusCode))
	}
	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	if media.Classify(finalURL, response.Header.Get("Content-Type")) != media.HLS {
		return ManifestInspection{}, newManifestError("invalid_manifest", "视频地址不是 M3U8 播放列表", errors.New("response content type is not HLS"))
	}
	manifest, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
	if err != nil {
		return ManifestInspection{}, newManifestError("network", "无法读取视频播放列表", err)
	}
	if len(manifest) > maxManifestBytes {
		return ManifestInspection{}, newManifestError("invalid_manifest", "视频播放列表过大", errors.New("root manifest exceeds 2 MiB"))
	}
	playlist, err := hls.ParseBytes(finalURL, manifest)
	if err != nil {
		var playlistError *hls.Error
		if errors.As(err, &playlistError) && playlistError.Code == hls.CodeUnsupportedEncryption {
			return ManifestInspection{}, newManifestError("encrypted_hls", "不支持加密或 DRM 视频", err)
		}
		return ManifestInspection{}, newManifestError("invalid_manifest", "视频播放列表格式无效", err)
	}
	return ManifestInspection{Playlist: playlist, Manifest: manifest, SourceURL: finalURL}, nil
}

type manifestError struct {
	code    string
	message string
	cause   error
}

func (e *manifestError) Error() string { return e.message }
func (e *manifestError) Unwrap() error { return e.cause }

func newManifestError(code, message string, cause error) error {
	return &manifestError{code: code, message: message, cause: cause}
}

func newEngine(deps engineDeps) (*Engine, error) {
	if deps.manager == nil {
		return nil, errors.New("task manager is required")
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	return &Engine{
		manager:                    deps.manager,
		inspector:                  deps.inspector,
		newDownloader:              deps.newDownloader,
		newHLSRunner:               deps.newHLSRunner,
		newPlatformRunner:          deps.newPlatformRunner,
		platformUnavailable:        deps.platformUnavailable,
		platformFFmpegUnavailable:  deps.platformFFmpegUnavailable,
		platformRuntimeUnavailable: deps.platformRuntimeUnavailable,
		specs:                      make(map[string]JobSpec),
		rootCtx:                    rootCtx,
		cancel:                     cancel,
	}, nil
}

// NewEngine constructs a production engine. A new downloader or FFmpeg runner
// is created for every attempt.
func NewEngine(manager *tasks.Manager, outputDir string, resolver safety.Resolver, ffmpegPath string, platform ytdlp.ProbeResult, runtime ytdlp.RuntimeResult) (*Engine, error) {
	deps := engineDeps{
		manager:   manager,
		inspector: NewManifestInspector(resolver),
		newDownloader: func(progress download.ProgressFunc) (directDownloader, error) {
			return download.New(download.Config{
				OutputDir:  outputDir,
				Resolver:   resolver,
				OnProgress: progress,
			})
		},
		newHLSRunner: func(progress ffmpeg.ProgressFunc) (hlsRunner, error) {
			return ffmpeg.New(ffmpeg.Config{
				OutputDir:  outputDir,
				Resolver:   resolver,
				FFmpegPath: ffmpegPath,
				OnProgress: progress,
			})
		},
	}
	if platform.Path == "" {
		deps.platformUnavailable = true
	} else if ffmpegPath == "" {
		deps.platformFFmpegUnavailable = true
	} else if platform.Snapshot == nil || platform.Path != platform.Snapshot.Path() || platform.Snapshot.Verify() != nil {
		deps.platformUnavailable = true
	} else if runtime.Snapshot == nil || runtime.Path == "" || runtime.Path != runtime.Snapshot.Path() || runtime.Snapshot.Verify() != nil {
		deps.platformRuntimeUnavailable = true
	} else {
		deps.newPlatformRunner = func(progress ytdlp.ProgressFunc) (platformRunner, error) {
			return ytdlp.New(ytdlp.Config{
				BinaryPath:         platform.Path,
				RuntimePath:        runtime.Path,
				FFmpegPath:         ffmpegPath,
				OutputDir:          outputDir,
				OnProgress:         progress,
				ExecutableSnapshot: platform.Snapshot,
				RuntimeSnapshot:    runtime.Snapshot,
			})
		}
	}
	return newEngine(deps)
}

// Start records a queued attempt and starts its worker asynchronously.
func (e *Engine) Start(ctx context.Context, spec JobSpec) (tasks.Task, error) {
	if ctx == nil {
		return tasks.Task{}, errors.New("start task: nil context")
	}
	if spec.MediaType != "mp4" && spec.MediaType != "hls" && spec.MediaType != "platform" {
		return tasks.Task{}, fmt.Errorf("unsupported media type %q", spec.MediaType)
	}
	if strings.TrimSpace(spec.URL) == "" {
		return tasks.Task{}, errors.New("video URL is required")
	}
	if spec.MediaType == "platform" {
		switch ytdlp.Quality(spec.Quality) {
		case ytdlp.QualityBest, ytdlp.Quality1080, ytdlp.Quality720:
		default:
			return tasks.Task{}, fmt.Errorf("unsupported platform quality %q", spec.Quality)
		}
		video, err := platformurl.Classify(spec.URL)
		if err != nil {
			return tasks.Task{}, err
		}
		spec.URL = video.CanonicalURL
		if e.platformUnavailable {
			return tasks.Task{}, &PlatformDownloaderUnavailableError{}
		}
		if e.platformFFmpegUnavailable {
			return tasks.Task{}, &PlatformFFmpegUnavailableError{}
		}
		if e.platformRuntimeUnavailable {
			return tasks.Task{}, &PlatformRuntimeUnavailableError{}
		}
	}
	if err := ctx.Err(); err != nil {
		return tasks.Task{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return tasks.Task{}, &EngineClosedError{}
	}
	task, err := e.manager.CreateWithContext(e.rootCtx, spec.URL, spec.Title)
	if err != nil {
		return tasks.Task{}, err
	}
	e.specs[task.ID] = spec
	if task.Status == tasks.Queued {
		e.wg.Add(1)
		go e.run(task.ID, spec)
	}
	return task, nil
}

func (e *Engine) run(id string, spec JobSpec) {
	defer e.wg.Done()
	ctx, err := e.manager.Context(id)
	if err != nil {
		return
	}
	defer func() {
		if ctx.Err() != nil {
			_, _ = e.manager.Cancel(id)
		}
	}()
	if ctx.Err() != nil {
		return
	}
	if _, err := e.manager.Transition(id, tasks.Downloading); err != nil {
		return
	}

	switch spec.MediaType {
	case "mp4":
		e.runMP4(ctx, id, spec)
	case "hls":
		e.runHLS(ctx, id, spec)
	case "platform":
		e.runPlatform(ctx, id, spec)
	}
}

func (e *Engine) runPlatform(ctx context.Context, id string, spec JobSpec) {
	if e.newPlatformRunner == nil {
		e.fail(id, errors.New("platform runner factory is unavailable"))
		return
	}
	runner, err := e.newPlatformRunner(func(progress ytdlp.Progress) {
		percent := progress.Percent
		if math.IsNaN(percent) || math.IsInf(percent, 0) {
			return
		}
		if percent < 0 {
			percent = 0
		}
		if percent > 99 {
			percent = 99
		}
		_, _ = e.manager.SetProgress(id, percent)
		if percent == 99 {
			_, _ = e.manager.Transition(id, tasks.Merging)
		}
	})
	if err != nil {
		e.fail(id, err)
		return
	}
	if runner == nil {
		e.fail(id, errors.New("platform runner factory returned nil"))
		return
	}
	path, err := runner.Run(ctx, ytdlp.Request{
		URL:     spec.URL,
		Title:   spec.Title,
		Quality: ytdlp.Quality(spec.Quality),
	})
	if publishedPath, ok := output.PublishedPath(err); ok {
		path = publishedPath
		err = nil
	}
	if err != nil {
		if ctx.Err() == nil {
			e.fail(id, err)
		}
		return
	}
	if path == "" {
		e.fail(id, errors.New("platform runner returned an empty output path"))
		return
	}
	active, getErr := e.manager.Get(id)
	if getErr != nil {
		return
	}
	if active.Status == tasks.Downloading {
		if _, transitionErr := e.manager.Transition(id, tasks.Merging); transitionErr != nil {
			return
		}
	}
	if _, err := e.manager.CompletePublished(id, path); err != nil {
		e.fail(id, fmt.Errorf("record published platform output: %w", err))
	}
}

func (e *Engine) runHLS(ctx context.Context, id string, spec JobSpec) {
	if e.inspector == nil {
		e.fail(id, errors.New("manifest inspector is unavailable"))
		return
	}
	inspection, err := e.inspector.Inspect(ctx, spec.URL)
	if err != nil {
		if ctx.Err() == nil {
			e.fail(id, err)
		}
		return
	}
	if _, err := e.manager.Transition(id, tasks.Merging); err != nil {
		return
	}
	if e.newHLSRunner == nil {
		e.fail(id, errors.New("HLS runner factory is unavailable"))
		return
	}
	runner, err := e.newHLSRunner(func(progress ffmpeg.Progress) {
		if progress.Done {
			_, _ = e.manager.SetProgress(id, 99)
		}
	})
	if err != nil {
		e.fail(id, err)
		return
	}
	path, err := runner.Run(ctx, ffmpeg.Request{
		SourceURL: inspection.SourceURL,
		Title:     spec.Title,
		Manifest:  append([]byte(nil), inspection.Manifest...),
	})
	if publishedPath, ok := output.PublishedPath(err); ok {
		path = publishedPath
		err = nil
	}
	if err != nil {
		if ctx.Err() == nil {
			e.fail(id, err)
		}
		return
	}
	if _, err := e.manager.CompletePublished(id, path); err != nil {
		e.fail(id, fmt.Errorf("record published HLS output: %w", err))
	}
}

func (e *Engine) runMP4(ctx context.Context, id string, spec JobSpec) {
	if e.newDownloader == nil {
		e.fail(id, errors.New("direct downloader factory is unavailable"))
		return
	}
	downloader, err := e.newDownloader(func(progress download.Progress) {
		if progress.TotalBytes <= 0 {
			return
		}
		percent := float64(progress.DownloadedBytes) * 100 / float64(progress.TotalBytes)
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		_, _ = e.manager.SetProgress(id, percent)
	})
	if err != nil {
		e.fail(id, err)
		return
	}
	path, err := downloader.Download(ctx, spec.URL, spec.Title)
	if publishedPath, ok := output.PublishedPath(err); ok {
		path = publishedPath
		err = nil
	}
	if err != nil {
		if ctx.Err() == nil {
			e.fail(id, err)
		}
		return
	}
	if _, err := e.manager.CompletePublished(id, path); err != nil {
		e.fail(id, fmt.Errorf("record published direct output: %w", err))
	}
}

func (e *Engine) fail(id string, internal error) {
	code, message := safeFailure(internal)
	_, _ = e.manager.FailWithCode(id, code, message, internal)
}

func safeFailure(internal error) (string, string) {
	var platformErr *ytdlp.Error
	if errors.As(internal, &platformErr) {
		switch platformErr.Code {
		case ytdlp.CodeCanceled:
			return "canceled", "下载已取消"
		case ytdlp.CodeLoginRequired:
			return "login_required", "当前视频需要登录，v0.2.1 暂不支持"
		case ytdlp.CodeVerificationRequired:
			return "verification_required", "YouTube 要求浏览器验证；为保护账号隐私，网页视频港不会读取登录信息"
		case ytdlp.CodeAccessLimited:
			return "access_limited", "当前内容受会员、付费或私有访问限制"
		case ytdlp.CodeGeoRestricted:
			return "geo_restricted", "当前网络所在地区无法访问此视频"
		case ytdlp.CodeExtractor:
			return "extractor_outdated", "平台解析规则已变化，请升级网页视频港"
		case ytdlp.CodeFFmpegMissing:
			return "ffmpeg_missing", "未安装 FFmpeg，请先安装后重试"
		case ytdlp.CodeNetwork:
			return "network", "网络连接失败，请稍后重试"
		case ytdlp.CodeNetworkFiltered:
			return "network_filtered", "当前网络阻止了本地下载连接，请联系网络管理员或更换网络"
		case ytdlp.CodeJavaScriptRuntime:
			return "javascript_runtime", "视频解析组件不完整，请重新安装网页视频港"
		case ytdlp.CodeOutput:
			return "output", "无法保存视频文件"
		case ytdlp.CodeProcess:
			return "platform_process", "平台暂时拒绝了下载，请稍后重试"
		}
	}
	var downloadErr *download.Error
	if errors.As(internal, &downloadErr) {
		switch downloadErr.Code {
		case download.CodeCanceled:
			return "canceled", "下载已取消"
		case download.CodeUnsafeSource:
			return "unsafe_source", "视频下载地址不安全或无效"
		case download.CodeHTTPStatus:
			return "http_status", "视频服务器拒绝了下载请求"
		case download.CodeNetwork:
			return "network", "网络连接失败，请稍后重试"
		case download.CodeTransfer:
			return "transfer", "视频传输中断，请稍后重试"
		case download.CodeOutput:
			return "output", "无法保存视频文件"
		}
	}
	var ffmpegErr *ffmpeg.Error
	if errors.As(internal, &ffmpegErr) {
		switch ffmpegErr.Code {
		case ffmpeg.CodeCanceled:
			return "canceled", "下载已取消"
		case ffmpeg.CodeEncrypted:
			return "encrypted_hls", "不支持加密或 DRM 视频"
		case ffmpeg.CodeFFmpegMissing:
			return "ffmpeg_missing", "未安装 FFmpeg，请先安装后重试"
		case ffmpeg.CodeUnsafeSource:
			return "unsafe_source", "视频下载地址不安全或无效"
		case ffmpeg.CodeManifest:
			return "invalid_manifest", "视频播放列表格式无效"
		case ffmpeg.CodeProcess:
			return "ffmpeg_process", "FFmpeg 合并视频失败"
		case ffmpeg.CodeProgress:
			return "progress_output", "无法读取 FFmpeg 进度"
		case ffmpeg.CodeOutput:
			return "output", "无法保存视频文件"
		}
	}
	var manifestErr *manifestError
	if errors.As(internal, &manifestErr) {
		switch manifestErr.code {
		case "canceled":
			return "canceled", "下载已取消"
		case "encrypted_hls":
			return "encrypted_hls", "不支持加密或 DRM 视频"
		case "unsafe_source":
			return "unsafe_source", "视频下载地址不安全或无效"
		case "http_status":
			return "http_status", "视频播放列表暂时不可用"
		case "network":
			return "network", "无法读取视频播放列表"
		case "invalid_manifest":
			return "invalid_manifest", "视频播放列表格式无效"
		}
	}
	return "download_failed", "视频下载失败，请稍后重试"
}

// Get returns the current public task state.
func (e *Engine) Get(id string) (tasks.Task, error) { return e.manager.Get(id) }

// List returns all attempts in creation order.
func (e *Engine) List() []tasks.Task { return e.manager.List() }

// Cancel cancels the manager-owned context observed by the active worker.
func (e *Engine) Cancel(id string) (tasks.Task, error) { return e.manager.Cancel(id) }

// Retry starts a fresh attempt using only the non-sensitive spec saved by
// Start. Tasks created outside this Engine are deliberately not reconstructible.
func (e *Engine) Retry(ctx context.Context, id string) (tasks.Task, error) {
	if ctx == nil {
		return tasks.Task{}, errors.New("retry task: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tasks.Task{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return tasks.Task{}, &EngineClosedError{}
	}
	source, err := e.manager.Get(id)
	if err != nil {
		return tasks.Task{}, err
	}
	spec, ok := e.specs[id]
	if !ok {
		return tasks.Task{}, &SpecNotFoundError{ID: id}
	}
	if source.Status != tasks.Failed && source.Status != tasks.Canceled {
		return tasks.Task{}, &tasks.TransitionError{ID: id, From: source.Status, To: tasks.Queued}
	}

	retry, err := e.manager.CreateWithContext(e.rootCtx, spec.URL, spec.Title)
	if err != nil {
		return tasks.Task{}, err
	}
	e.specs[retry.ID] = spec
	if retry.Status == tasks.Queued {
		e.wg.Add(1)
		go e.run(retry.ID, spec)
	}
	return retry, nil
}

// Shutdown atomically rejects new attempts, cancels active work, and waits for
// every worker to finish its downloader/runner cleanup.
func (e *Engine) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown task engine: nil context")
	}
	e.mu.Lock()
	if !e.closing {
		e.closing = true
		e.cancel()
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
