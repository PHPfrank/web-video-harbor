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

// Classify determines a media kind from the URL path, then its content type.
func Classify(rawURL, contentType string) Kind {
	if parsed, err := url.Parse(rawURL); err == nil {
		switch strings.ToLower(path.Ext(parsed.Path)) {
		case ".mp4":
			return MP4
		case ".m3u8":
			return HLS
		}
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return Unknown
	}
	switch strings.ToLower(mediaType) {
	case "video/mp4":
		return MP4
	case "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return HLS
	default:
		return Unknown
	}
}
