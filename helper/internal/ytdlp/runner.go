// Package ytdlp securely executes the constrained yt-dlp invocation used for
// supported platform video pages and publishes one validated local result.
package ytdlp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"web-video-harbor/helper/internal/output"
	"web-video-harbor/helper/internal/platformurl"
)

const (
	maxProgressLineBytes    = 1024
	maxDiagnosticLineBytes  = 4 * 1024
	maxDiagnosticTailBytes  = 16 * 1024
	maxTitleBytes           = 1024
	progressPrefix          = "WVH_PROGRESS:"
	progressTemplate        = "download:WVH_PROGRESS:%(info.format_id)#j\t%(progress._percent_str)s"
	terminationGrace        = 200 * time.Millisecond
	terminationConfirmGrace = 200 * time.Millisecond
	outputDrainGrace        = 200 * time.Millisecond
)

var (
	errInvalidConfig  = errors.New("yt-dlp configuration is invalid")
	errInvalidRequest = errors.New("yt-dlp request is invalid")
	errInvalidStaging = errors.New("yt-dlp staging directory is invalid")
)

// Quality is one of the fixed video quality choices exposed by the extension.
type Quality string

const (
	QualityBest Quality = "best"
	Quality1080 Quality = "1080"
	Quality720  Quality = "720"
)

var selectors = map[Quality]string{
	QualityBest: "bv*+ba/b",
	Quality1080: "bv*[height<=1080]+ba/b[height<=1080]",
	Quality720:  "bv*[height<=720]+ba/b[height<=720]",
}

// Progress is a bounded download percentage reported by yt-dlp.
type Progress struct {
	Percent float64
}

type ProgressFunc func(Progress)

type commandFactory func(path string, args []string, env []string) *exec.Cmd

type outputReservation interface {
	Path() string
	File() *os.File
	PublishExpected(int64) error
	Release() error
}

// Code is a stable platform-download failure category for API consumers.
type Code string

const (
	CodeCanceled             Code = "canceled"
	CodeLoginRequired        Code = "login_required"
	CodeVerificationRequired Code = "verification_required"
	CodeAccessLimited        Code = "access_limited"
	CodeGeoRestricted        Code = "geo_restricted"
	CodeExtractor            Code = "extractor_outdated"
	CodeFFmpegMissing        Code = "ffmpeg_missing"
	CodeNetwork              Code = "network"
	CodeNetworkFiltered      Code = "network_filtered"
	CodeJavaScriptRuntime    Code = "javascript_runtime"
	CodeOutput               Code = "output"
	CodeProcess              Code = "platform_process"
)

