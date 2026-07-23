// Package ffmpeg remuxes caller-preflighted HLS streams into MP4 files.
package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/hlsproxy"
	"web-video-downloader/helper/internal/output"
	"web-video-downloader/helper/internal/safety"
)

const (
	maxStderrBytes       = 8 * 1024
	maxProgressTokenSize = 256 * 1024
	maxFilenameBytes     = 255
	protocolWhitelist    = "http,tcp"
	proxyCloseTimeout    = 5 * time.Second
)

// Code identifies a stable runner failure category.
type Code string

const (
	CodeCanceled      Code = "canceled"
	CodeUnsafeSource  Code = "unsafe_source"
	CodeManifest      Code = "invalid_manifest"
	CodeEncrypted     Code = "encrypted_hls"
	CodeFFmpegMissing Code = "ffmpeg_missing"
	CodeProcess       Code = "ffmpeg_process"
	CodeProgress      Code = "progress_output"
	CodeOutput        Code = "output"
)

// Error exposes a safe Chinese message. Stderr contains at most the bounded,
// redacted diagnostic tail and never intentionally includes SourceURL.
type Error struct {
	Code    Code
	Message string
	Stderr  string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Progress is one complete record from FFmpeg's -progress output.
type Progress struct {
	OutTime   time.Duration
	TotalSize int64
	Speed     string
	Done      bool
}

type ProgressFunc func(Progress)

// Request contains a selected HLS source and the exact manifest bytes already
// fetched by a safe caller-controlled preflight step. Manifest is mandatory so
// encryption inspection cannot be accidentally skipped.
type Request struct {
	SourceURL string
	Title     string
	Manifest  []byte
}

// Config contains production runner settings.
type Config struct {
	OutputDir  string
	Resolver   safety.Resolver
	FFmpegPath string
	OnProgress ProgressFunc
}

type internalConfig struct {
	outputDir      string
	resolver       safety.Resolver
	ffmpegPath     string
	onProgress     ProgressFunc
	commandFactory commandFactory
	progressParser progressParserFunc
	proxyFactory   proxyFactory
}

// Runner starts an FFmpeg process for each Run call and serializes progress
// callback invocations, including when callers run multiple requests at once.
type Runner struct {
	outputDir           string
	resolver            safety.Resolver
	ffmpegPath          string
	onProgress          ProgressFunc
	commandFactory      commandFactory
	progressParser      progressParserFunc
	proxyFactory        proxyFactory
	syncOutputDirectory func(string) error
	progressMu          sync.Mutex
}

// New constructs a production runner backed by exec.CommandContext.
func New(config Config) (*Runner, error) {
	return newRunner(internalConfig{
		outputDir:  config.OutputDir,
		resolver:   config.Resolver,
		ffmpegPath: config.FFmpegPath,
		onProgress: config.OnProgress,
	})
}

func newRunner(config internalConfig) (*Runner, error) {
	absDir, err := filepath.Abs(config.outputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve output directory: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("output path is not a directory")
	}
	ffmpegPath := config.ffmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	factory := config.commandFactory
	if factory == nil {
		factory = productionCommand
	}
	parser := config.progressParser
	if parser == nil {
		parser = parseProgress
	}
	proxyFactory := config.proxyFactory
	if proxyFactory == nil {
		proxyFactory = productionProxyFactory
	}
	return &Runner{
		outputDir:           absDir,
		resolver:            config.resolver,
		ffmpegPath:          ffmpegPath,
		onProgress:          config.onProgress,
		commandFactory:      factory,
		progressParser:      parser,
		proxyFactory:        proxyFactory,
		syncOutputDirectory: syncDirectory,
	}, nil
}

type command interface {
	StdoutPipe() (io.ReadCloser, error)
	SetStderr(io.Writer)
	SetEnv([]string)
	Start() error
	Wait() error
}

type commandFactory func(context.Context, string, ...string) command
type progressParserFunc func(io.Reader, ProgressFunc) error

type hlsProxy interface {
	URL() string
	Err() error
	Close(context.Context) error
}

type proxyFactory func(context.Context, hlsproxy.Config) (hlsProxy, error)

func productionProxyFactory(ctx context.Context, config hlsproxy.Config) (hlsProxy, error) {
	return hlsproxy.Start(ctx, config)
}

type execCommand struct{ cmd *exec.Cmd }

func productionCommand(ctx context.Context, name string, args ...string) command {
	return &execCommand{cmd: exec.CommandContext(ctx, name, args...)}
}

func (c *execCommand) StdoutPipe() (io.ReadCloser, error) { return c.cmd.StdoutPipe() }
func (c *execCommand) SetStderr(writer io.Writer)         { c.cmd.Stderr = writer }
func (c *execCommand) SetEnv(env []string)                { c.cmd.Env = env }
func (c *execCommand) Start() error                       { return c.cmd.Start() }
func (c *execCommand) Wait() error                        { return c.cmd.Wait() }

// Run remuxes an unencrypted HLS source to a new MP4 file. FFmpeg receives only
// an opaque loopback URL; all remote HLS requests remain under the safe Go
// transport owned by the helper.
func (r *Runner) Run(ctx context.Context, request Request) (path string, returnErr error) {
	if ctx == nil {
		return "", canceledError(context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return "", canceledError(err)
	}
	if len(request.Manifest) == 0 {
		return "", manifestError(errors.New("preflight manifest is required"))
	}
	if _, err := hls.ParseBytes(request.SourceURL, request.Manifest); err != nil {
		var playlistErr *hls.Error
		if errors.As(err, &playlistErr) && playlistErr.Code == hls.CodeUnsupportedEncryption {
			return "", &Error{Code: CodeEncrypted, Message: "不支持加密或 DRM 视频", cause: errors.New("HLS encryption is unsupported")}
		}
		return "", manifestError(errors.New("HLS preflight failed"))
	}
	if _, err := safety.ValidateRemoteURL(ctx, request.SourceURL, r.resolver); err != nil {
		if ctx.Err() != nil {
			return "", canceledError(ctx.Err())
		}
		return "", &Error{Code: CodeUnsafeSource, Message: "视频下载地址不安全或无效", cause: safeValidationDiagnostic(err)}
	}
	proxy, err := r.proxyFactory(ctx, hlsproxy.Config{
		SourceURL: request.SourceURL,
		Manifest:  request.Manifest,
		Resolver:  r.resolver,
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", canceledError(ctx.Err())
		}
		return "", mapProxyError(err)
	}
	var proxyCloseOnce sync.Once
	var proxyCloseErr error
	closeProxy := func() error {
		proxyCloseOnce.Do(func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), proxyCloseTimeout)
			proxyCloseErr = proxy.Close(closeCtx)
			cancel()
		})
		return proxyCloseErr
	}
	defer func() {
		closeErr := closeProxy()
		if closeErr != nil && returnErr == nil {
			path = ""
			returnErr = &Error{Code: CodeProcess, Message: "无法关闭安全视频代理", cause: errors.New("close HLS proxy failed")}
		}
	}()

	stagingDir, err := os.MkdirTemp(r.outputDir, ".web-video-ffmpeg-*")
	if err != nil {
		return "", outputError("无法创建视频暂存目录", err)
	}
	stagingInfo, err := os.Lstat(stagingDir)
	if err != nil {
		_ = os.Remove(stagingDir)
		return "", outputError("无法检查视频暂存目录", err)
	}
	partPath := filepath.Join(stagingDir, "output.part")
	defer func() {
		cleanupErr := cleanupStaging(stagingDir, partPath, stagingInfo)
		if cleanupErr != nil && returnErr == nil {
			if path != "" {
				returnErr = output.NewPublishedError(path, outputError("无法清理临时视频文件", cleanupErr))
			} else {
				returnErr = outputError("无法清理临时视频文件", cleanupErr)
			}
		}
	}()

	args := []string{
		"-protocol_whitelist", protocolWhitelist,
		"-nostdin", "-y", "-i", proxy.URL(),
		"-map", "0", "-c", "copy", "-movflags", "+faststart",
		"-progress", "pipe:1", "-nostats", "-f", "mp4", partPath,
	}
	cmd := r.commandFactory(ctx, r.ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", &Error{Code: CodeProcess, Message: "无法读取 FFmpeg 进度", cause: errors.New("create FFmpeg stdout pipe failed")}
	}
	stderr := newRedactingTailBuffer(maxStderrBytes)
	cmd.SetStderr(stderr)
	cmd.SetEnv(sanitizedEnvironment(os.Environ()))
	if err := cmd.Start(); err != nil {
		closeErr := stdout.Close()
		if ctx.Err() != nil {
			return "", canceledError(ctx.Err())
		}
		if isMissingExecutable(err) {
			return "", &Error{Code: CodeFFmpegMissing, Message: "未安装 FFmpeg", cause: errors.New("FFmpeg executable not found")}
		}
		return "", &Error{Code: CodeProcess, Message: "无法启动 FFmpeg", cause: errors.Join(errors.New("start FFmpeg failed"), normalizeCloseError(closeErr))}
	}

	parseDone := make(chan error, 1)
	go func() {
		parseDone <- r.progressParser(stdout, r.reportProgress)
	}()
	parseErr := <-parseDone
	waitErr := cmd.Wait()
	closeErr := normalizeCloseError(stdout.Close())

	if ctx.Err() != nil {
		return "", canceledError(ctx.Err())
	}
	if parseErr != nil {
		return "", &Error{Code: CodeProgress, Message: "无法读取 FFmpeg 进度", cause: errors.Join(parseErr, closeErr)}
	}
	if proxyErr := proxy.Err(); proxyErr != nil {
		return "", mapProxyError(proxyErr)
	}
	if waitErr != nil {
		return "", &Error{
			Code:    CodeProcess,
			Message: "FFmpeg 合并视频失败",
			Stderr:  stderr.String(),
			cause:   errors.New("FFmpeg exited unsuccessfully"),
		}
	}
	if closeErr != nil {
		return "", &Error{Code: CodeProgress, Message: "无法读取 FFmpeg 进度", cause: closeErr}
	}
	if err := closeProxy(); err != nil {
		return "", &Error{Code: CodeProcess, Message: "无法关闭安全视频代理", cause: errors.New("close HLS proxy failed")}
	}
	if proxyErr := proxy.Err(); proxyErr != nil {
		return "", mapProxyError(proxyErr)
	}
	if err := syncAndClosePart(partPath); err != nil {
		return "", outputError("无法保存合并后的视频", err)
	}
	published, err := publishNoReplace(partPath, r.outputDir, request.Title)
	if err != nil {
		return "", outputError("无法保存合并后的视频", err)
	}
	if err := r.syncOutputDirectory(r.outputDir); err != nil {
		return published, output.NewPublishedError(published, outputError("无法确认视频文件已保存", err))
	}
	return published, nil
}

