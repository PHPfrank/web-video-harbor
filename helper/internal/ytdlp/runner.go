// Package ytdlp defines the constrained yt-dlp invocation used for supported
// platform video pages. Process execution is added separately.
package ytdlp

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"web-video-harbor/helper/internal/platformurl"
)

const (
	maxProgressLineBytes = 1024
	maxTitleBytes        = 1024
	progressPrefix       = "WVH_PROGRESS:"
	progressTemplate     = "download:WVH_PROGRESS:%(progress._percent_str)s"
)

var (
	errInvalidConfig        = errors.New("yt-dlp configuration is invalid")
	errInvalidRequest       = errors.New("yt-dlp request is invalid")
	errInvalidStaging       = errors.New("yt-dlp staging directory is invalid")
	errExecutionUnavailable = errors.New("yt-dlp execution is unavailable")
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
	binaryPath string
	ffmpegPath string
	outputDir  string
	onProgress ProgressFunc
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
		binaryPath: config.BinaryPath,
		ffmpegPath: config.FFmpegPath,
		outputDir:  config.OutputDir,
		onProgress: config.OnProgress,
	}, nil
}

// Run currently validates the request and deliberately refuses to execute.
// Subprocess lifecycle and output publication are implemented in the next
// runner task.
func (r *Runner) Run(ctx context.Context, request Request) (string, error) {
	if r == nil || ctx == nil {
		return "", errInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateRequest(request); err != nil {
		return "", err
	}
	return "", errExecutionUnavailable
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