// Error exposes only a fixed, URL-free Chinese message.
type Error struct {
	Code                     Code
	Message                  string
	cause                    error
	retryableConnectionReset bool
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Config contains only fixed local paths and an optional progress callback.
type Config struct {
	BinaryPath         string
	RuntimePath        string
	FFmpegPath         string
	OutputDir          string
	OnProgress         ProgressFunc
	ExecutableSnapshot *ExecutableSnapshot
	RuntimeSnapshot    *ExecutableSnapshot
}

// Request identifies one canonical supported platform video page.
type Request struct {
	URL     string
	Title   string
	Quality Quality
}

// Runner holds the validated inputs needed to build one fixed argument array.
type Runner struct {
	binaryPath                  string
	runtimePath                 string
	ffmpegPath                  string
	outputDir                   string
	outputInfo                  os.FileInfo
	executableSnapshot          *ExecutableSnapshot
	runtimeSnapshot             *ExecutableSnapshot
	onProgress                  ProgressFunc
	commandFactory              commandFactory
	removeTree                  func(*os.File) error
	beforeOpenStaged            func(string)
	copyOutput                  func(context.Context, io.Writer, io.Reader) error
	reserveOutput               func(*os.File, string, string, string) (outputReservation, error)
	beforeCleanupRename         func(string)
	beforeCleanupRemove         func(string)
	beforeCleanupEntryIsolation func(*os.File, string)
	afterCleanupEntryIsolation  func(*os.File, string)
	beforeCleanupGuardRemove    func(*os.File, string)
	beforeRollbackIsolation     func(*os.File, string)
	beforeRollbackGuardRemove   func(*os.File, string)
}

type privateDirectoryOps struct {
	open   func(int, string, int, uint32) (int, error)
	wrap   func(uintptr, string) *os.File
	fchmod func(int, uint32) error
	stat   func(*os.File) (os.FileInfo, error)
}

// New validates configured path syntax and the existing output directory
// without executing either binary. The caller remains responsible for
// authenticating the binary files before construction.
func New(config Config) (*Runner, error) {
	if !validConfiguredPathSyntax(config.BinaryPath) || !validConfiguredPathSyntax(config.RuntimePath) ||
		!validConfiguredPathSyntax(config.FFmpegPath) {
		return nil, errInvalidConfig
	}
	if !validConfiguredPathSyntax(config.OutputDir) {
		return nil, errInvalidConfig
	}
	if config.ExecutableSnapshot == nil || config.BinaryPath != config.ExecutableSnapshot.Path() || config.ExecutableSnapshot.Verify() != nil {
		return nil, errInvalidConfig
	}
	if config.RuntimeSnapshot == nil || config.RuntimePath != config.RuntimeSnapshot.Path() || config.RuntimeSnapshot.Verify() != nil {
		return nil, errInvalidConfig
	}
	outputRoot, info, err := openConfiguredOutputRoot(config.OutputDir, nil)
	if err != nil {
		return nil, errInvalidConfig
	}
	_ = outputRoot.Close()

	return &Runner{
		binaryPath:         config.BinaryPath,
		runtimePath:        config.RuntimePath,
		ffmpegPath:         config.FFmpegPath,
		outputDir:          config.OutputDir,
		outputInfo:         info,
		executableSnapshot: config.ExecutableSnapshot,
		runtimeSnapshot:    config.RuntimeSnapshot,
		onProgress:         config.OnProgress,
		commandFactory:     defaultCommandFactory,
		removeTree:         removeDirectoryContents,
		copyOutput:         copyWithContext,
		reserveOutput: func(root *os.File, dir, base, extension string) (outputReservation, error) {
			return output.ReserveAvailablePathAt(root, dir, base, extension)
		},
	}, nil
}

type attemptMode uint8

const (
	attemptDefault attemptMode = iota
	attemptChromeMac
)

// Run executes one default attempt and, only for a YouTube TLS reset, one
// fixed Chrome/macOS-compatible attempt.
func (r *Runner) Run(ctx context.Context, request Request) (string, error) {
	path, err := r.runAttempt(ctx, request, attemptDefault)
	if err == nil || ctx == nil || ctx.Err() != nil {
		return path, err
	}
	video, classifyErr := platformurl.Classify(request.URL)
	var runnerError *Error
	if classifyErr != nil || video.Provider != platformurl.YouTube ||
		!errors.As(err, &runnerError) || !runnerError.retryableConnectionReset || errorContainsCode(err, CodeOutput) {
		return path, err
	}
	secondPath, secondErr := r.runAttempt(ctx, request, attemptChromeMac)
	var secondRunnerError *Error
	if secondErr != nil && errors.As(secondErr, &secondRunnerError) && secondRunnerError.retryableConnectionReset &&
		!errorContainsCode(secondErr, CodeOutput) {
		return secondPath, runError(CodeNetworkFiltered)
	}
	return secondPath, secondErr
}

func errorContainsCode(err error, code Code) bool {
	if err == nil {
		return false
	}
	if runnerError, ok := err.(*Error); ok && runnerError.Code == code {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, nested := range joined.Unwrap() {
			if errorContainsCode(nested, code) {
				return true
			}
		}
		return false
	}
	return errorContainsCode(errors.Unwrap(err), code)
}

// runAttempt executes one fixed yt-dlp invocation, validates its staged
// output, and publishes one video through an exclusive output reservation.
func (r *Runner) runAttempt(ctx context.Context, request Request, mode attemptMode) (path string, returnErr error) {
	if r == nil {
		return "", errInvalidRequest
	}
	if ctx == nil {
		return "", canceledError(context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return "", canceledError(err)
	}
	if err := validateRequest(request); err != nil {
		return "", err
	}
	if r.executableSnapshot == nil {
		return "", runError(CodeProcess)
	}
	releaseSnapshot, err := r.executableSnapshot.acquire()
	if err != nil {
		return "", runError(CodeProcess)
	}
	defer releaseSnapshot()
	if r.runtimeSnapshot == nil {
		return "", runError(CodeJavaScriptRuntime)
	}
	releaseRuntime, err := r.runtimeSnapshot.acquire()
	if err != nil {
		return "", runError(CodeJavaScriptRuntime)
	}
	defer releaseRuntime()

	outputRoot, _, err := openConfiguredOutputRoot(r.outputDir, r.outputInfo)
	if err != nil {
		return "", outputError()
	}
	defer outputRoot.Close()
	stagingName, stagingRoot, stagingInfo, err := createPrivateDirectoryAt(
		outputRoot,
		".web-video-platform-",
		privateDirectoryOps{},
	)
	if err != nil {
		return "", outputError()
	}
	stagingDir := filepath.Join(r.outputDir, stagingName)
	defer stagingRoot.Close()
	defer func() {
		cleanupErr := r.cleanupOwnedStaging(outputRoot, stagingName, stagingInfo)
		if verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo) != nil {
			hadPublishedPath := path != ""
			path = ""
			if publishedErr, ok := returnErr.(*output.PublishedError); hadPublishedPath && ok {
				returnErr = errors.Join(publishedErr.Unwrap(), outputError())
			} else if hadPublishedPath || returnErr == nil {
				returnErr = outputError()
			} else {
				returnErr = errors.Join(returnErr, outputError())
			}
			return
		}
		if cleanupErr == nil {
			return
		}
		cleanupWarning := outputError()
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cleanupWarning)
			return
		}
		if path != "" {
			returnErr = output.NewPublishedError(path, cleanupWarning)
		} else {
			returnErr = cleanupWarning
		}
	}()

	args, err := r.buildArgsForAttempt(request, stagingDir, mode)
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", canceledError(ctx.Err())
	}
	if err := verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo); err != nil {
		return "", outputError()
	}
	if r.executableSnapshot != nil {
		if err := r.executableSnapshot.Verify(); err != nil {
			return "", runError(CodeProcess)
		}
	}
	if r.runtimeSnapshot == nil || r.runtimeSnapshot.Verify() != nil {
		return "", runError(CodeJavaScriptRuntime)
	}
	command := r.commandFactory(r.binaryPath, args, minimalEnvironment())
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = outputDrainGrace
	progressState := progressState{}
	progressWriter := newBoundedLineWriter(maxProgressLineBytes, func(line []byte, overlong bool) {
		if overlong {
			return
		}
		next, ok := parseProgressLine(string(line), progressState)
		if !ok {
			return
		}
		progressState = next
		if r.onProgress != nil {
			r.onProgress(Progress{Percent: next.percent})
		}
	})
	diagnostic := make([]byte, 0, maxDiagnosticTailBytes)
	diagnosticWriter := newBoundedLineWriter(maxDiagnosticLineBytes, func(line []byte, _ bool) {
		diagnostic = appendBoundedTail(diagnostic, line, maxDiagnosticTailBytes)
		diagnostic = appendBoundedTail(diagnostic, []byte{'\n'}, maxDiagnosticTailBytes)
	})
	command.Stdout = progressWriter
	command.Stderr = diagnosticWriter
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return "", canceledError(ctx.Err())
		}
		return "", runError(CodeProcess)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()
	if r.executableSnapshot != nil {
		if err := r.executableSnapshot.Verify(); err != nil {
			_ = terminateProcessGroup(command.Process.Pid, waitResult)
			progressWriter.finish()
			diagnosticWriter.finish()
			return "", runError(CodeProcess)
		}
	}
	if r.runtimeSnapshot == nil || r.runtimeSnapshot.Verify() != nil {
		_ = terminateProcessGroup(command.Process.Pid, waitResult)
		progressWriter.finish()
		diagnosticWriter.finish()
		return "", runError(CodeJavaScriptRuntime)
	}
	var waitErr error
	canceled := false
	select {
	case waitErr = <-waitResult:
	case <-ctx.Done():
		canceled = true
		waitErr = terminateProcessGroup(command.Process.Pid, waitResult)
	}
	if !terminateOrphanedProcessGroup(command.Process.Pid) && waitErr == nil {
		waitErr = errors.New("platform process group remained active")
	}
	progressWriter.finish()
	diagnosticWriter.finish()
	if canceled || ctx.Err() != nil {
		return "", canceledError(ctx.Err())
	}
	if waitErr != nil {
		video, _ := platformurl.Classify(request.URL)
		return "", classifyDiagnostic(diagnostic, video.Provider)
	}
	if err := verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo); err != nil {
		return "", outputError()
	}

	stagedPath, stagedInfo, err := validateStagedOutput(outputRoot, stagingName, stagingRoot, stagingDir, stagingInfo)
	if err != nil {
		return "", outputError()
	}
	if r.beforeOpenStaged != nil {
		r.beforeOpenStaged(stagedPath)
	}
	stagedFD, err := unix.Openat(
		int(stagingRoot.Fd()),
		filepath.Base(stagedPath),
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return "", outputError()
	}
	staged := os.NewFile(uintptr(stagedFD), filepath.Base(stagedPath))
	if staged == nil {
		_ = unix.Close(stagedFD)
		return "", outputError()
	}
	defer staged.Close()
	openedInfo, err := staged.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() == 0 || !os.SameFile(stagedInfo, openedInfo) {
		return "", outputError()
	}
	if err := verifyOwnedDirectoryAt(outputRoot, stagingName, stagingInfo); err != nil {
		return "", outputError()
	}

	extension := strings.ToLower(filepath.Ext(stagedPath))
	if err := verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo); err != nil {
		return "", outputError()
	}
	reservation, err := r.reserveOutput(outputRoot, r.outputDir, request.Title, extension)
	if err != nil {
		return "", outputError()
	}
	reservedInfo, err := reservation.File().Stat()
	if err != nil || !reservedInfo.Mode().IsRegular() || reservedInfo.Mode().Perm() != 0o600 {
		_ = rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		return "", outputError()
	}
	if err := r.copyOutput(ctx, reservation.File(), staged); err != nil {
		rollbackOK := rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		if !rollbackOK {
			return "", outputError()
		}
		if ctx.Err() != nil {
			return "", canceledError(ctx.Err())
		}
		if errors.Is(err, context.Canceled) {
			return "", canceledError(context.Canceled)
		}
		return "", outputError()
	}
	if ctx.Err() != nil {
		if !rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove) {
			return "", outputError()
		}
		return "", canceledError(ctx.Err())
	}
	copiedInfo, err := reservation.File().Stat()
	if err != nil || !copiedInfo.Mode().IsRegular() || copiedInfo.Mode().Perm() != 0o600 ||
		!os.SameFile(reservedInfo, copiedInfo) || copiedInfo.Size() != openedInfo.Size() {
		_ = rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		return "", outputError()
	}
	if err := verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo); err != nil {
		_ = rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		return "", outputError()
	}
	if err := reservation.PublishExpected(openedInfo.Size()); err != nil {
		if releaseErr := reservation.Release(); releaseErr == nil {
			return "", outputError()
		}
		if completeOwnedReservationAt(outputRoot, filepath.Base(reservation.Path()), reservedInfo, openedInfo.Size()) &&
			verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo) == nil {
			path = reservation.Path()
			return path, output.NewPublishedError(path, outputError())
		}
		_ = rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		return "", outputError()
	}
	if !completeOwnedReservationAt(outputRoot, filepath.Base(reservation.Path()), reservedInfo, openedInfo.Size()) ||
		verifyConfiguredOutputRoot(r.outputDir, outputRoot, r.outputInfo) != nil {
		_ = rollbackReservation(outputRoot, reservation, reservedInfo, r.beforeRollbackIsolation, r.beforeRollbackGuardRemove)
		return "", outputError()
	}
	path = reservation.Path()
	if ctx.Err() != nil {
		return path, output.NewPublishedError(path, canceledError(ctx.Err()))
	}
	return path, nil
}