func mapProxyError(err error) error {
	var proxyErr *hlsproxy.Error
	if !errors.As(err, &proxyErr) {
		return &Error{Code: CodeProcess, Message: "安全视频代理失败", cause: errors.New("HLS proxy failed")}
	}
	switch proxyErr.Code {
	case hlsproxy.CodeUnsafeSource:
		return &Error{Code: CodeUnsafeSource, Message: "视频下载地址不安全或无效", cause: errors.New("HLS proxy rejected unsafe source")}
	case hlsproxy.CodeEncrypted:
		return &Error{Code: CodeEncrypted, Message: "不支持加密或 DRM 视频", cause: errors.New("HLS proxy rejected encrypted stream")}
	case hlsproxy.CodeManifest, hlsproxy.CodeTooLarge, hlsproxy.CodeResourceLimit:
		return manifestError(errors.New("HLS proxy rejected invalid manifest"))
	case hlsproxy.CodeCanceled:
		return canceledError(context.Canceled)
	default:
		return &Error{Code: CodeProcess, Message: "安全视频代理失败", cause: errors.New("HLS proxy failed")}
	}
}

func sanitizedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "ffreport":
			continue
		default:
			result = append(result, entry)
		}
	}
	return result
}

func (r *Runner) reportProgress(progress Progress) {
	if r.onProgress == nil {
		return
	}
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	r.onProgress(progress)
}

