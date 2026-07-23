// Package download streams video resources to safe, atomically published
// output files.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"web-video-downloader/helper/internal/output"
	"web-video-downloader/helper/internal/safety"
)

const (
	defaultAttempts  = 3
	copyBufferSize   = 64 * 1024
	maxFileNameBytes = 255
	maxRetryAfter    = 5 * time.Minute
)

// Code is a stable download failure category for API consumers.
type Code string

const (
	CodeCanceled     Code = "canceled"
	CodeUnsafeSource Code = "unsafe_source"
	CodeHTTPStatus   Code = "http_status"
	CodeNetwork      Code = "network"
	CodeTransfer     Code = "transfer"
	CodeOutput       Code = "output"
)

// Error exposes a safe Chinese message without including a source URL. Cause
// intentionally contains only URL-free diagnostics or context cancellation.
type Error struct {
	Code       Code
	Message    string
	StatusCode int
	cause      error
	retryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Progress is reported after bytes have been written to disk. TotalBytes is
// zero when the server did not provide a Content-Length.
type Progress struct {
	DownloadedBytes int64
	TotalBytes      int64
}

type ProgressFunc func(Progress)
type SleepFunc func(context.Context, time.Duration) error

// RetryPolicy counts the initial request in MaxAttempts. Backoff receives the
// just-failed one-based attempt number.
type RetryPolicy struct {
	MaxAttempts int
	Backoff     func(failedAttempt int) time.Duration
}

// Config contains production-safe downloader settings. HTTP transport and URL
// validation are always installed by exported constructors and cannot be
// replaced through this configuration.
type Config struct {
	OutputDir  string
	Resolver   safety.Resolver
	Retry      RetryPolicy
	OnProgress ProgressFunc
}

type internalConfig struct {
	client       *http.Client
	outputDir    string
	resolver     safety.Resolver
	retry        RetryPolicy
	onProgress   ProgressFunc
	sleep        SleepFunc
	skipURLCheck bool
}

type Downloader struct {
	client       *http.Client
	outputDir    string
	resolver     safety.Resolver
	retry        RetryPolicy
	onProgress   ProgressFunc
	sleep        SleepFunc
	now          func() time.Time
	validateURLs bool
	partWriter   func(io.Writer) io.Writer
}

// New constructs a production downloader with a safe transport and redirect
// policy. Test-only dependency injection remains package-private.
func New(config Config) (*Downloader, error) {
	return newDownloader(internalConfig{
		client:     newSafeHTTPClient(config.Resolver, nil),
		outputDir:  config.OutputDir,
		resolver:   config.Resolver,
		retry:      config.Retry,
		onProgress: config.OnProgress,
	})
}

func newDownloader(config internalConfig) (*Downloader, error) {
	if config.client == nil {
		return nil, errors.New("download client is required")
	}
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

	retry := config.retry
	if retry.MaxAttempts == 0 {
		retry.MaxAttempts = defaultAttempts
	}
	if retry.MaxAttempts < 1 {
		return nil, errors.New("retry attempts must be at least one")
	}
	if retry.Backoff == nil {
		retry.Backoff = defaultBackoff
	}
	sleep := config.sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := time.Now

	return &Downloader{
		client:       config.client,
		outputDir:    absDir,
		resolver:     config.resolver,
		retry:        retry,
		onProgress:   config.onProgress,
		sleep:        sleep,
		now:          now,
		validateURLs: !config.skipURLCheck,
		partWriter:   identityWriter,
	}, nil
}

// NewSafe constructs the production downloader. Target validation occurs once
// before every request attempt, while the safe transport validates the DNS
// answer actually used for every dial. Redirect targets are validated on every
// hop, avoiding a validation-then-default-dial DNS rebinding gap.
func NewSafe(outputDir string, resolver safety.Resolver, onProgress ProgressFunc) (*Downloader, error) {
	return New(Config{
		OutputDir:  outputDir,
		Resolver:   resolver,
		OnProgress: onProgress,
	})
}

func newSafeHTTPClient(resolver safety.Resolver, dialer safety.ContextDialer) *http.Client {
	return &http.Client{
		Transport:     safety.NewSafeTransport(resolver, dialer),
		CheckRedirect: safety.SafeRedirectPolicy(resolver),
	}
}

// Download streams rawURL into a private part file and atomically publishes a
// unique MP4 output after the complete body has been synchronized to disk.
func (d *Downloader) Download(ctx context.Context, rawURL, title string) (path string, returnErr error) {
	if ctx == nil {
		return "", &Error{Code: CodeCanceled, Message: "下载已取消", cause: context.Canceled}
	}
	stagingDir, err := os.MkdirTemp(d.outputDir, ".web-video-*")
	if err != nil {
		return "", outputError("无法创建下载暂存目录", err)
	}
	stagingInfo, err := os.Stat(stagingDir)
	if err != nil {
		_ = os.Remove(stagingDir)
		return "", outputError("无法检查下载暂存目录", err)
	}
	partPath := filepath.Join(stagingDir, "download.part")
	part, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(stagingDir)
		return "", outputError("无法创建临时下载文件", err)
	}
	partInfo, err := part.Stat()
	if err != nil {
		_ = part.Close()
		_ = os.Remove(partPath)
		_ = os.Remove(stagingDir)
		return "", outputError("无法检查临时下载文件", err)
	}
	partClosed := false
	stagingCleaned := false
	reportProgress := monotonicProgress(d.onProgress)
	defer func() {
		var closeErr error
		if !partClosed {
			closeErr = part.Close()
		}
		var cleanupErr error
		if !stagingCleaned {
			cleanupErr = cleanupOwnedStaging(stagingDir, partPath, stagingInfo, partInfo)
		}
		if finalizationErr := errors.Join(closeErr, cleanupErr); finalizationErr != nil && returnErr == nil {
			path = ""
			returnErr = outputError("无法清理临时下载文件", finalizationErr)
		}
	}()

	var lastErr *Error
	for attempt := 1; attempt <= d.retry.MaxAttempts; attempt++ {
		if err := resetPart(part); err != nil {
			return "", outputError("无法准备临时下载文件", err)
		}

		attemptErr, retry := d.downloadAttempt(ctx, rawURL, part, reportProgress)
		if attemptErr == nil {
			if err := part.Sync(); err != nil {
				return "", outputError("无法保存下载文件", err)
			}
			if err := part.Close(); err != nil {
				partClosed = true
				return "", outputError("无法关闭下载文件", err)
			}
			partClosed = true
			published, err := publishNoReplace(partPath, d.outputDir, title)
			if err != nil {
				return "", outputError("无法保存下载文件", err)
			}
			if err := cleanupOwnedStaging(stagingDir, partPath, stagingInfo, partInfo); err != nil {
				return "", outputError("无法清理临时下载文件", err)
			}
			stagingCleaned = true
			if err := syncDirectory(d.outputDir); err != nil {
				return "", outputError("无法确认下载文件已保存", err)
			}
			return published, nil
		}
		lastErr = attemptErr
		if !retry || attempt == d.retry.MaxAttempts {
			return "", attemptErr
		}
		retryDelay := d.retry.Backoff(attempt)
		if lastErr.retryAfter > retryDelay {
			retryDelay = lastErr.retryAfter
		}
		if err := d.sleep(ctx, retryDelay); err != nil {
			return "", canceledError(ctx)
		}
	}
	return "", lastErr
}

