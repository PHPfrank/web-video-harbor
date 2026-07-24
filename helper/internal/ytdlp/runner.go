// Package ytdlp securely executes the constrained yt-dlp invocation used for
// supported platform video pages and publishes one validated local result.
package ytdlp

import (
	"bytes"
	"context"
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
	maxProgressLineBytes   = 1024
	maxDiagnosticLineBytes = 4 * 1024
	maxDiagnosticTailBytes = 16 * 1024
	maxTitleBytes          = 1024
	progressPrefix         = "WVH_PROGRESS:"
	progressTemplate       = "download:WVH_PROGRESS:%(progress._percent_str)s"
	terminationGrace       = 200 * time.Millisecond
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
	Publish() error
	Release() error
}

// Code is a stable platform-download failure category for API consumers.
type Code string

const (
	CodeCanceled      Code = "canceled"
	CodeLoginRequired Code = "login_required"
	CodeAccessLimited Code = "access_limited"
	CodeGeoRestricted Code = "geo_restricted"
	CodeExtractor     Code = "extractor_outdated"
	CodeFFmpegMissing Code = "ffmpeg_missing"
	CodeNetwork       Code = "network"
	CodeOutput        Code = "output"
	CodeProcess       Code = "platform_process"
)

// Error exposes only a fixed, URL-free Chinese message.
type Error struct {
	Code    Code
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Config contains only fixed local paths and an optional progress callback.
type Config struct {
	BinaryPath string
	FFmpegPath string
	OutputDir  string
	OnProgress ProgressFunc
}

// Request identifies one canonical supported platform video page.
type Request struct {
	URL     string
	Title   string
	Quality Quality
}

// Runner holds the validated inputs needed to build one fixed argument array.
type Runner struct {
	binaryPath          string
	ffmpegPath          string
	outputDir           string
	onProgress          ProgressFunc
	commandFactory      commandFactory
	removeTree          func(*os.File) error
	beforeOpenStaged    func(string)
	copyOutput          func(context.Context, io.Writer, io.Reader) error
	reserveOutput       func(string, string, string) (outputReservation, error)
	beforeCleanupRename func(string)
	beforeCleanupRemove func(string)
}

// New validates configured path syntax and the existing output directory
// without executing either binary. The caller remains responsible for
// authenticating the binary files before construction.
func New(config Config) (*Runner, error) {
	if !validConfiguredPathSyntax(config.BinaryPath) || !validConfiguredPathSyntax(config.FFmpegPath) {
		return nil, errInvalidConfig
	}
	if !validConfiguredPathSyntax(config.OutputDir) {
		return nil, errInvalidConfig
	}
	info, err := os.Lstat(config.OutputDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errInvalidConfig
	}

	return &Runner{
		binaryPath:     config.BinaryPath,
		ffmpegPath:     config.FFmpegPath,
		outputDir:      config.OutputDir,
		onProgress:     config.OnProgress,
		commandFactory: defaultCommandFactory,
		removeTree:     removeDirectoryContents,
		copyOutput:     copyWithContext,
		reserveOutput: func(dir, base, extension string) (outputReservation, error) {
			return output.ReserveAvailablePath(dir, base, extension)
		},
	}, nil
}

// Run executes the fixed yt-dlp invocation, validates its staged output, and
// publishes one video through an exclusive output reservation.
func (r *Runner) Run(ctx context.Context, request Request) (path string, returnErr error) {
	if r == nil {
		return "", errInvalidRequest
	}
	if ctx == nil {
		return "", canceledError()
	}
	if err := ctx.Err(); err != nil {
		return "", canceledError()
	}
	if err := validateRequest(request); err != nil {
		return "", err
	}

	stagingDir, err := os.MkdirTemp(r.outputDir, ".web-video-platform-")
	if err != nil {
		return "", outputError()
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = os.Remove(stagingDir)
		return "", outputError()
	}
	stagingInfo, err := os.Lstat(stagingDir)
	if err != nil || !validOwnedDirectory(stagingInfo) {
		_ = os.Remove(stagingDir)
		return "", outputError()
	}
	stagingRoot, err := os.OpenFile(stagingDir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = os.Remove(stagingDir)
		return "", outputError()
	}
	openedStagingInfo, err := stagingRoot.Stat()
	if err != nil || !validOwnedDirectory(openedStagingInfo) || !os.SameFile(stagingInfo, openedStagingInfo) {
		_ = stagingRoot.Close()
		_ = os.Remove(stagingDir)
		return "", outputError()
	}
	defer stagingRoot.Close()
	defer func() {
		if cleanupErr := r.cleanupOwnedStaging(stagingDir, stagingInfo); cleanupErr != nil && returnErr == nil {
			if path != "" {
				returnErr = output.NewPublishedError(path, outputError())
			} else {
				returnErr = outputError()
			}
		}
	}()

	args, err := r.buildArgs(request, stagingDir)
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", canceledError()
	}
	command := r.commandFactory(r.binaryPath, args, minimalEnvironment())
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
			return "", canceledError()
		}
		return "", runError(CodeProcess)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()
	var waitErr error
	canceled := false
	select {
	case waitErr = <-waitResult:
	case <-ctx.Done():
		canceled = true
		waitErr = terminateProcessGroup(command.Process.Pid, waitResult)
	}
	progressWriter.finish()
	diagnosticWriter.finish()
	if canceled || ctx.Err() != nil {
		return "", canceledError()
	}
	if waitErr != nil {
		return "", classifyDiagnostic(diagnostic)
	}

	stagedPath, stagedInfo, err := validateStagedOutput(stagingRoot, stagingDir, stagingInfo)
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
	if err := verifyOwnedStaging(stagingDir, stagingInfo); err != nil {
		return "", outputError()
	}

	extension := strings.ToLower(filepath.Ext(stagedPath))
	reservation, err := r.reserveOutput(r.outputDir, request.Title, extension)
	if err != nil {
		return "", outputError()
	}
	reservedInfo, err := reservation.File().Stat()
	if err != nil || !reservedInfo.Mode().IsRegular() {
		_ = rollbackReservation(reservation, reservedInfo)
		return "", outputError()
	}
	if err := r.copyOutput(ctx, reservation.File(), staged); err != nil {
		rollbackOK := rollbackReservation(reservation, reservedInfo)
		if !rollbackOK {
			return "", outputError()
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return "", canceledError()
		}
		return "", outputError()
	}
	if ctx.Err() != nil {
		if !rollbackReservation(reservation, reservedInfo) {
			return "", outputError()
		}
		return "", canceledError()
	}
	if err := reservation.Publish(); err != nil {
		if releaseErr := reservation.Release(); releaseErr == nil {
			return "", outputError()
		}
		if completeOwnedReservation(reservation.Path(), reservedInfo, openedInfo.Size()) {
			path = reservation.Path()
			return path, output.NewPublishedError(path, outputError())
		}
		_ = rollbackReservation(reservation, reservedInfo)
		return "", outputError()
	}
	path = reservation.Path()
	if ctx.Err() != nil {
		return path, output.NewPublishedError(path, canceledError())
	}
	return path, nil
}