func parseProgress(reader io.Reader, callback ProgressFunc) error {
	if reader == nil {
		return errors.New("progress reader is nil")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024), maxProgressTokenSize)
	var record Progress
	var hasOutTimeUS bool
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "out_time_us":
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 && parsed <= math.MaxInt64/int64(time.Microsecond) {
				record.OutTime = time.Duration(parsed) * time.Microsecond
				hasOutTimeUS = true
			}
		case "out_time_ms":
			if !hasOutTimeUS {
				if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 && parsed <= math.MaxInt64/int64(time.Microsecond) {
					record.OutTime = time.Duration(parsed) * time.Microsecond
				}
			}
		case "total_size":
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 {
				record.TotalSize = parsed
			}
		case "speed":
			record.Speed = value
		case "progress":
			if value != "continue" && value != "end" {
				continue
			}
			record.Done = value == "end"
			if callback != nil {
				callback(record)
			}
			record = Progress{}
			hasOutTimeUS = false
		}
	}
	parseErr := scanner.Err()
	if parseErr == nil {
		return nil
	}
	closer, ok := reader.(io.Closer)
	if !ok {
		return parseErr
	}
	return errors.Join(parseErr, normalizeCloseError(closer.Close()))
}

type tailBuffer struct {
	limit int
	data  []byte
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if b.limit <= 0 {
		return written, nil
	}
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return written, nil
	}
	overflow := len(b.data) + len(data) - b.limit
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return written, nil
}

