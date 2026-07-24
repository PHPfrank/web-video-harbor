package ytdlp

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testYouTubeURL = "https://www.youtube.com/watch?v=_mVb1D8wHxg"

func TestBuildArgsUsesExactQualitySelectors(t *testing.T) {
	runner, stagingDir := newTestRunner(t)

	tests := []struct {
		quality  Quality
		selector string
	}{
		{quality: QualityBest, selector: "bv*+ba/b"},
		{quality: Quality1080, selector: "bv*[height<=1080]+ba/b[height<=1080]"},
		{quality: Quality720, selector: "bv*[height<=720]+ba/b[height<=720]"},
	}

	for _, test := range tests {
		t.Run(string(test.quality), func(t *testing.T) {
			args, err := runner.buildArgs(Request{
				URL:     testYouTubeURL,
				Title:   "Test video",
				Quality: test.quality,
			}, stagingDir)
			if err != nil {
				t.Fatalf("buildArgs() error = %v", err)
			}
			if got := valueAfter(t, args, "--format"); got != test.selector {
				t.Fatalf("format selector = %q, want %q", got, test.selector)
			}
		})
	}
}

func TestBuildArgsRejectsMissingOrUnknownQuality(t *testing.T) {
	runner, stagingDir := newTestRunner(t)

	for _, quality := range []Quality{"", "4k", "BEST", " best "} {
		t.Run(string(quality), func(t *testing.T) {
			_, err := runner.buildArgs(Request{
				URL:     testYouTubeURL,
				Title:   "Test video",
				Quality: quality,
			}, stagingDir)
			if err == nil {
				t.Fatalf("buildArgs() accepted quality %q", quality)
			}
			if strings.Contains(err.Error(), testYouTubeURL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}
}

func TestBuildArgsUsesOnlyFixedSafeArgumentArray(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	request := Request{URL: testYouTubeURL, Title: "Test video", Quality: Quality1080}

	args, err := runner.buildArgs(request, stagingDir)
	if err != nil {
		t.Fatalf("buildArgs() error = %v", err)
	}

	want := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--max-downloads", "1",
		"--newline",
		"--no-colors",
		"--progress",
		"--progress-template", "download:WVH_PROGRESS:%(progress._percent_str)s",
		"--merge-output-format", "mp4/mkv",
		"--ffmpeg-location", runner.ffmpegPath,
		"--paths", "home:" + stagingDir,
		"--output", "media.%(ext)s",
		"--format", "bv*[height<=1080]+ba/b[height<=1080]",
		request.URL,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if args[len(args)-1] != request.URL {
		t.Fatalf("final argument = %q, want canonical page URL", args[len(args)-1])
	}
}

func TestBuildArgsContainsNoCookieConfigPluginUpdateOrPlaylistExpansionFlags(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	args, err := runner.buildArgs(Request{
		URL:     testYouTubeURL,
		Title:   "Test video",
		Quality: QualityBest,
	}, stagingDir)
	if err != nil {
		t.Fatalf("buildArgs() error = %v", err)
	}

	forbidden := map[string]struct{}{
		"--cookies":              {},
		"--cookies-from-browser": {},
		"--config-locations":     {},
		"--plugin-dirs":          {},
		"--update":               {},
		"-U":                     {},
		"--yes-playlist":         {},
	}
	for _, arg := range args {
		if _, found := forbidden[arg]; found {
			t.Fatalf("unsafe argument present: %q", arg)
		}
		for flag := range forbidden {
			if strings.HasPrefix(arg, flag+"=") {
				t.Fatalf("unsafe argument present: %q", arg)
			}
		}
	}
}

func TestBuildArgsRejectsNonCanonicalURLAndUnsafeStagingDirectory(t *testing.T) {
	runner, stagingDir := newTestRunner(t)
	tests := []struct {
		name       string
		requestURL string
		stagingDir string
	}{
		{
			name:       "non canonical URL",
			requestURL: testYouTubeURL + "&list=PLsecret",
			stagingDir: stagingDir,
		},
		{
			name:       "staging directory outside output root",
			requestURL: testYouTubeURL,
			stagingDir: t.TempDir(),
		},
		{
			name:       "relative staging directory",
			requestURL: testYouTubeURL,
			stagingDir: "relative-staging",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runner.buildArgs(Request{
				URL:     test.requestURL,
				Title:   "Test video",
				Quality: QualityBest,
			}, test.stagingDir)
			if err == nil {
				t.Fatal("buildArgs() accepted unsafe input")
			}
			if strings.Contains(err.Error(), test.requestURL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}
}

func TestParseProgressAcceptsPrefixWhitespaceDecimalsAndPercentSign(t *testing.T) {
	tests := []struct {
		line string
		want float64
	}{
		{line: "WVH_PROGRESS:42", want: 42},
		{line: "WVH_PROGRESS: 42.5%", want: 42.5},
		{line: "WVH_PROGRESS:\t 7.25 % \t", want: 7.25},
		{line: "WVH_PROGRESS:-10%", want: 0},
		{line: "WVH_PROGRESS:100%", want: 99},
		{line: "WVH_PROGRESS:999.5%", want: 99},
	}

	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			got, ok := parseProgressLine(test.line, progressState{})
			if !ok || !got.hasPrevious || got.percent != test.want {
				t.Fatalf("parseProgressLine(%q) = (%#v, %v), want percent %v", test.line, got, ok, test.want)
			}
		})
	}
}

func TestProgressParsingIsMonotonic(t *testing.T) {
	state := progressState{}
	var reported []float64
	for _, line := range []string{
		"WVH_PROGRESS:10%",
		"WVH_PROGRESS:8%",
		"WVH_PROGRESS:10%",
		"WVH_PROGRESS:63.4%",
		"WVH_PROGRESS:101%",
		"WVH_PROGRESS:80%",
	} {
		next, ok := parseProgressLine(line, state)
		if !ok {
			continue
		}
		state = next
		reported = append(reported, next.percent)
	}

	want := []float64{10, 63.4, 99}
	if !reflect.DeepEqual(reported, want) {
		t.Fatalf("reported progress = %#v, want %#v", reported, want)
	}
}

func TestProgressParsingReportsInitialZeroOnlyOnce(t *testing.T) {
	state := progressState{}

	first, ok := parseProgressLine("WVH_PROGRESS:0%", state)
	if !ok || !first.hasPrevious || first.percent != 0 {
		t.Fatalf("first zero progress = (%#v, %v), want reported zero", first, ok)
	}
	second, ok := parseProgressLine("WVH_PROGRESS:0.0%", first)
	if ok || second != first {
		t.Fatalf("repeated zero progress = (%#v, %v), want unchanged and ignored", second, ok)
	}
}

func TestParseProgressIgnoresUntrustedMalformedAndNonFiniteLines(t *testing.T) {
	tests := []string{
		"",
		"  WVH_PROGRESS:42%",
		"download:WVH_PROGRESS:42%",
		"[download] 42%",
		"WVH_PROGRESS:",
		"WVH_PROGRESS:%",
		"WVH_PROGRESS:NaN%",
		"WVH_PROGRESS:+Inf%",
		"WVH_PROGRESS:-Inf%",
		"WVH_PROGRESS:12%%",
		"WVH_PROGRESS:1 2%",
		"WVH_PROGRESS:42% trailing",
	}

	for _, line := range tests {
		t.Run(line, func(t *testing.T) {
			previous := progressState{percent: 17, hasPrevious: true}
			got, ok := parseProgressLine(line, previous)
			if ok || got != previous {
				t.Fatalf("parseProgressLine(%q) = (%#v, %v), want unchanged state", line, got, ok)
			}
		})
	}
}

func TestParseProgressEnforcesMaximumLineLengthBoundary(t *testing.T) {
	prefix := "WVH_PROGRESS:"
	value := "42%"
	atLimit := prefix + strings.Repeat(" ", maxProgressLineBytes-len(prefix)-len(value)) + value
	overLimit := atLimit + " "

	if len(atLimit) != maxProgressLineBytes {
		t.Fatalf("test line length = %d, want %d", len(atLimit), maxProgressLineBytes)
	}
	if got, ok := parseProgressLine(atLimit, progressState{}); !ok || !got.hasPrevious || got.percent != 42 {
		t.Fatalf("at-limit progress = (%#v, %v), want reported 42", got, ok)
	}
	previous := progressState{percent: 17, hasPrevious: true}
	if got, ok := parseProgressLine(overLimit, previous); ok || got != previous {
		t.Fatalf("over-limit progress = (%#v, %v), want unchanged state", got, ok)
	}
}

func TestParseProgressRejectsNonFinitePreviousValue(t *testing.T) {
	for _, previous := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		state := progressState{percent: previous, hasPrevious: true}
		got, ok := parseProgressLine("WVH_PROGRESS:50%", state)
		if ok || !math.IsNaN(got.percent) && !math.IsInf(got.percent, 0) {
			t.Fatalf("non-finite previous value was accepted: (%#v, %v)", got, ok)
		}
	}
}

func TestNewValidatesRequiredPathsWithoutLeakingRequestData(t *testing.T) {
	valid := testConfig(t)
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty binary", mutate: func(config *Config) { config.BinaryPath = "" }},
		{name: "relative binary", mutate: func(config *Config) { config.BinaryPath = "yt-dlp" }},
		{name: "unclean binary", mutate: func(config *Config) { config.BinaryPath += "/../yt-dlp" }},
		{name: "empty ffmpeg", mutate: func(config *Config) { config.FFmpegPath = "" }},
		{name: "relative ffmpeg", mutate: func(config *Config) { config.FFmpegPath = "ffmpeg" }},
		{name: "control in ffmpeg", mutate: func(config *Config) { config.FFmpegPath += "\nsecret" }},
		{name: "empty output", mutate: func(config *Config) { config.OutputDir = "" }},
		{name: "relative output", mutate: func(config *Config) { config.OutputDir = "downloads" }},
		{name: "output is file", mutate: func(config *Config) { config.OutputDir = config.BinaryPath }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted invalid configuration")
			} else if strings.Contains(err.Error(), testYouTubeURL) {
				t.Fatalf("error leaked unrelated request URL: %v", err)
			}
		})
	}
}