func rollbackReservation(
	root *os.File,
	reservation outputReservation,
	owned os.FileInfo,
	beforeIsolation func(*os.File, string),
	beforeGuardRemove func(*os.File, string),
) bool {
	if reservation == nil {
		return true
	}
	if err := reservation.Release(); err == nil {
		return true
	}
	if owned == nil {
		return false
	}
	name := filepath.Base(reservation.Path())
	pathInfo, err := fileInfoAt(root, name)
	if errors.Is(err, os.ErrNotExist) {
		_ = reservation.File().Close()
		return true
	}
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, pathInfo) {
		return false
	}
	if beforeIsolation != nil {
		beforeIsolation(root, name)
	}
	expected, err := identityFromFileInfo(owned)
	if err != nil {
		return false
	}
	cleanupName, cleanupDirectory, matched, err := isolateOwnedEntryAt(
		root,
		name,
		expected,
		".web-video-rollback-guard-",
		beforeGuardRemove,
	)
	if err != nil || !matched {
		if cleanupDirectory != nil {
			_ = cleanupDirectory.Close()
		}
		_ = reservation.File().Close()
		return false
	}
	if err := unix.Unlinkat(int(cleanupDirectory.Fd()), "isolated-entry", 0); err != nil {
		_ = cleanupDirectory.Close()
		_ = reservation.File().Close()
		return false
	}
	_ = reservation.File().Close()
	return removeEmptyPinnedDirectoryAt(root, cleanupName, cleanupDirectory, beforeGuardRemove) == nil
}

