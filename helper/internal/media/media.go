// Package media identifies media types from URLs and HTTP content types.
package media

import (
	"mime"
	"net/url"
	"path"
	"strings"
)

// Kind is a supported media kind.
type Kind string

const (
	Unknown Kind = "unknown"
	MP4     Kind = "mp4"
	HLS     Kind = "hls"
)

// Classify determines a media kind from the response content type, falling back
// to the URL path only when the content type is absent or generic.
func Classify(rawURL, contentType string) Kind {
	if strings.TrimSpace(contentType) != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return Unknown
		}
		switch strings.ToLower(mediaType) {
		case "video/mp4":
			return MP4
		case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl":
			return HLS
		case "application/octet-stream":
			// Generic binary responses rely on their URL extension.
		default:
			return Unknown
		}
	}

	if parsed, err := url.Parse(rawURL); err == nil {
		switch strings.ToLower(path.Ext(parsed.Path)) {
		case ".mp4":
			return MP4
		case ".m3u8":
			return HLS
		}
	}
	return Unknown
}
