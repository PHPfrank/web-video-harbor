// Package hls inspects HLS playlists without fetching referenced resources.
package hls

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const maxPlaylistSize = 2 * 1024 * 1024

// Code identifies a playlist inspection failure for API consumers.
type Code string

const (
	CodeInvalidPlaylist       Code = "invalid_playlist"
	CodeUnsupportedEncryption Code = "unsupported_encryption"
	CodePlaylistTooLarge      Code = "playlist_too_large"
)

// Error is a typed playlist inspection error.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Variant is one selectable stream in an HLS playlist.
type Variant struct {
	URL       string `json:"url"`
	Label     string `json:"label"`
	Bandwidth int    `json:"bandwidth,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Codecs    string `json:"codecs,omitempty"`
}

// Playlist is the result of inspecting a master or media playlist.
type Playlist struct {
	Master   bool      `json:"master"`
	Variants []Variant `json:"variants"`
}

// ParseBytes inspects an HLS playlist held in memory.
func ParseBytes(manifestURL string, data []byte) (*Playlist, error) {
	return Parse(manifestURL, bytes.NewReader(data))
}

// Parse inspects an HLS playlist read from r.
func Parse(manifestURL string, r io.Reader) (*Playlist, error) {
	base, err := url.Parse(manifestURL)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return nil, invalidError("manifest URL must be absolute", err)
	}
	if r == nil {
		return nil, invalidError("playlist reader is nil", nil)
	}

	counter := &countingReader{r: io.LimitReader(r, maxPlaylistSize+1)}
	scanner := bufio.NewScanner(counter)
	scanner.Buffer(make([]byte, 64*1024), maxPlaylistSize+2)

	var (
		headerSeen bool
		mediaURI   bool
		pending    *Variant
		variants   []Variant
	)

	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		if !headerSeen {
			if line != "#EXTM3U" {
				return nil, invalidError("playlist must begin with #EXTM3U", nil)
			}
			headerSeen = true
			continue
		}
		if pending != nil && strings.HasPrefix(line, "#") {
			return nil, invalidError("variant URI is missing", nil)
		}

		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			attrs, parseErr := parseAttributes(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if parseErr != nil {
				return nil, invalidError("invalid EXT-X-KEY attributes", parseErr)
			}
			method, ok := attrs["METHOD"]
			if !ok || method == "" {
				return nil, invalidError("EXT-X-KEY is missing METHOD", nil)
			}
			if !strings.EqualFold(method, "NONE") {
				return nil, &Error{
					Code:    CodeUnsupportedEncryption,
					Message: fmt.Sprintf("HLS encryption method %q is not supported", method),
				}
			}
			continue
		}

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			variant, parseErr := parseVariant(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if parseErr != nil {
				return nil, invalidError("invalid EXT-X-STREAM-INF attributes", parseErr)
			}
			pending = &variant
			continue
		}

		if strings.HasPrefix(line, "#") {
			continue
		}

		resolved, resolveErr := resolveURL(base, line)
		if resolveErr != nil {
			return nil, invalidError("playlist contains an invalid URI", resolveErr)
		}
		if pending != nil {
			pending.URL = resolved
			variants = append(variants, *pending)
			pending = nil
		} else {
			mediaURI = true
		}
	}

	if counter.n > maxPlaylistSize {
		return nil, &Error{Code: CodePlaylistTooLarge, Message: "playlist exceeds the 2 MiB limit"}
	}
	if err := scanner.Err(); err != nil {
		return nil, invalidError("could not read playlist", err)
	}
	if !headerSeen {
		return nil, invalidError("playlist is empty", nil)
	}
	if pending != nil {
		return nil, invalidError("variant URI is missing", nil)
	}
	if len(variants) > 0 {
		sort.SliceStable(variants, func(i, j int) bool {
			leftPixels := variants[i].Width * variants[i].Height
			rightPixels := variants[j].Width * variants[j].Height
			if leftPixels != rightPixels {
				return leftPixels > rightPixels
			}
			return variants[i].Bandwidth > variants[j].Bandwidth
		})
		return &Playlist{Master: true, Variants: variants}, nil
	}
	if mediaURI {
		return &Playlist{Variants: []Variant{{URL: base.String(), Label: "原始画质"}}}, nil
	}
	return nil, invalidError("playlist has no media or variants", nil)
}

type countingReader struct {
	r io.Reader
	n int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

func parseVariant(raw string) (Variant, error) {
	attrs, err := parseAttributes(raw)
	if err != nil {
		return Variant{}, err
	}

	bandwidthValue, ok := attrs["BANDWIDTH"]
	if !ok {
		return Variant{}, fmt.Errorf("BANDWIDTH is required")
	}
	bandwidth, err := strconv.Atoi(bandwidthValue)
	if err != nil || bandwidth <= 0 {
		return Variant{}, fmt.Errorf("invalid BANDWIDTH %q", bandwidthValue)
	}

	variant := Variant{Bandwidth: bandwidth, Codecs: attrs["CODECS"]}
	if resolution, ok := attrs["RESOLUTION"]; ok {
		parts := strings.Split(resolution, "x")
		if len(parts) != 2 {
			return Variant{}, fmt.Errorf("invalid RESOLUTION %q", resolution)
		}
		variant.Width, err = strconv.Atoi(parts[0])
		if err != nil || variant.Width <= 0 {
			return Variant{}, fmt.Errorf("invalid RESOLUTION %q", resolution)
		}
		variant.Height, err = strconv.Atoi(parts[1])
		if err != nil || variant.Height <= 0 {
			return Variant{}, fmt.Errorf("invalid RESOLUTION %q", resolution)
		}
		variant.Label = fmt.Sprintf("%dp", variant.Height)
	} else {
		variant.Label = fmt.Sprintf("%d kbps", bandwidth/1000)
	}
	return variant, nil
}

func parseAttributes(raw string) (map[string]string, error) {
	parts, err := splitAttributes(raw)
	if err != nil {
		return nil, err
	}
	attrs := make(map[string]string, len(parts))
	for _, part := range parts {
		key, value, found := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			return nil, fmt.Errorf("invalid attribute %q", part)
		}
		if _, exists := attrs[key]; exists {
			return nil, fmt.Errorf("duplicate attribute %q", key)
		}
		if strings.HasPrefix(value, "\"") || strings.HasSuffix(value, "\"") {
			if len(value) < 2 || !strings.HasPrefix(value, "\"") || !strings.HasSuffix(value, "\"") {
				return nil, fmt.Errorf("invalid quoted value for %q", key)
			}
			value = value[1 : len(value)-1]
			if strings.Contains(value, "\"") {
				return nil, fmt.Errorf("invalid quote in %q", key)
			}
		}
		attrs[key] = value
	}
	return attrs, nil
}

func splitAttributes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("attribute list is empty")
	}

	var (
		parts  []string
		start  int
		quoted bool
	)
	for i, char := range raw {
		switch char {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				part := strings.TrimSpace(raw[start:i])
				if part == "" {
					return nil, fmt.Errorf("empty attribute")
				}
				parts = append(parts, part)
				start = i + 1
			}
		}
	}
	if quoted {
		return nil, fmt.Errorf("unterminated quoted value")
	}
	last := strings.TrimSpace(raw[start:])
	if last == "" {
		return nil, fmt.Errorf("empty attribute")
	}
	return append(parts, last), nil
}

func resolveURL(base *url.URL, reference string) (string, error) {
	ref, err := url.Parse(reference)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func invalidError(message string, err error) *Error {
	return &Error{Code: CodeInvalidPlaylist, Message: message, Err: err}
}