func (d *Downloader) downloadAttempt(ctx context.Context, rawURL string, part *os.File, reportProgress ProgressFunc) (*Error, bool) {
	if err := ctx.Err(); err != nil {
		return canceledError(ctx), false
	}
	if d.validateURLs {
		if _, err := safety.ValidateRemoteURL(ctx, rawURL, d.resolver); err != nil {
			if ctx.Err() != nil {
				return canceledError(ctx), false
			}
			return &Error{Code: CodeUnsafeSource, Message: "视频下载地址不安全或无效", cause: safeDiagnostic(err)}, false
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return &Error{Code: CodeUnsafeSource, Message: "视频下载地址格式无效", cause: errors.New("request URL is invalid")}, false
	}
	response, err := d.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return canceledError(ctx), false
		}
		var validationErr *safety.ValidationError
		if errors.As(err, &validationErr) {
			return &Error{Code: CodeUnsafeSource, Message: validationErr.Message, cause: safeDiagnostic(validationErr)}, false
		}
		return &Error{Code: CodeNetwork, Message: "连接视频服务器失败", cause: errors.New("HTTP transport failed")}, isTransientNetworkError(err)
	}

	if response.StatusCode != http.StatusOK {
		closeErr := response.Body.Close()
		failure := &Error{
			Code:       CodeHTTPStatus,
			Message:    fmt.Sprintf("视频服务器返回错误（HTTP %d）", response.StatusCode),
			StatusCode: response.StatusCode,
			retryAfter: parseRetryAfter(response.Header.Get("Retry-After"), d.now()),
		}
		if closeErr != nil {
			failure.cause = errors.New("close HTTP error response body failed")
		}
		return failure, isTransientStatus(response.StatusCode)
	}

	total := response.ContentLength
	if total < 0 {
		total = 0
	}
	written, copyErr := io.CopyBuffer(&progressWriter{
		writer:     &destinationWriter{writer: d.partWriter(part)},
		totalBytes: total,
		callback:   reportProgress,
	}, &sourceReader{reader: response.Body}, make([]byte, copyBufferSize))
	closeErr := response.Body.Close()
	if ctx.Err() != nil {
		return canceledError(ctx), false
	}
	if copyErr != nil {
		var writeErr *destinationWriteError
		if errors.As(copyErr, &writeErr) {
			return outputError("无法写入临时下载文件", errors.New("destination write failed")), false
		}
		return &Error{Code: CodeTransfer, Message: "视频传输中断", cause: errors.New("copy response body failed")}, isTransientTransferError(copyErr)
	}
	if closeErr != nil {
		return &Error{Code: CodeTransfer, Message: "视频传输未正常结束", cause: errors.New("close response body failed")}, isTransientNetworkError(closeErr)
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return &Error{Code: CodeTransfer, Message: "视频内容不完整", cause: fmt.Errorf("received %d bytes, expected %d", written, response.ContentLength)}, true
	}
	if written == 0 {
		return &Error{Code: CodeTransfer, Message: "视频内容为空", cause: errors.New("empty HTTP 200 response body")}, false
	}
	return nil, false
}