func completeOwnedReservationAt(root *os.File, name string, owned os.FileInfo, expectedSize int64) bool {
	if owned == nil || expectedSize <= 0 {
		return false
	}
	info, err := fileInfoAt(root, name)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().Perm() == 0o600 && os.SameFile(owned, info) && info.Size() == expectedSize
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(buffer)
	if err == nil {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return count, contextErr
		}
	}
	return count, err
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	_, err := io.Copy(destination, &contextReader{ctx: ctx, reader: source})
	return err
}

func outputError() error {
	return runError(CodeOutput)
}

func canceledError(cause error) error {
	if cause == nil {
		cause = context.Canceled
	}
	errorValue := runError(CodeCanceled)
	errorValue.cause = cause
	return errorValue
}

func runError(code Code) *Error {
	messages := map[Code]string{
		CodeCanceled:             "下载已取消",
		CodeLoginRequired:        "当前视频需要登录，当前版本不读取登录信息",
		CodeVerificationRequired: "YouTube 要求浏览器验证；为保护账号隐私，网页视频港不会读取登录信息",
		CodeAccessLimited:        "当前内容受会员、付费或私有访问限制",
		CodeGeoRestricted:        "当前网络所在地区无法访问此视频",
		CodeExtractor:            "平台解析规则已变化，请升级网页视频港",
		CodeFFmpegMissing:        "未找到可用的 FFmpeg，请安装或修复后重试",
		CodeNetwork:              "网络连接失败，请检查网络后重试",
		CodeNetworkFiltered:      "当前网络阻止了本地下载连接，请联系网络管理员或更换网络",
		CodeJavaScriptRuntime:    "视频解析组件不完整，请重新安装网页视频港",
		CodeOutput:               "无法安全保存平台视频",
		CodeProcess:              "平台暂时拒绝了下载，请稍后重试",
	}
	message, ok := messages[code]
	if !ok {
		code = CodeProcess
		message = messages[code]
	}
	return &Error{Code: code, Message: message}
}

func classifyDiagnostic(diagnostic []byte, provider platformurl.Provider) error {
	lower := strings.ToLower(string(diagnostic))
	containsAny := func(patterns ...string) bool {
		for _, pattern := range patterns {
			if strings.Contains(lower, pattern) {
				return true
			}
		}
		return false
	}

	switch {
	case containsAny("members-only", "members only", "private video", "video is private", "premium", "paid content", "付费", "会员"):
		return runError(CodeAccessLimited)
	case provider == platformurl.YouTube && containsAny("confirm you are not a bot", "confirm you're not a bot"):
		return runError(CodeVerificationRequired)
	case containsAny("sign in", "login required", "log in", "cookies-from-browser", "authentication required"):
		return runError(CodeLoginRequired)
	case containsAny("not available in your country", "not available in your region", "geo restriction", "geo-restricted", "geographic restriction"):
		return runError(CodeGeoRestricted)
	case containsAny("no supported javascript runtime", "javascript runtime could be found", "deno executable"):
		return runError(CodeJavaScriptRuntime)
	case containsAny("unable to extract", "extractor error", "nsig", "signature extraction", "update yt-dlp", "plugin is missing"):
		return runError(CodeExtractor)
	case containsAny("ffmpeg not found", "ffprobe not found", "ffmpeg is not installed"):
		return runError(CodeFFmpegMissing)
	case containsAny("network is unreachable", "unable to download webpage", "connection timed out", "connection reset", "temporary failure in name resolution", "nodename nor servname", "curl: (35)"):
		networkError := runError(CodeNetwork)
		networkError.retryableConnectionReset = containsAny("connection reset", "connectionreseterror", "curl: (35)")
		return networkError
	default:
		return runError(CodeProcess)
	}
}

func terminateProcessGroup(pid int, waitResult <-chan error) error {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.NewTimer(terminationGrace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var waitErr error
	waited := false
	for {
		if waited && !processGroupExists(pid) {
			return waitErr
		}
		select {
		case waitErr = <-waitResult:
			waited = true
		case <-ticker.C:
		case <-deadline.C:
			killed := processGroupExists(pid)
			if killed {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
			if !waited {
				waitErr = <-waitResult
			}
			if killed {
				_ = confirmProcessGroupExit(pid, terminationConfirmGrace, processGroupExists)
			}
			return waitErr
		}
	}
}

func confirmProcessGroupExit(pid int, timeout time.Duration, exists func(int) bool) bool {
	if exists == nil {
		return false
	}
	if !exists(pid) {
		return true
	}
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !exists(pid) {
				return true
			}
		case <-timer.C:
			return !exists(pid)
		}
	}
}

