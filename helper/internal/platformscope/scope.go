// Package platformscope classifies the page context of experimental platforms.
package platformscope

import (
	"net/url"
	"strings"
)

var experimentalRoots = []string{
	"youtube.com",
	"bilibili.com",
	"weixin.qq.com",
	"wechat.com",
}

// IsExperimentalPage reports whether rawURL is a trusted HTTPS page URL for
// one of the bundled experimental platforms. Media paths and query text do not
// influence classification.
func IsExperimentalPage(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || !asciiHost(host) || !asciiHost(parsed.Host) {
		return false
	}
	if host == "youtu.be" {
		return true
	}
	for _, root := range experimentalRoots {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

func asciiHost(host string) bool {
	for i := 0; i < len(host); i++ {
		c := host[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}