func (b *tailBuffer) String() string { return string(b.data) }

// redactingTailBuffer removes every HTTP(S) URL before storing a bounded tail.
// It is a small streaming state machine so URLs and their prefixes may span
// arbitrary Write calls without requiring an unbounded pre-redaction buffer.
type redactingTailBuffer struct {
	tail    tailBuffer
	pending []byte
	inURL   bool
}

func newRedactingTailBuffer(limit int) *redactingTailBuffer {
	return &redactingTailBuffer{tail: tailBuffer{limit: limit}}
}

func (b *redactingTailBuffer) Write(data []byte) (int, error) {
	for _, value := range data {
		b.consume(value)
	}
	return len(data), nil
}

func (b *redactingTailBuffer) consume(value byte) {
	if b.inURL {
		if isURLTerminator(value) {
			b.inURL = false
			b.consume(value)
		}
		return
	}

	b.pending = append(b.pending, value)
	for len(b.pending) > 0 {
		matched, prefix := matchesURLPrefix(b.pending)
		if matched {
			_, _ = b.tail.Write([]byte("[视频地址]"))
			b.pending = b.pending[:0]
			b.inURL = true
			return
		}
		if prefix {
			return
		}
		_, _ = b.tail.Write(b.pending[:1])
		b.pending = b.pending[1:]
	}
}

func (b *redactingTailBuffer) String() string {
	copyOfTail := tailBuffer{limit: b.tail.limit, data: append([]byte(nil), b.tail.data...)}
	if !b.inURL && len(b.pending) > 0 {
		_, _ = copyOfTail.Write(b.pending)
	}
	return copyOfTail.String()
}

func matchesURLPrefix(candidate []byte) (matched, prefix bool) {
	for _, scheme := range []string{"http://", "https://"} {
		if len(candidate) > len(scheme) {
			continue
		}
		matches := true
		for index, value := range candidate {
			if asciiLower(value) != scheme[index] {
				matches = false
				break
			}
		}
		if matches {
			return len(candidate) == len(scheme), true
		}
	}
	return false, false
}

func asciiLower(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func isURLTerminator(value byte) bool {
	if value <= ' ' || value == 0x7f {
		return true
	}
	return strings.ContainsRune("\"`<>{}", rune(value))
}

func syncAndClosePart(path string) error {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect FFmpeg output: %w", err)
	}
	if !pathInfo.Mode().IsRegular() {
		return errors.New("FFmpeg output is not a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open FFmpeg output: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(pathInfo, openedInfo) {
		closeErr := file.Close()
		if statErr == nil {
			statErr = errors.New("FFmpeg output changed before publication")
		}
		return errors.Join(statErr, closeErr)
	}
	chmodErr := file.Chmod(0o600)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(chmodErr, syncErr, closeErr)
}

func publishNoReplace(partPath, dir, title string) (string, error) {
	base := output.SanitizeBaseName(title)
	for index := 1; ; index++ {
		suffix := ""
		if index > 1 {
			suffix = fmt.Sprintf(" (%d)", index)
		}
		budget := maxFilenameBytes - len(suffix) - len(".mp4")
		candidate := filepath.Join(dir, truncateUTF8(base, budget)+suffix+".mp4")
		err := os.Link(partPath, candidate)
		switch {
		case err == nil:
			return candidate, nil
		case errors.Is(err, os.ErrExist):
			continue
		default:
			return "", fmt.Errorf("atomically publish FFmpeg output: %w", err)
		}
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	value = strings.TrimRight(value, " .-")
	if value == "" {
		return "视频"
	}
	return value
}

func cleanupStaging(dir, partPath string, ownedDir os.FileInfo) error {
	currentDir, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect staging directory: %w", err)
	}
	if !os.SameFile(ownedDir, currentDir) {
		return errors.New("staging path no longer names the owned directory")
	}
	if err := os.Remove(partPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private part file: %w", err)
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("remove private staging directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func normalizeCloseError(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func isMissingExecutable(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

func canceledError(cause error) *Error {
	if cause == nil {
		cause = context.Canceled
	}
	return &Error{Code: CodeCanceled, Message: "下载已取消", cause: cause}
}

func manifestError(cause error) *Error {
	return &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: cause}
}

func outputError(message string, cause error) *Error {
	return &Error{Code: CodeOutput, Message: message, cause: cause}
}

func safeValidationDiagnostic(err error) error {
	var validationErr *safety.ValidationError
	if errors.As(err, &validationErr) {
		return fmt.Errorf("source validation failed (%s)", validationErr.Code)
	}
	return errors.New("source validation failed")
}