func rollbackReservation(reservation outputReservation, owned os.FileInfo) bool {
	if reservation == nil {
		return true
	}
	if err := reservation.Release(); err == nil {
		return true
	}
	if owned == nil {
		return false
	}
	pathInfo, err := os.Lstat(reservation.Path())
	if errors.Is(err, os.ErrNotExist) {
		_ = reservation.File().Close()
		return true
	}
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, pathInfo) {
		return false
	}
	if err := os.Remove(reservation.Path()); err != nil {
		return false
	}
	_ = reservation.File().Close()
	_, err = os.Lstat(reservation.Path())
	return errors.Is(err, os.ErrNotExist)
}

func completeOwnedReservation(path string, owned os.FileInfo, expectedSize int64) bool {
	if owned == nil || expectedSize <= 0 {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(owned, info) && info.Size() == expectedSize
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

func canceledError() error {
	errorValue := runError(CodeCanceled)
	errorValue.cause = context.Canceled
	return errorValue
}

func runError(code Code) *Error {
	messages := map[Code]string{
		CodeCanceled:      "下载已取消",
		CodeLoginRequired: "当前视频需要登录，v0.2.0 暂不支持",
		CodeAccessLimited: "当前内容受会员、付费或私有访问限制",
		CodeGeoRestricted: "当前网络所在地区无法访问此视频",
		CodeExtractor:     "平台解析规则已变化，请升级网页视频港",
		CodeFFmpegMissing: "未找到可用的 FFmpeg，请安装或修复后重试",
		CodeNetwork:       "网络连接失败，请检查网络后重试",
		CodeOutput:        "无法安全保存平台视频",
		CodeProcess:       "平台暂时拒绝了下载，请稍后重试",
	}
	message, ok := messages[code]
	if !ok {
		code = CodeProcess
		message = messages[code]
	}
	return &Error{Code: code, Message: message}
}

func classifyDiagnostic(diagnostic []byte) error {
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
	case containsAny("sign in", "login required", "log in", "cookies-from-browser", "authentication required"):
		return runError(CodeLoginRequired)
	case containsAny("not available in your country", "not available in your region", "geo restriction", "geo-restricted", "geographic restriction"):
		return runError(CodeGeoRestricted)
	case containsAny("unable to extract", "extractor error", "nsig", "signature extraction", "update yt-dlp", "no supported javascript runtime", "plugin is missing"):
		return runError(CodeExtractor)
	case containsAny("ffmpeg not found", "ffprobe not found", "ffmpeg is not installed"):
		return runError(CodeFFmpegMissing)
	case containsAny("network is unreachable", "unable to download webpage", "connection timed out", "connection reset", "temporary failure in name resolution", "nodename nor servname"):
		return runError(CodeNetwork)
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
			if processGroupExists(pid) {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
			if !waited {
				waitErr = <-waitResult
			}
			return waitErr
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

func validateStagedOutput(stagingRoot *os.File, stagingDir string, owned os.FileInfo) (string, os.FileInfo, error) {
	if err := verifyOwnedStaging(stagingDir, owned); err != nil {
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
	info, err := entries[0].Info()
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

func verifyOwnedStaging(path string, owned os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil || !validOwnedDirectory(info) || !os.SameFile(owned, info) {
		return outputError()
	}
	return nil
}

func (r *Runner) cleanupOwnedStaging(path string, owned os.FileInfo) error {
	if err := verifyOwnedStaging(path, owned); err != nil {
		return err
	}
	quarantineRoot, err := os.MkdirTemp(r.outputDir, ".web-video-platform-cleanup-")
	if err != nil {
		return outputError()
	}
	if err := os.Chmod(quarantineRoot, 0o700); err != nil {
		_ = os.Remove(quarantineRoot)
		return outputError()
	}
	quarantineInfo, err := os.Lstat(quarantineRoot)
	if err != nil || !validOwnedDirectory(quarantineInfo) {
		_ = os.Remove(quarantineRoot)
		return outputError()
	}
	quarantineDirectory, err := os.OpenFile(quarantineRoot, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = os.Remove(quarantineRoot)
		return outputError()
	}
	defer quarantineDirectory.Close()
	openedQuarantineInfo, err := quarantineDirectory.Stat()
	if err != nil || !validOwnedDirectory(openedQuarantineInfo) || !os.SameFile(quarantineInfo, openedQuarantineInfo) {
		return outputError()
	}
	if r.beforeCleanupRename != nil {
		r.beforeCleanupRename(path)
	}
	quarantinedStaging := filepath.Join(quarantineRoot, "owned-staging")
	if err := os.Rename(path, quarantinedStaging); err != nil {
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
	if err := r.removeTree(movedDirectory); err != nil {
		return outputError()
	}
	if err := movedDirectory.Close(); err != nil {
		return outputError()
	}
	if err := unix.Unlinkat(int(quarantineDirectory.Fd()), "owned-staging", unix.AT_REMOVEDIR); err != nil {
		return outputError()
	}
	if err := quarantineDirectory.Close(); err != nil {
		return outputError()
	}
	if err := os.Remove(quarantineRoot); err != nil {
		return outputError()
	}
	return nil
}

func removeDirectoryContents(directory *os.File) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || filepath.Base(name) != name {
			return outputError()
		}
		childFD, openErr := unix.Openat(
			int(directory.Fd()),
			name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
			0,
		)
		if openErr == nil {
			child := os.NewFile(uintptr(childFD), name)
			if child == nil {
				_ = unix.Close(childFD)
				return outputError()
			}
			removeErr := removeDirectoryContents(child)
			closeErr := child.Close()
			if removeErr != nil || closeErr != nil {
				return outputError()
			}
			if err := unix.Unlinkat(int(directory.Fd()), name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(openErr, syscall.ENOTDIR) && !errors.Is(openErr, syscall.ELOOP) {
			return openErr
		}
		if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) buildArgs(request Request, stagingDir string) ([]string, error) {
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
	return []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--max-downloads", "1",
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", progressTemplate,
		"--merge-output-format", "mp4/mkv",
		"--ffmpeg-location", r.ffmpegPath,
		"--paths", "home:" + stagingDir,
		"--output", "media.%(ext)s",
		"--format", selector,
		request.URL,
	}, nil
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
	percent     float64
	hasPrevious bool
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

	value := strings.TrimSpace(strings.TrimPrefix(line, progressPrefix))
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
	percent = math.Max(0, math.Min(99, percent))
	if previous.hasPrevious && percent <= previous.percent {
		return previous, false
	}
	return progressState{percent: percent, hasPrevious: true}, true
}
