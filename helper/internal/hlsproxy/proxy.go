// Package hlsproxy exposes caller-preflighted HLS through an opaque loopback
// URL while fetching every nested resource with the helper's safe transport.
package hlsproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/safety"
)

const (
	maxPlaylistBytes = 2 * 1024 * 1024
	maxRangeBytes    = 256
)

type Code string

const (
	CodeUnsafeSource Code = "unsafe_source"
	CodeManifest     Code = "invalid_manifest"
	CodeEncrypted    Code = "encrypted_hls"
	CodeUpstream     Code = "upstream"
	CodeTooLarge     Code = "playlist_too_large"
	CodeLifecycle    Code = "lifecycle"
)

type Error struct {
	Code    Code
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

type Config struct {
	SourceURL string
	Manifest  []byte
	Resolver  safety.Resolver
}

type internalConfig struct {
	sourceURL         string
	manifest          []byte
	resolver          safety.Resolver
	client            *http.Client
	skipURLValidation bool
}

type resourceKind uint8

const (
	resourceBinary resourceKind = iota
	resourcePlaylist
)

type resource struct {
	url  string
	kind resourceKind
}

type Proxy struct {
	rootURL   string
	rootBody  []byte
	token     string
	host      string
	listener  net.Listener
	server    *http.Server
	client    *http.Client
	transport *http.Transport
	resolver  safety.Resolver
	skipCheck bool
	cancel    context.CancelFunc
	done      chan error

	resourcesMu sync.RWMutex
	resources   map[string]resource
	reverse     map[string]string

	errorMu  sync.RWMutex
	firstErr error

	closeOnce sync.Once
	closeErr  error
}

func Start(ctx context.Context, config Config) (*Proxy, error) {
	return start(ctx, internalConfig{
		sourceURL: config.SourceURL,
		manifest:  config.Manifest,
		resolver:  config.Resolver,
	})
}

func start(ctx context.Context, config internalConfig) (*Proxy, error) {
	if ctx == nil {
		return nil, &Error{Code: CodeLifecycle, Message: "无法启动安全视频代理", cause: context.Canceled}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Code: CodeLifecycle, Message: "无法启动安全视频代理", cause: err}
	}
	if len(config.manifest) == 0 || len(config.manifest) > maxPlaylistBytes {
		return nil, playlistSizeOrManifestError(len(config.manifest))
	}
	if err := inspectPlaylist(config.sourceURL, config.manifest); err != nil {
		return nil, err
	}
	base, err := url.Parse(config.sourceURL)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return nil, &Error{Code: CodeUnsafeSource, Message: "视频下载地址不安全或无效", cause: errors.New("source URL is invalid")}
	}
	if !config.skipURLValidation {
		if _, err := safety.ValidateRemoteURL(ctx, config.sourceURL, config.resolver); err != nil {
			return nil, &Error{Code: CodeUnsafeSource, Message: "视频下载地址不安全或无效", cause: errors.New("source validation failed")}
		}
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, &Error{Code: CodeLifecycle, Message: "无法启动安全视频代理", cause: errors.New("listen failed")}
	}
	token, err := randomID(32)
	if err != nil {
		_ = listener.Close()
		return nil, &Error{Code: CodeLifecycle, Message: "无法启动安全视频代理", cause: errors.New("token generation failed")}
	}
	proxyCtx, cancel := context.WithCancel(ctx)
	p := &Proxy{
		token:     token,
		host:      listener.Addr().String(),
		listener:  listener,
		resolver:  config.resolver,
		skipCheck: config.skipURLValidation,
		cancel:    cancel,
		done:      make(chan error, 1),
		resources: make(map[string]resource),
		reverse:   make(map[string]string),
	}
	if config.client != nil {
		clientCopy := *config.client
		clientCopy.CheckRedirect = withoutReferer(clientCopy.CheckRedirect)
		p.client = &clientCopy
	} else {
		transport := safety.NewSafeTransport(config.resolver, nil)
		transport.DisableCompression = true
		transport.ResponseHeaderTimeout = 30 * time.Second
		p.transport = transport
		p.client = &http.Client{
			Transport:     transport,
			CheckRedirect: withoutReferer(safety.SafeRedirectPolicy(config.resolver)),
		}
	}
	p.rootURL = "http://" + p.host + "/" + p.token + "/root.m3u8"
	p.rootBody, err = p.rewritePlaylist(config.sourceURL, config.manifest)
	if err != nil {
		cancel()
		_ = listener.Close()
		return nil, err
	}
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return proxyCtx
		},
	}
	go func() {
		err := p.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		p.done <- err
	}()
	go func() {
		<-proxyCtx.Done()
		_ = p.server.Close()
	}()
	return p, nil
}

func (p *Proxy) URL() string { return p.rootURL }

func (p *Proxy) Err() error {
	p.errorMu.RLock()
	defer p.errorMu.RUnlock()
	return p.firstErr
}

