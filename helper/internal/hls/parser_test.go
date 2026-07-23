package hls

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseMasterPlaylist(t *testing.T) {
	t.Parallel()

	got, err := parseFixture(t, "https://cdn.example/path/master.m3u8", "master.m3u8")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !got.Master {
		t.Fatal("Parse() Master = false, want true")
	}
	if len(got.Variants) != 2 {
		t.Fatalf("Parse() returned %d variants, want 2", len(got.Variants))
	}

	want := []Variant{
		{URL: "https://cdn.example/path/1080/index.m3u8", Label: "1080p", Bandwidth: 5200000, Width: 1920, Height: 1080, Codecs: "avc1.640028,mp4a.40.2"},
		{URL: "https://cdn.example/path/720/index.m3u8", Label: "720p", Bandwidth: 2800000, Width: 1280, Height: 720, Codecs: "avc1.4d401f,mp4a.40.2"},
	}
	for i := range want {
		if got.Variants[i] != want[i] {
			t.Errorf("variant[%d] = %#v, want %#v", i, got.Variants[i], want[i])
		}
	}
}

func TestParseMediaPlaylistReturnsOriginalQuality(t *testing.T) {
	t.Parallel()

	const manifestURL = "https://cdn.example/live/media.m3u8?token=abc"
	got, err := parseFixture(t, manifestURL, "media.m3u8")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.Master {
		t.Fatal("Parse() Master = true, want false")
	}
	want := []Variant{{URL: manifestURL, Label: "原始画质"}}
	if len(got.Variants) != 1 || got.Variants[0] != want[0] {
		t.Fatalf("Parse() Variants = %#v, want %#v", got.Variants, want)
	}
}

func TestParseRejectsEncryptedPlaylist(t *testing.T) {
	t.Parallel()

	_, err := parseFixture(t, "https://cdn.example/live/media.m3u8", "encrypted.m3u8")
	assertErrorCode(t, err, CodeUnsupportedEncryption)
}

func TestParseAcceptsEncryptionMethodNone(t *testing.T) {
	t.Parallel()

	playlist := "#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:4,\npart.ts\n"
	got, err := Parse("https://cdn.example/live/media.m3u8", strings.NewReader(playlist))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(got.Variants) != 1 || got.Variants[0].Label != "原始画质" {
		t.Fatalf("Parse() Variants = %#v", got.Variants)
	}
}

func TestParseHandlesBOMCRLFAndBlankLines(t *testing.T) {
	t.Parallel()

	playlist := []byte("\xef\xbb\xbf\r\n\r\n#EXTM3U\r\n\r\n#EXTINF:4,\r\npart.ts\r\n")
	got, err := ParseBytes("https://cdn.example/live/media.m3u8", playlist)
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}
	if len(got.Variants) != 1 || got.Variants[0].URL != "https://cdn.example/live/media.m3u8" {
		t.Fatalf("ParseBytes() Variants = %#v", got.Variants)
	}
}

func TestParseRejectsMalformedPlaylists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		playlist string
	}{
		{name: "empty", playlist: ""},
		{name: "only blank lines", playlist: " \n\r\n"},
		{name: "missing EXTM3U", playlist: "#EXTINF:4,\npart.ts\n"},
		{name: "empty after header", playlist: "#EXTM3U\n"},
		{name: "missing variant URI", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1234\n"},
		{name: "tag instead of variant URI", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1234\n#EXT-X-ENDLIST\n"},
		{name: "key tag instead of variant URI", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1234\n#EXT-X-KEY:METHOD=NONE\nv.m3u8\n"},
		{name: "invalid bandwidth", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=fast\nv.m3u8\n"},
		{name: "missing bandwidth", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=640x360\nv.m3u8\n"},
		{name: "invalid resolution", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=wide\nv.m3u8\n"},
		{name: "invalid quoted attribute", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"avc1,mp4a\nv.m3u8\n"},
		{name: "duplicate attribute", playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,BANDWIDTH=2\nv.m3u8\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse("https://cdn.example/master.m3u8", strings.NewReader(tt.playlist))
			assertErrorCode(t, err, CodeInvalidPlaylist)
		})
	}
}

func TestParseAllowsUnknownAttributes(t *testing.T) {
	t.Parallel()

	playlist := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,FRAME-RATE=30.000,CUSTOM=ok\nvideo.m3u8\n"
	got, err := Parse("https://cdn.example/master.m3u8", strings.NewReader(playlist))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.Variants[0].URL != "https://cdn.example/video.m3u8" {
		t.Fatalf("variant URL = %q", got.Variants[0].URL)
	}
}

func TestParseSortsAudioOnlyVariantsByBandwidth(t *testing.T) {
	t.Parallel()

	playlist := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=128000,CODECS=\"mp4a.40.2\"\nlow/audio.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=256000,CODECS=\"mp4a.40.2\"\n/audio/high.m3u8\n"
	got, err := Parse("https://cdn.example/path/master.m3u8", strings.NewReader(playlist))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := []Variant{
		{URL: "https://cdn.example/audio/high.m3u8", Label: "256 kbps", Bandwidth: 256000, Codecs: "mp4a.40.2"},
		{URL: "https://cdn.example/path/low/audio.m3u8", Label: "128 kbps", Bandwidth: 128000, Codecs: "mp4a.40.2"},
	}
	if len(got.Variants) != len(want) {
		t.Fatalf("got %d variants, want %d", len(got.Variants), len(want))
	}
	for i := range want {
		if got.Variants[i] != want[i] {
			t.Errorf("variant[%d] = %#v, want %#v", i, got.Variants[i], want[i])
		}
	}
}

func TestParseRejectsOversizedPlaylist(t *testing.T) {
	t.Parallel()

	playlist := "#EXTM3U\n" + strings.Repeat("# filler\n", (2*1024*1024/9)+1)
	_, err := Parse("https://cdn.example/media.m3u8", strings.NewReader(playlist))
	assertErrorCode(t, err, CodePlaylistTooLarge)
}

func TestParseRejectsInvalidManifestURL(t *testing.T) {
	t.Parallel()

	_, err := Parse("://bad", strings.NewReader("#EXTM3U\n#EXTINF:1,\na.ts\n"))
	assertErrorCode(t, err, CodeInvalidPlaylist)
}

func parseFixture(t *testing.T, manifestURL, name string) (*Playlist, error) {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	return Parse(manifestURL, f)
}

func assertErrorCode(t *testing.T, err error, want Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	var parseErr *Error
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *Error: %v", err, err)
	}
	if parseErr.Code != want {
		t.Fatalf("error code = %q, want %q: %v", parseErr.Code, want, err)
	}
	if parseErr.Message == "" {
		t.Fatal("error message is empty")
	}
}
