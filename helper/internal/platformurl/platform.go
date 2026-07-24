package platformurl

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const maxURLLength = 2048

var (
	youtubeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	bilibiliIDPattern = regexp.MustCompile(`^(?:BV[A-Za-z0-9]{10}|av[1-9][0-9]*)$`)
	partPattern       = regexp.MustCompile(`^[0-9]+$`)
	errUnsupported    = errors.New("unsupported platform video URL")
)

type Provider string

const (
	YouTube  Provider = "youtube"
	Bilibili Provider = "bilibili"
)

type Video struct {
	Provider     Provider
	CanonicalURL string
}

func Classify(raw string) (Video, error) {
	if raw == "" || len(raw) > maxURLLength {
		return Video{}, errUnsupported
	}

	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return Video{}, errUnsupported
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return Video{}, errUnsupported
	}
	if strings.Contains(parsed.EscapedPath(), "%") {
		return Video{}, errUnsupported
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return Video{}, errUnsupported
	}

	switch parsed.Host {
	case "www.youtube.com", "youtube.com", "youtu.be":
		return classifyYouTube(parsed)
	case "www.bilibili.com":
		return classifyBilibili(parsed)
	default:
		return Video{}, errUnsupported
	}
}

func classifyYouTube(parsed *url.URL) (Video, error) {
	if parsed.Host == "youtu.be" {
		id, ok := pathID(parsed.Path, "")
		if !ok || !youtubeIDPattern.MatchString(id) {
			return Video{}, errUnsupported
		}
		return Video{Provider: YouTube, CanonicalURL: "https://youtu.be/" + id}, nil
	}

	if parsed.Path == "/watch" {
		query, err := url.ParseQuery(parsed.RawQuery)
		id, literal := literalQueryValue(parsed.RawQuery, "v", youtubeIDPattern)
		if err != nil || len(query["v"]) != 1 || !literal {
			return Video{}, errUnsupported
		}
		return Video{
			Provider:     YouTube,
			CanonicalURL: "https://www.youtube.com/watch?v=" + id,
		}, nil
	}

	id, ok := pathID(parsed.Path, "/shorts")
	if !ok || !youtubeIDPattern.MatchString(id) {
		return Video{}, errUnsupported
	}
	return Video{
		Provider:     YouTube,
		CanonicalURL: "https://www.youtube.com/shorts/" + id,
	}, nil
}

func classifyBilibili(parsed *url.URL) (Video, error) {
	id, ok := pathID(parsed.Path, "/video")
	if !ok || !bilibiliIDPattern.MatchString(id) {
		return Video{}, errUnsupported
	}

	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query["p"]) > 1 {
		return Video{}, errUnsupported
	}
	canonical := "https://www.bilibili.com/video/" + id
	if len(query["p"]) == 1 {
		part, literal := literalQueryValue(parsed.RawQuery, "p", partPattern)
		if !literal {
			return Video{}, errUnsupported
		}
		canonical += "?p=" + part
	}

	return Video{Provider: Bilibili, CanonicalURL: canonical}, nil
}

func literalQueryValue(rawQuery, key string, pattern *regexp.Regexp) (string, bool) {
	prefix := key + "="
	var value string
	count := 0
	for _, field := range strings.Split(rawQuery, "&") {
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		count++
		value = strings.TrimPrefix(field, prefix)
		if !pattern.MatchString(value) {
			return "", false
		}
	}
	return value, count == 1
}

func pathID(path, prefix string) (string, bool) {
	if prefix != "" {
		if !strings.HasPrefix(path, prefix+"/") {
			return "", false
		}
		path = strings.TrimPrefix(path, prefix)
	}
	path = strings.TrimSuffix(path, "/")
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	id := strings.TrimPrefix(path, "/")
	return id, id != "" && !strings.Contains(id, "/")
}