func (p *Proxy) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.closeOnce.Do(func() {
		p.cancel()
		shutdownErr := p.server.Shutdown(ctx)
		if shutdownErr != nil {
			_ = p.server.Close()
		}
		if p.transport != nil {
			p.transport.CloseIdleConnections()
		}
		serveErr := <-p.done
		p.closeErr = errors.Join(shutdownErr, serveErr)
	})
	return p.closeErr
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Host != p.host || request.URL.RawQuery != "" {
		http.NotFound(writer, request)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != p.token {
		http.NotFound(writer, request)
		return
	}
	if len(parts) == 2 && parts[1] == "root.m3u8" {
		servePlaylist(writer, request, p.rootBody)
		return
	}
	if len(parts) != 3 || parts[1] != "r" {
		http.NotFound(writer, request)
		return
	}
	p.resourcesMu.RLock()
	resource, ok := p.resources[parts[2]]
	p.resourcesMu.RUnlock()
	if !ok {
		http.NotFound(writer, request)
		return
	}
	p.serveResource(writer, request, resource)
}

func (p *Proxy) serveResource(writer http.ResponseWriter, inbound *http.Request, target resource) {
	if !p.skipCheck {
		if _, err := safety.ValidateRemoteURL(inbound.Context(), target.url, p.resolver); err != nil {
			p.fail(CodeUnsafeSource, "视频子资源地址不安全", errors.New("nested target validation failed"))
			http.Error(writer, "bad gateway", http.StatusBadGateway)
			return
		}
	}
	method := inbound.Method
	if target.kind == resourcePlaylist {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(inbound.Context(), method, target.url, nil)
	if err != nil {
		p.fail(CodeUnsafeSource, "视频子资源地址无效", errors.New("nested request creation failed"))
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	if target.kind == resourceBinary {
		rangeValue := inbound.Header.Get("Range")
		if rangeValue != "" {
			if len(rangeValue) > maxRangeBytes || strings.Contains(rangeValue, ",") || !strings.HasPrefix(rangeValue, "bytes=") {
				http.Error(writer, "invalid range", http.StatusBadRequest)
				return
			}
			request.Header.Set("Range", rangeValue)
		}
	}
	response, err := p.client.Do(request)
	if err != nil {
		p.fail(CodeUnsafeSource, "无法安全读取视频子资源", errors.New("safe upstream request failed"))
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		p.fail(CodeUpstream, "视频服务器返回错误", errors.New("upstream status is not usable"))
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	if target.kind == resourcePlaylist {
		p.serveChildPlaylist(writer, inbound, response, target.url)
		return
	}
	copySafeHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	if inbound.Method != http.MethodHead {
		if _, err := io.Copy(writer, response.Body); err != nil {
			p.fail(CodeUpstream, "视频子资源传输中断", errors.New("copy upstream body failed"))
		}
	}
}

func (p *Proxy) serveChildPlaylist(writer http.ResponseWriter, inbound *http.Request, response *http.Response, fallbackURL string) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPlaylistBytes+1))
	if err != nil {
		p.fail(CodeUpstream, "无法读取子播放列表", errors.New("read child playlist failed"))
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	if len(body) > maxPlaylistBytes {
		p.fail(CodeTooLarge, "子播放列表过大", errors.New("child playlist exceeds limit"))
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	baseURL := fallbackURL
	if response.Request != nil && response.Request.URL != nil {
		baseURL = response.Request.URL.String()
	}
	if err := inspectPlaylist(baseURL, body); err != nil {
		p.setError(err)
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	rewritten, err := p.rewritePlaylist(baseURL, body)
	if err != nil {
		p.setError(err)
		http.Error(writer, "bad gateway", http.StatusBadGateway)
		return
	}
	servePlaylist(writer, inbound, rewritten)
}

func servePlaylist(writer http.ResponseWriter, request *http.Request, body []byte) {
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func copySafeHeaders(destination, source http.Header) {
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := source.Get(name); value != "" {
			destination.Set(name, value)
		}
	}
}

func withoutReferer(policy func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		request.Header.Del("Referer")
		if policy == nil {
			return nil
		}
		return policy(request, via)
	}
}

func (p *Proxy) rewritePlaylist(manifestURL string, manifest []byte) ([]byte, error) {
	base, err := url.Parse(manifestURL)
	if err != nil || !base.IsAbs() || base.Host == "" {
		return nil, &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("playlist base URL is invalid")}
	}
	lines := strings.Split(strings.ReplaceAll(string(manifest), "\r\n", "\n"), "\n")
	pendingPlaylist := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, "#EXT-X-STREAM-INF:") {
				pendingPlaylist = true
			}
			kind := resourceBinary
			if isPlaylistAttributeTag(trimmed) {
				kind = resourcePlaylist
			}
			rewritten, err := p.rewriteURIAttributes(base, line, kind)
			if err != nil {
				return nil, err
			}
			lines[index] = rewritten
			continue
		}
		kind := resourceBinary
		if pendingPlaylist {
			kind = resourcePlaylist
		}
		rewritten, err := p.registerReference(base, trimmed, kind)
		if err != nil {
			return nil, err
		}
		lines[index] = rewritten
		pendingPlaylist = false
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func (p *Proxy) rewriteURIAttributes(base *url.URL, line string, kind resourceKind) (string, error) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return line, nil
	}
	parts, err := splitAttributeParts(line[colon+1:])
	if err != nil {
		return "", &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("attribute list is malformed")}
	}
	changed := false
	for index, part := range parts {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "URI" {
			if strings.Contains(key, "URI") && strings.HasPrefix(value, "\"") {
				return "", &Error{Code: CodeManifest, Message: "M3U8 包含不支持的外部资源属性", cause: errors.New("unsupported URI-bearing attribute")}
			}
			continue
		}
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return "", &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("URI attribute is not quoted")}
		}
		rewritten, err := p.registerReference(base, value[1:len(value)-1], kind)
		if err != nil {
			return "", err
		}
		parts[index] = strings.TrimSpace(key) + "=\"" + rewritten + "\""
		changed = true
	}
	if !changed {
		return line, nil
	}
	return line[:colon+1] + strings.Join(parts, ","), nil
}

