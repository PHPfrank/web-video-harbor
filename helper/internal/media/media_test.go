package media

import "testing"

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rawURL      string
		contentType string
		want        Kind
	}{
		{name: "MP4 extension", rawURL: "https://cdn.example/video.mp4", want: MP4},
		{name: "MP4 extension before query and fragment", rawURL: "https://cdn.example/video.MP4?token=.m3u8#part", want: MP4},
		{name: "HLS extension", rawURL: "https://cdn.example/live.m3u8?token=abc", want: HLS},
		{name: "Apple HLS content type", rawURL: "https://cdn.example/play?id=1", contentType: "application/vnd.apple.mpegurl", want: HLS},
		{name: "legacy HLS content type with case and parameters", rawURL: "https://cdn.example/play", contentType: "Application/X-MPEGURL; Charset=UTF-8", want: HLS},
		{name: "audio HLS content type", rawURL: "https://cdn.example/play", contentType: "audio/mpegurl", want: HLS},
		{name: "MP4 content type with parameters", rawURL: "https://cdn.example/play", contentType: "VIDEO/MP4; codecs=avc1", want: MP4},
		{name: "HLS content type takes precedence over MP4 extension", rawURL: "https://cdn.example/video.mp4", contentType: "application/x-mpegurl", want: HLS},
		{name: "MP4 content type takes precedence over HLS extension", rawURL: "https://cdn.example/video.m3u8", contentType: "video/mp4", want: MP4},
		{name: "HTML content type rejects misleading MP4 extension", rawURL: "https://cdn.example/video.mp4", contentType: "text/html", want: Unknown},
		{name: "JSON content type rejects misleading HLS extension", rawURL: "https://cdn.example/video.m3u8", contentType: "application/json", want: Unknown},
		{name: "octet stream falls back to MP4 extension", rawURL: "https://cdn.example/video.mp4", contentType: "application/octet-stream", want: MP4},
		{name: "octet stream falls back to HLS extension", rawURL: "https://cdn.example/video.m3u8", contentType: "APPLICATION/OCTET-STREAM; charset=binary", want: HLS},
		{name: "does not match query substring", rawURL: "https://cdn.example/page?next=video.mp4", want: Unknown},
		{name: "does not match path substring", rawURL: "https://cdn.example/archive.mp4.backup", want: Unknown},
		{name: "does not match content type substring", rawURL: "https://cdn.example/play", contentType: "application/not-video/mp4-extra", want: Unknown},
		{name: "invalid URL", rawURL: "://bad", want: Unknown},
		{name: "empty input", want: Unknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Classify(tt.rawURL, tt.contentType); got != tt.want {
				t.Fatalf("Classify(%q, %q) = %q, want %q", tt.rawURL, tt.contentType, got, tt.want)
			}
		})
	}
}