func TestRunValidatesRequestAndDoesNotExecute(t *testing.T) {
	runner, _ := newTestRunner(t)
	tests := []struct {
		name    string
		request Request
	}{
		{name: "empty URL", request: Request{Title: "Title", Quality: QualityBest}},
		{name: "unsupported URL", request: Request{URL: "https://example.com/video", Title: "Title", Quality: QualityBest}},
		{name: "non canonical URL", request: Request{URL: testYouTubeURL + "&feature=share", Title: "Title", Quality: QualityBest}},
		{name: "empty title", request: Request{URL: testYouTubeURL, Quality: QualityBest}},
		{name: "whitespace title", request: Request{URL: testYouTubeURL, Title: " \t ", Quality: QualityBest}},
		{name: "control in title", request: Request{URL: testYouTubeURL, Title: "Title\nsecret", Quality: QualityBest}},
		{name: "invalid UTF-8 title", request: Request{URL: testYouTubeURL, Title: string([]byte{0xff}), Quality: QualityBest}},
		{name: "oversized title", request: Request{URL: testYouTubeURL, Title: strings.Repeat("a", maxTitleBytes+1), Quality: QualityBest}},
		{name: "empty quality", request: Request{URL: testYouTubeURL, Title: "Title"}},
		{name: "unknown quality", request: Request{URL: testYouTubeURL, Title: "Title", Quality: "4k"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := runner.Run(context.Background(), test.request)
			if err == nil || path != "" {
				t.Fatalf("Run() = (%q, %v), want safe validation error", path, err)
			}
			if test.request.URL != "" && strings.Contains(err.Error(), test.request.URL) {
				t.Fatalf("error leaked request URL: %v", err)
			}
		})
	}

	path, err := runner.Run(context.Background(), Request{
		URL:     testYouTubeURL,
		Title:   "Valid title",
		Quality: QualityBest,
	})
	if err == nil || path != "" {
		t.Fatalf("Run(valid request) = (%q, %v), want execution-unavailable error", path, err)
	}
	if !strings.Contains(err.Error(), "execution is unavailable") {
		t.Fatalf("Run(valid request) error = %v, want explicit unavailable marker", err)
	}
}

func newTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stagingDir := filepath.Join(config.OutputDir, ".wvh-test-stage")
	if err := os.Mkdir(stagingDir, 0o700); err != nil {
		t.Fatalf("create staging directory: %v", err)
	}
	return runner, stagingDir
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		BinaryPath: filepath.Join(root, "yt-dlp_macos"),
		FFmpegPath: filepath.Join(root, "ffmpeg"),
		OutputDir:  root,
	}
}

func valueAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index < len(args)-1; index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("flag %q not found in %#v", flag, args)
	return ""
}