func processGroupExists(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func defaultCommandFactory(path string, args []string, env []string) *exec.Cmd {
	command := exec.Command(path, args...)
	command.Env = env
	return command
}

func minimalEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}

type boundedLineWriter struct {
	maxLineBytes int
	line         []byte
	overlong     bool
	handle       func([]byte, bool)
}

func newBoundedLineWriter(maxLineBytes int, handle func([]byte, bool)) *boundedLineWriter {
	return &boundedLineWriter{
		maxLineBytes: maxLineBytes,
		line:         make([]byte, 0, min(maxLineBytes, 4096)),
		handle:       handle,
	}
}

func (w *boundedLineWriter) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			w.appendFragment(data)
			break
		}
		w.appendFragment(data[:newline])
		w.emitLine()
		data = data[newline+1:]
	}
	return written, nil
}

func (w *boundedLineWriter) appendFragment(fragment []byte) {
	remaining := w.maxLineBytes - len(w.line)
	if remaining <= 0 {
		if len(fragment) > 0 {
			w.overlong = true
		}
		return
	}
	if len(fragment) > remaining {
		w.line = append(w.line, fragment[:remaining]...)
		w.overlong = true
		return
	}
	w.line = append(w.line, fragment...)
}

func (w *boundedLineWriter) emitLine() {
	line := w.line
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	w.handle(line, w.overlong)
	w.line = w.line[:0]
	w.overlong = false
}

func (w *boundedLineWriter) finish() {
	if len(w.line) == 0 && !w.overlong {
		return
	}
	w.emitLine()
}

func appendBoundedTail(tail, addition []byte, limit int) []byte {
	if len(addition) >= limit {
		return append(tail[:0], addition[len(addition)-limit:]...)
	}
	overflow := len(tail) + len(addition) - limit
	if overflow > 0 {
		copy(tail, tail[overflow:])
		tail = tail[:len(tail)-overflow]
	}
	return append(tail, addition...)
}

var stagedVideoExtensions = map[string]struct{}{
	".m4v":  {},
	".mkv":  {},
	".mp4":  {},
	".webm": {},
}

func validateStagedOutput(outputRoot *os.File, stagingName string, stagingRoot *os.File, stagingDir string, owned os.FileInfo) (string, os.FileInfo, error) {
	if err := verifyOwnedDirectoryAt(outputRoot, stagingName, owned); err != nil {
		return "", nil, err
	}
	rootInfo, err := stagingRoot.Stat()
	if err != nil || !validOwnedDirectory(rootInfo) || !os.SameFile(owned, rootInfo) {
		return "", nil, outputError()
	}
	entries, err := stagingRoot.ReadDir(-1)
	if err != nil || len(entries) != 1 {
		return "", nil, outputError()
	}
	name := entries[0].Name()
	if name == "" || filepath.Base(name) != name {
		return "", nil, outputError()
	}
	path := filepath.Join(stagingDir, name)
	info, err := fileInfoAt(stagingRoot, name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return "", nil, outputError()
	}
	if _, ok := stagedVideoExtensions[strings.ToLower(filepath.Ext(path))]; !ok {
		return "", nil, outputError()
	}
	return path, info, nil
}

func validOwnedDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0
}

func openConfiguredOutputRoot(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, outputError()
	}
	root, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, outputError()
	}
	openedInfo, err := root.Stat()
	if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) ||
		(expected != nil && !os.SameFile(expected, openedInfo)) {
		_ = root.Close()
		return nil, nil, outputError()
	}
	return root, openedInfo, nil
}

func verifyConfiguredOutputRoot(path string, root *os.File, expected os.FileInfo) error {
	if root == nil || expected == nil {
		return outputError()
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return outputError()
	}
	openedInfo, err := root.Stat()
	if err != nil || !openedInfo.IsDir() || !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, pathInfo) {
		return outputError()
	}
	return nil
}

func createPrivateDirectoryAt(root *os.File, prefix string, ops privateDirectoryOps) (string, *os.File, os.FileInfo, error) {
	if root == nil || prefix == "" || filepath.Base(prefix) != prefix {
		return "", nil, nil, outputError()
	}
	for range 100 {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", nil, nil, outputError()
		}
		name := prefix + hex.EncodeToString(random)
		if err := unix.Mkdirat(int(root.Fd()), name, 0o700); err != nil {
			if errors.Is(err, syscall.EEXIST) {
				continue
			}
			return "", nil, nil, outputError()
		}
		openDirectory := ops.open
		if openDirectory == nil {
			openDirectory = unix.Openat
		}
		fd, err := openDirectory(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return "", nil, nil, outputError()
		}
		wrapDirectory := ops.wrap
		if wrapDirectory == nil {
			wrapDirectory = os.NewFile
		}
		directory := wrapDirectory(uintptr(fd), name)
		if directory == nil {
			pinnedDirectory := os.NewFile(uintptr(fd), name)
			if pinnedDirectory == nil {
				_ = unix.Close(fd)
				return "", nil, nil, outputError()
			}
			cleanupErr := removeEmptyPinnedDirectoryAt(root, name, pinnedDirectory, nil)
			return "", nil, nil, errors.Join(outputError(), cleanupErr)
		}
		chmodDirectory := ops.fchmod
		if chmodDirectory == nil {
			chmodDirectory = unix.Fchmod
		}
		if err := chmodDirectory(fd, 0o700); err != nil {
			cleanupErr := removeEmptyPinnedDirectoryAt(root, name, directory, nil)
			return "", nil, nil, errors.Join(outputError(), cleanupErr)
		}
		statDirectory := ops.stat
		if statDirectory == nil {
			statDirectory = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
		}
		info, err := statDirectory(directory)
		if err != nil || !validOwnedDirectory(info) || info.Mode().Perm() != 0o700 {
			cleanupErr := removeEmptyPinnedDirectoryAt(root, name, directory, nil)
			return "", nil, nil, errors.Join(outputError(), cleanupErr)
		}
		return name, directory, info, nil
	}
	return "", nil, nil, outputError()
}