func splitAttributeParts(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var parts []string
	start := 0
	quoted := false
	for index, value := range raw {
		switch value {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, raw[start:index])
				start = index + 1
			}
		}
	}
	if quoted {
		return nil, errors.New("unterminated quote")
	}
	return append(parts, raw[start:]), nil
}

func isPlaylistAttributeTag(line string) bool {
	for _, prefix := range []string{
		"#EXT-X-MEDIA:",
		"#EXT-X-I-FRAME-STREAM-INF:",
		"#EXT-X-IMAGE-STREAM-INF:",
		"#EXT-X-RENDITION-REPORT:",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func (p *Proxy) registerReference(base *url.URL, reference string, kind resourceKind) (string, error) {
	parsed, err := url.Parse(reference)
	if err != nil {
		return "", &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("resource URI is invalid")}
	}
	resolved := base.ResolveReference(parsed)
	if resolved.Scheme != "http" && resolved.Scheme != "https" || resolved.Host == "" || resolved.User != nil {
		return "", &Error{Code: CodeUnsafeSource, Message: "视频子资源地址不安全", cause: errors.New("resource URI scheme is not allowed")}
	}
	key := resolved.String() + "\x00" + strconv.Itoa(int(kind))
	p.resourcesMu.Lock()
	defer p.resourcesMu.Unlock()
	if id, ok := p.reverse[key]; ok {
		return "http://" + p.host + "/" + p.token + "/r/" + id, nil
	}
	id, err := randomID(18)
	if err != nil {
		return "", &Error{Code: CodeLifecycle, Message: "无法注册视频子资源", cause: errors.New("resource ID generation failed")}
	}
	p.resources[id] = resource{url: resolved.String(), kind: kind}
	p.reverse[key] = id
	return "http://" + p.host + "/" + p.token + "/r/" + id, nil
}

func inspectPlaylist(manifestURL string, body []byte) error {
	text := string(body)
	if strings.Contains(text, "#EXT-X-CONTENT-STEERING") || strings.Contains(text, "#EXT-X-DEFINE") || strings.Contains(text, "{$") {
		return &Error{Code: CodeManifest, Message: "M3U8 包含不支持的动态外部资源", cause: errors.New("unsupported dynamic HLS feature")}
	}
	_, err := hls.ParseBytes(manifestURL, body)
	if err == nil {
		return nil
	}
	var playlistErr *hls.Error
	if errors.As(err, &playlistErr) && playlistErr.Code == hls.CodeUnsupportedEncryption {
		return &Error{Code: CodeEncrypted, Message: "不支持加密或 DRM 视频", cause: errors.New("HLS encryption is unsupported")}
	}
	if errors.As(err, &playlistErr) && playlistErr.Code == hls.CodePlaylistTooLarge {
		return &Error{Code: CodeTooLarge, Message: "M3U8 播放列表过大", cause: errors.New("playlist exceeds size limit")}
	}
	return &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("playlist inspection failed")}
}

func playlistSizeOrManifestError(size int) error {
	if size > maxPlaylistBytes {
		return &Error{Code: CodeTooLarge, Message: "M3U8 播放列表过大", cause: errors.New("playlist exceeds size limit")}
	}
	return &Error{Code: CodeManifest, Message: "M3U8 播放列表无效", cause: errors.New("playlist is empty")}
}

func (p *Proxy) fail(code Code, message string, cause error) {
	p.setError(&Error{Code: code, Message: message, cause: cause})
}

func (p *Proxy) setError(err error) {
	p.errorMu.Lock()
	defer p.errorMu.Unlock()
	if p.firstErr == nil {
		p.firstErr = err
	}
}

func randomID(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("random ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