func monotonicProgress(callback ProgressFunc) ProgressFunc {
	if callback == nil {
		return nil
	}
	var highWater int64
	return func(progress Progress) {
		if progress.DownloadedBytes < highWater {
			return
		}
		highWater = progress.DownloadedBytes
		callback(progress)
	}
}

func identityWriter(writer io.Writer) io.Writer { return writer }

type sourceReader struct {
	reader io.Reader
}

func (r *sourceReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if err != nil && err != io.EOF {
		return n, &sourceReadError{cause: err}
	}
	return n, err
}

type sourceReadError struct {
	cause error
}

func (e *sourceReadError) Error() string { return "response body read failed" }
func (e *sourceReadError) Unwrap() error { return e.cause }

type destinationWriter struct {
	writer io.Writer
}

func (w *destinationWriter) Write(buffer []byte) (int, error) {
	n, err := w.writer.Write(buffer)
	if err != nil {
		return n, &destinationWriteError{cause: err}
	}
	if n != len(buffer) {
		return n, &destinationWriteError{cause: io.ErrShortWrite}
	}
	return n, nil
}

type destinationWriteError struct {
	cause error
}

func (e *destinationWriteError) Error() string { return "destination write failed" }
func (e *destinationWriteError) Unwrap() error { return e.cause }

type progressWriter struct {
	writer     io.Writer
	totalBytes int64
	written    int64
	callback   ProgressFunc
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if w.callback != nil && n > 0 {
		w.callback(Progress{DownloadedBytes: w.written, TotalBytes: w.totalBytes})
	}
	return n, err
}

func resetPart(part *os.File) error {
	if err := part.Truncate(0); err != nil {
		return err
	}
	_, err := part.Seek(0, io.SeekStart)
	return err
}

func publishNoReplace(partPath, dir, title string) (string, error) {
	base := output.SanitizeBaseName(title)
	for index := 1; ; index++ {
		suffix := ""
		if index > 1 {
			suffix = fmt.Sprintf(" (%d)", index)
		}
		nameBudget := maxFileNameBytes - len(suffix) - len(".mp4")
		candidateBase := truncateUTF8(base, nameBudget)
		candidate := filepath.Join(dir, candidateBase+suffix+".mp4")
		err := os.Link(partPath, candidate)
		switch {
		case err == nil:
			return candidate, nil
		case errors.Is(err, os.ErrExist):
			continue
		default:
			return "", fmt.Errorf("publish completed download: %w", err)
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

// cleanupOwnedStaging removes entries only from the private, random staging
// directory created for this operation. Identity checks prevent cleanup from
// following a replaced directory or part path.
func cleanupOwnedStaging(stagingDir, partPath string, stagingInfo, partInfo os.FileInfo) error {
	currentPart, err := os.Lstat(partPath)
	switch {
	case err == nil && !os.SameFile(partInfo, currentPart):
		return errors.New("part path no longer names the owned file")
	case err == nil:
		if err := os.Remove(partPath); err != nil {
			return fmt.Errorf("remove owned part: %w", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect part path: %w", err)
	}

	currentStaging, err := os.Lstat(stagingDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect staging directory: %w", err)
	}
	if !os.SameFile(stagingInfo, currentStaging) {
		return errors.New("staging path no longer names the owned directory")
	}
	if err := os.Remove(stagingDir); err != nil {
		return fmt.Errorf("remove owned staging directory: %w", err)
	}
	return nil
}

func isTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds >= int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := date.Sub(now)
	if delay <= 0 {
		return 0
	}
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}

func isTransientNetworkError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func isTransientTransferError(err error) bool {
	var readErr *sourceReadError
	if errors.As(err, &readErr) {
		return isTransientNetworkError(readErr.cause)
	}
	return false
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func defaultBackoff(failedAttempt int) time.Duration {
	return time.Duration(failedAttempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func canceledError(ctx context.Context) *Error {
	cause := context.Canceled
	if ctx != nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	return &Error{Code: CodeCanceled, Message: "下载已取消", cause: cause}
}

func outputError(message string, cause error) *Error {
	return &Error{Code: CodeOutput, Message: message, cause: cause}
}

func safeDiagnostic(err error) error {
	var validationErr *safety.ValidationError
	if errors.As(err, &validationErr) {
		return fmt.Errorf("source validation failed (%s)", validationErr.Code)
	}
	return errors.New("source validation failed")
}