func fileInfoAt(root *os.File, name string) (os.FileInfo, error) {
	if root == nil || name == "" || filepath.Base(name) != name {
		return nil, outputError()
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, outputError()
	}
	defer file.Close()
	return file.Stat()
}

func verifyOwnedDirectoryAt(root *os.File, name string, owned os.FileInfo) error {
	info, err := fileInfoAt(root, name)
	if err != nil || !validOwnedDirectory(info) || !os.SameFile(owned, info) {
		return outputError()
	}
	return nil
}

func (r *Runner) cleanupOwnedStaging(outputRoot *os.File, stagingName string, owned os.FileInfo) error {
	if err := verifyOwnedDirectoryAt(outputRoot, stagingName, owned); err != nil {
		return err
	}
	quarantineName, quarantineDirectory, quarantineInfo, err := createPrivateDirectoryAt(
		outputRoot,
		".web-video-platform-cleanup-",
		privateDirectoryOps{},
	)
	if err != nil {
		return outputError()
	}
	defer quarantineDirectory.Close()
	stagingPath := filepath.Join(r.outputDir, stagingName)
	quarantineRoot := filepath.Join(r.outputDir, quarantineName)
	if r.beforeCleanupRename != nil {
		r.beforeCleanupRename(stagingPath)
	}
	if err := unix.Renameat(int(outputRoot.Fd()), stagingName, int(quarantineDirectory.Fd()), "owned-staging"); err != nil {
		return outputError()
	}
	movedFD, err := unix.Openat(
		int(quarantineDirectory.Fd()),
		"owned-staging",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return outputError()
	}
	movedDirectory := os.NewFile(uintptr(movedFD), "owned-staging")
	if movedDirectory == nil {
		_ = unix.Close(movedFD)
		return outputError()
	}
	defer movedDirectory.Close()
	movedInfo, err := movedDirectory.Stat()
	if err != nil || !validOwnedDirectory(movedInfo) || !os.SameFile(owned, movedInfo) {
		return outputError()
	}
	if r.beforeCleanupRemove != nil {
		r.beforeCleanupRemove(quarantineRoot)
	}
	removeTree := r.removeTree
	if r.beforeCleanupEntryIsolation != nil || r.afterCleanupEntryIsolation != nil || r.beforeCleanupGuardRemove != nil {
		removeTree = func(directory *os.File) error {
			return removeDirectoryContentsWithHook(
				directory,
				r.beforeCleanupEntryIsolation,
				r.afterCleanupEntryIsolation,
				r.beforeCleanupGuardRemove,
			)
		}
	}
	if err := removeTree(movedDirectory); err != nil {
		return outputError()
	}
	if err := movedDirectory.Close(); err != nil {
		return outputError()
	}
	movedIdentity, err := identityFromFileInfo(movedInfo)
	if err != nil {
		return outputError()
	}
	stagingCleanupName, stagingCleanupDirectory, matched, err := isolateOwnedEntryAt(
		quarantineDirectory,
		"owned-staging",
		movedIdentity,
		".web-video-platform-entry-cleanup-",
		r.beforeCleanupGuardRemove,
	)
	if err != nil || !matched {
		if stagingCleanupDirectory != nil {
			_ = stagingCleanupDirectory.Close()
		}
		return outputError()
	}
	if err := unix.Unlinkat(int(stagingCleanupDirectory.Fd()), "isolated-entry", unix.AT_REMOVEDIR); err != nil {
		_ = stagingCleanupDirectory.Close()
		return outputError()
	}
	if err := removeEmptyPinnedDirectoryAt(
		quarantineDirectory,
		stagingCleanupName,
		stagingCleanupDirectory,
		r.beforeCleanupGuardRemove,
	); err != nil {
		return outputError()
	}
	if err := quarantineDirectory.Close(); err != nil {
		return outputError()
	}
	quarantineIdentity, err := identityFromFileInfo(quarantineInfo)
	if err != nil {
		return outputError()
	}
	rootCleanupName, rootCleanupDirectory, matched, err := isolateOwnedEntryAt(
		outputRoot,
		quarantineName,
		quarantineIdentity,
		".web-video-platform-root-cleanup-",
		r.beforeCleanupGuardRemove,
	)
	if err != nil || !matched {
		if rootCleanupDirectory != nil {
			_ = rootCleanupDirectory.Close()
		}
		return outputError()
	}
	if err := unix.Unlinkat(int(rootCleanupDirectory.Fd()), "isolated-entry", unix.AT_REMOVEDIR); err != nil {
		_ = rootCleanupDirectory.Close()
		return outputError()
	}
	if err := removeEmptyPinnedDirectoryAt(
		outputRoot,
		rootCleanupName,
		rootCleanupDirectory,
		r.beforeCleanupGuardRemove,
	); err != nil {
		return outputError()
	}
	return nil
}

func removeDirectoryContents(directory *os.File) error {
	return removeDirectoryContentsWithHook(directory, nil, nil, nil)
}

func removeDirectoryContentsWithHook(
	directory *os.File,
	beforeIsolation func(*os.File, string),
	afterIsolation func(*os.File, string),
	beforeGuardRemove func(*os.File, string),
) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || filepath.Base(name) != name {
			return outputError()
		}
		expected, err := entryIdentityAt(directory, name)
		if err != nil {
			return err
		}
		if beforeIsolation != nil {
			beforeIsolation(directory, name)
		}
		cleanupName, cleanupDirectory, matched, err := isolateOwnedEntryAt(
			directory,
			name,
			expected,
			".web-video-platform-entry-cleanup-",
			beforeGuardRemove,
		)
		if err != nil || !matched {
			if cleanupDirectory != nil {
				_ = cleanupDirectory.Close()
			}
			return outputError()
		}
		if afterIsolation != nil {
			afterIsolation(cleanupDirectory, "isolated-entry")
		}
		childFD, openErr := unix.Openat(
			int(cleanupDirectory.Fd()),
			"isolated-entry",
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
			0,
		)
		if openErr == nil {
			child := os.NewFile(uintptr(childFD), name)
			if child == nil {
				_ = unix.Close(childFD)
				_ = cleanupDirectory.Close()
				return outputError()
			}
			childInfo, statErr := child.Stat()
			childIdentity, identityErr := identityFromFileInfo(childInfo)
			if statErr != nil || identityErr != nil || !validOwnedDirectory(childInfo) || childIdentity != expected {
				_ = child.Close()
				_ = cleanupDirectory.Close()
				return outputError()
			}
			removeErr := removeDirectoryContentsWithHook(child, beforeIsolation, afterIsolation, beforeGuardRemove)
			if removeErr != nil {
				_ = child.Close()
				_ = cleanupDirectory.Close()
				return outputError()
			}
			if err := removeEmptyPinnedOwnedDirectoryAt(
				cleanupDirectory,
				"isolated-entry",
				child,
				expected,
				beforeGuardRemove,
			); err != nil {
				_ = cleanupDirectory.Close()
				return outputError()
			}
		} else if !errors.Is(openErr, syscall.ENOTDIR) && !errors.Is(openErr, syscall.ELOOP) {
			_ = cleanupDirectory.Close()
			return openErr
		} else {
			currentIdentity, identityErr := entryIdentityAt(cleanupDirectory, "isolated-entry")
			if identityErr != nil || currentIdentity != expected {
				_ = cleanupDirectory.Close()
				return outputError()
			}
			if err := unix.Unlinkat(int(cleanupDirectory.Fd()), "isolated-entry", 0); err != nil {
				_ = cleanupDirectory.Close()
				return err
			}
		}
		if err := removeEmptyPinnedDirectoryAt(
			directory,
			cleanupName,
			cleanupDirectory,
			beforeGuardRemove,
		); err != nil {
			return err
		}
	}
	return nil
}

func removeEmptyPinnedOwnedDirectoryAt(
	parent *os.File,
	name string,
	directory *os.File,
	expected entryIdentity,
	beforeVerify func(*os.File, string),
) error {
	if parent == nil || directory == nil || name == "" || filepath.Base(name) != name {
		return outputError()
	}
	if beforeVerify != nil {
		beforeVerify(parent, name)
	}
	openedInfo, err := directory.Stat()
	if err != nil || !validOwnedDirectory(openedInfo) {
		_ = directory.Close()
		return outputError()
	}
	openedIdentity, err := identityFromFileInfo(openedInfo)
	if err != nil || openedIdentity != expected {
		_ = directory.Close()
		return outputError()
	}
	pathIdentity, err := entryIdentityAt(parent, name)
	if err != nil || pathIdentity != expected {
		_ = directory.Close()
		return outputError()
	}
	entries, readErr := directory.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		_ = directory.Close()
		return outputError()
	}
	// The owned directory FD remains pinned and the expected identity check is
	// immediately adjacent to this single rmdir. POSIX has no inode-conditional
	// unlink; replacement after this check is outside the supported threat model.
	removeErr := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
	closeErr := directory.Close()
	return errors.Join(removeErr, closeErr)
}

type entryIdentity struct {
	device uint64
	inode  uint64
}

func identityFromFileInfo(info os.FileInfo) (entryIdentity, error) {
	if info == nil {
		return entryIdentity{}, outputError()
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return entryIdentity{}, outputError()
	}
	return entryIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func entryIdentityAt(parent *os.File, name string) (entryIdentity, error) {
	if parent == nil || name == "" || filepath.Base(name) != name {
		return entryIdentity{}, outputError()
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return entryIdentity{}, err
	}
	return entryIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func isolateOwnedEntryAt(
	parent *os.File,
	name string,
	expected entryIdentity,
	prefix string,
	beforeGuardRemove func(*os.File, string),
) (string, *os.File, bool, error) {
	if parent == nil || name == "" || filepath.Base(name) != name {
		return "", nil, false, outputError()
	}
	cleanupName, cleanupDirectory, _, err := createPrivateDirectoryAt(parent, prefix, privateDirectoryOps{})
	if err != nil {
		return "", nil, false, err
	}
	if err := unix.Renameat(int(parent.Fd()), name, int(cleanupDirectory.Fd()), "isolated-entry"); err != nil {
		cleanupErr := removeEmptyPinnedDirectoryAt(parent, cleanupName, cleanupDirectory, beforeGuardRemove)
		return "", nil, false, errors.Join(err, cleanupErr)
	}
	isolatedIdentity, err := entryIdentityAt(cleanupDirectory, "isolated-entry")
	if err != nil || expected != isolatedIdentity {
		return cleanupName, cleanupDirectory, false, outputError()
	}
	return cleanupName, cleanupDirectory, true, nil
}

func removeEmptyPinnedDirectoryAt(
	parent *os.File,
	name string,
	directory *os.File,
	beforeVerify func(*os.File, string),
) error {
	if parent == nil || directory == nil || name == "" || filepath.Base(name) != name {
		return outputError()
	}
	if beforeVerify != nil {
		beforeVerify(parent, name)
	}
	openedInfo, err := directory.Stat()
	if err != nil || !validOwnedDirectory(openedInfo) || openedInfo.Mode().Perm() != 0o700 {
		_ = directory.Close()
		return outputError()
	}
	openedIdentity, err := identityFromFileInfo(openedInfo)
	if err != nil {
		_ = directory.Close()
		return outputError()
	}
	pathIdentity, err := entryIdentityAt(parent, name)
	if err != nil || openedIdentity != pathIdentity {
		_ = directory.Close()
		return outputError()
	}
	entries, readErr := directory.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		_ = directory.Close()
		return outputError()
	}
	// The name is random and private, the directory FD remains pinned, and the
	// identity check is immediately adjacent to this single rmdir. POSIX has no
	// inode-conditional unlink; replacement after this check is outside the
	// supported threat model. A replacement observed above is always preserved.
	removeErr := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
	closeErr := directory.Close()
	return errors.Join(removeErr, closeErr)
}

func (r *Runner) buildArgs(request Request, stagingDir string) ([]string, error) {
	return r.buildArgsForAttempt(request, stagingDir, attemptDefault)
}

func (r *Runner) buildArgsForAttempt(request Request, stagingDir string, mode attemptMode) ([]string, error) {
	if r == nil {
		return nil, errInvalidConfig
	}
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if !r.validStagingDir(stagingDir) {
		return nil, errInvalidStaging
	}

	selector := selectors[request.Quality]
	args := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--js-runtimes", "deno:" + r.runtimePath,
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", progressTemplate,
		"--merge-output-format", "mp4/mkv",
		"--ffmpeg-location", r.ffmpegPath,
		"--paths", "home:" + stagingDir,
		"--output", "media.%(ext)s",
		"--format", selector,
	}
	if mode == attemptChromeMac {
		args = append(args, "--impersonate", "Chrome-136:Macos-15")
	} else if mode != attemptDefault {
		return nil, errInvalidConfig
	}
	args = append(args, request.URL)
	return args, nil
}

func validateRequest(request Request) error {
	video, err := platformurl.Classify(request.URL)
	if err != nil || video.CanonicalURL != request.URL {
		return errInvalidRequest
	}
	if !validTitle(request.Title) {
		return errInvalidRequest
	}
	if _, ok := selectors[request.Quality]; !ok {
		return errInvalidRequest
	}
	return nil
}

func validTitle(title string) bool {
	if title == "" || len(title) > maxTitleBytes || !utf8.ValidString(title) || strings.TrimSpace(title) == "" {
		return false
	}
	for _, character := range title {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validConfiguredPathSyntax(path string) bool {
	if path == "" || !utf8.ValidString(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for _, character := range path {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (r *Runner) validStagingDir(stagingDir string) bool {
	if !validConfiguredPathSyntax(stagingDir) || stagingDir == r.outputDir {
		return false
	}
	info, err := os.Lstat(stagingDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return false
	}

	realOutput, err := filepath.EvalSymlinks(r.outputDir)
	if err != nil {
		return false
	}
	realStaging, err := filepath.EvalSymlinks(stagingDir)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(realOutput, realStaging)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type progressState struct {
	percent        float64
	hasPrevious    bool
	streamIDs      [2]string
	streamPercents [2]float64
	streamSeen     [2]bool
}

// parseProgressLine accepts only the dedicated progress marker, returns a
// bounded percentage, and suppresses duplicate or decreasing reports.
func parseProgressLine(line string, previous progressState) (progressState, bool) {
	if len(line) > maxProgressLineBytes || !strings.HasPrefix(line, progressPrefix) {
		return previous, false
	}
	if previous.hasPrevious && (math.IsNaN(previous.percent) || math.IsInf(previous.percent, 0)) {
		return previous, false
	}

	payload := strings.TrimPrefix(line, progressPrefix)
	fields := strings.Split(payload, "\t")
	if len(fields) != 2 {
		return previous, false
	}
	var streamID string
	if err := json.Unmarshal([]byte(fields[0]), &streamID); err != nil || !validProgressStreamID(streamID) {
		return previous, false
	}
	value := strings.TrimSpace(fields[1])
	if strings.HasSuffix(value, "%") {
		value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	}
	if value == "" {
		return previous, false
	}
	percent, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) {
		return previous, false
	}
	percent = math.Max(0, math.Min(100, percent))
	streamIndex := -1
	for index, knownID := range previous.streamIDs {
		if knownID == streamID {
			streamIndex = index
			break
		}
		if streamIndex == -1 && knownID == "" {
			streamIndex = index
		}
	}
	if streamIndex < 0 || previous.streamSeen[streamIndex] && percent <= previous.streamPercents[streamIndex] {
		return previous, false
	}
	next := previous
	next.streamIDs[streamIndex] = streamID
	next.streamPercents[streamIndex] = percent
	next.streamSeen[streamIndex] = true
	next.percent = (float64(streamIndex) + percent/100) * (99.0 / float64(len(next.streamIDs)))
	if previous.hasPrevious && next.percent <= previous.percent {
		return previous, false
	}
	next.hasPrevious = true
	return next, true
}

func validProgressStreamID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._+-", character) {
			continue
		}
		return false
	}
	return true
}
