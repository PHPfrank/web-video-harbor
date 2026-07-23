package hlsproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/safety"
)

type publicResolver struct{}

func (publicResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestProxyBindsLoopbackUsesOpaqueTokenAndLimitsMethods(t *testing.T) {
	proxy := startTestProxy(t, "https://cdn.example/root.m3u8?root-secret", mediaPlaylist("segment.ts?segment-secret"), nil)
	rootURL := proxy.URL()
	parsed, err := url.Parse(rootURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatalf("proxy URL = %q, want random IPv4 loopback port", rootURL)
	}
	if strings.Contains(rootURL, "cdn.example") || strings.Contains(rootURL, "secret") || len(parsed.Path) < 40 {
		t.Fatalf("proxy URL is not opaque: %q", rootURL)
	}

	response, err := http.Get(rootURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", response.StatusCode)
	}
	headRequest, _ := http.NewRequest(http.MethodHead, rootURL, nil)
	response, err = http.DefaultClient.Do(headRequest)
	if err != nil {
		t.Fatal(err)
	}
	headBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(headBody) != 0 || response.Header.Get("Content-Length") == "" {
		t.Fatalf("HEAD response = status %d, body %q, headers %#v", response.StatusCode, headBody, response.Header)
	}

	request, _ := http.NewRequest(http.MethodPost, rootURL, nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", response.StatusCode)
	}

	badURL := *parsed
	parts := strings.Split(strings.TrimPrefix(badURL.Path, "/"), "/")
	parts[0] = strings.Repeat("x", len(parts[0]))
	badURL.Path = "/" + strings.Join(parts, "/")
	response, err = http.Get(badURL.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("bad-token status = %d, want 404", response.StatusCode)
	}
}

func TestProxyNeverRefetchesCallerRootManifest(t *testing.T) {
	var rootRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/root.m3u8" {
			rootRequests.Add(1)
			_, _ = io.WriteString(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:4,\nchanged.ts\n")
			return
		}
		_, _ = io.WriteString(writer, "segment")
	}))
	defer upstream.Close()

	proxy := startTestProxy(t, upstream.URL+"/root.m3u8?root-secret", mediaPlaylist("segment.ts?segment-secret"), upstream.Client())
	body := getBody(t, proxy.URL())
	if rootRequests.Load() != 0 {
		t.Fatalf("caller root was fetched %d times", rootRequests.Load())
	}
	if strings.Contains(body, upstream.URL) || strings.Contains(body, "segment-secret") {
		t.Fatalf("rewritten root leaks upstream URL: %q", body)
	}
}

func TestProxyRewritesRelativeQueryAndURIAttributes(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		secrets  []string
	}{
		{
			name: "master URI lines and playlist attributes",
			manifest: "#EXTM3U\n" +
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"main\",URI=\"audio/index.m3u8?audio-secret\"\n" +
				"#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,URI=\"?iframe-secret\"\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=1000\nvideo/index.m3u8?variant-secret\n",
			secrets: []string{"audio-secret", "iframe-secret", "variant-secret"},
		},
		{
			name: "media URI lines and binary attributes",
			manifest: "#EXTM3U\n" +
				"#EXT-X-MAP:URI=\"init.mp4?init-secret\"\n" +
				"#EXT-X-KEY:METHOD=NONE,URI=\"unused.key?key-secret\"\n" +
				"#EXTINF:4,\nsegment.ts?segment-secret\n",
			secrets: []string{"init-secret", "key-secret", "segment-secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := startTestProxy(t, "https://cdn.example/path/root.m3u8?root-secret", tt.manifest, nil)
			body := getBody(t, proxy.URL())
			if count := strings.Count(body, "http://127.0.0.1:"); count < len(tt.secrets) {
				t.Fatalf("rewritten proxy URLs = %d, want at least %d: %q", count, len(tt.secrets), body)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(body, secret) {
					t.Errorf("rewritten playlist leaked %q: %q", secret, body)
				}
			}
		})
	}
}

func TestProxyNormalizesParserAcceptedBOMInRootPlaylist(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "attached to header", manifest: "\ufeff" + mediaPlaylist("segment.ts")},
		{name: "standalone before blank lines", manifest: "\ufeff\n\n" + mediaPlaylist("segment.ts")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := startTestProxy(t, "https://cdn.example/root.m3u8", tt.manifest, nil)
			body := getBody(t, proxy.URL())
			if strings.Contains(body, "\ufeff") {
				t.Fatalf("rewritten root retained BOM: %q", body)
			}
			if _, err := hls.ParseBytes(proxy.URL(), []byte(body)); err != nil {
				t.Fatalf("rewritten root is not parser-valid: %v; body %q", err, body)
			}
		})
	}
}

func TestProxyNormalizesParserAcceptedBOMInChildPlaylist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "\ufeff"+mediaPlaylist("segment.ts"))
	}))
	defer upstream.Close()
	root := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n"
	proxy := startTestProxy(t, upstream.URL+"/root.m3u8", root, upstream.Client())
	childURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
	body := getBody(t, childURL)
	if strings.Contains(body, "\ufeff") {
		t.Fatalf("rewritten child retained BOM: %q", body)
	}
	if _, err := hls.ParseBytes(childURL, []byte(body)); err != nil {
		t.Fatalf("rewritten child is not parser-valid: %v; body %q", err, body)
	}
}

func TestProxyRejectsPrivateVariantSegmentAndMapBeforeUpstream(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "variant", manifest: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttp://127.0.0.1/private.m3u8?secret\n"},
		{name: "segment", manifest: mediaPlaylist("http://127.0.0.1/private.ts?secret")},
		{name: "map", manifest: "#EXTM3U\n#EXT-X-MAP:URI=\"http://127.0.0.1/init.mp4?secret\"\n#EXTINF:4,\nsegment.ts\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, err := Start(context.Background(), Config{
				SourceURL: "https://cdn.example/root.m3u8",
				Manifest:  []byte(tt.manifest),
				Resolver:  publicResolver{},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer closeProxy(t, proxy)
			resourceURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
			response, err := http.Get(resourceURL)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("resource status = %d, want 502", response.StatusCode)
			}
			assertProxyCode(t, proxy.Err(), CodeUnsafeSource)
			if strings.Contains(proxy.Err().Error(), "127.0.0.1") || strings.Contains(proxy.Err().Error(), "secret") {
				t.Fatalf("proxy error leaked upstream URL: %v", proxy.Err())
			}
		})
	}
}

func TestProxyRejectsEncryptedChildPlaylist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key?key-secret\"\n#EXTINF:4,\nsegment.ts\n")
	}))
	defer upstream.Close()
	root := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8?child-secret\n"
	proxy := startTestProxy(t, upstream.URL+"/root.m3u8", root, upstream.Client())
	childURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
	response, err := http.Get(childURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("child status = %d, want 502", response.StatusCode)
	}
	assertProxyCode(t, proxy.Err(), CodeEncrypted)
	if strings.Contains(proxy.Err().Error(), "secret") || strings.Contains(proxy.Err().Error(), upstream.URL) {
		t.Fatalf("proxy error leaked signed URL: %v", proxy.Err())
	}
}

func TestProxyTreatsImageStreamURIAsChildPlaylist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:4,\nimage.ts\n")
	}))
	defer upstream.Close()
	root := "#EXTM3U\n" +
		"#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=1,URI=\"images.m3u8?image-secret\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2\nvideo.m3u8\n"
	proxy := startTestProxy(t, upstream.URL+"/root.m3u8", root, upstream.Client())
	imagePlaylistURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
	response, err := http.Get(imagePlaylistURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("image child status = %d, want 502", response.StatusCode)
	}
	assertProxyCode(t, proxy.Err(), CodeEncrypted)
}

func TestProxyRejectsMalformedAndOversizedChildPlaylists(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Code
	}{
		{name: "malformed", body: "not-an-hls-playlist\n", want: CodeManifest},
		{name: "oversized", body: strings.Repeat("x", maxPlaylistBytes+1), want: CodeTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(writer, tt.body)
			}))
			defer upstream.Close()
			root := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8?child-secret\n"
			proxy := startTestProxy(t, upstream.URL+"/root.m3u8", root, upstream.Client())
			childURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
			response, err := http.Get(childURL)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("child status = %d, want 502", response.StatusCode)
			}
			assertProxyCode(t, proxy.Err(), tt.want)
			if strings.Contains(proxy.Err().Error(), "child-secret") || strings.Contains(proxy.Err().Error(), upstream.URL) {
				t.Fatalf("proxy error leaked signed URL: %v", proxy.Err())
			}
		})
	}
}

func TestProxyForwardsOnlyRangeAndSafeResponseMetadata(t *testing.T) {
	requestHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestHeaders <- request.Header.Clone()
		writer.Header().Set("Content-Type", "video/mp2t")
		writer.Header().Set("Content-Range", "bytes 2-4/8")
		writer.Header().Set("Accept-Ranges", "bytes")
		writer.Header().Set("X-Upstream-Secret", "do-not-copy")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(writer, "234")
	}))
	defer upstream.Close()
	proxy := startTestProxy(t, upstream.URL+"/root.m3u8", mediaPlaylist("segment.ts"), upstream.Client())
	segmentURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
	request, _ := http.NewRequest(http.MethodGet, segmentURL, nil)
	request.Header.Set("Range", "bytes=2-4")
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || string(body) != "234" {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("Content-Type") != "video/mp2t" || response.Header.Get("Content-Length") != "3" || response.Header.Get("Content-Range") != "bytes 2-4/8" || response.Header.Get("Accept-Ranges") != "bytes" || response.Header.Get("X-Upstream-Secret") != "" {
		t.Fatalf("response headers = %#v", response.Header)
	}
	upstreamHeaders := <-requestHeaders
	if upstreamHeaders.Get("Range") != "bytes=2-4" || upstreamHeaders.Get("Cookie") != "" || upstreamHeaders.Get("Authorization") != "" {
		t.Fatalf("upstream headers = %#v", upstreamHeaders)
	}
}

func TestProxyStripsRefererAcrossUpstreamRedirects(t *testing.T) {
	targetHeaders := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetHeaders <- request.Header.Clone()
		_, _ = io.WriteString(writer, "segment")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/final.ts", http.StatusFound)
	}))
	defer redirector.Close()

	proxy := startTestProxy(t, redirector.URL+"/root.m3u8", mediaPlaylist("segment.ts?source-secret"), redirector.Client())
	segmentURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
	request, _ := http.NewRequest(http.MethodGet, segmentURL, nil)
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("redirected segment status = %d", response.StatusCode)
	}
	headers := <-targetHeaders
	if headers.Get("Referer") != "" || headers.Get("Cookie") != "" || headers.Get("Authorization") != "" {
		t.Fatalf("redirect target received sensitive headers: %#v", headers)
	}
}

func TestProxyClassifiesUpstreamRequestErrors(t *testing.T) {
	tests := []struct {
		name       string
		requestErr error
		want       Code
		canceled   bool
	}{
		{
			name:       "safety validation",
			requestErr: &safety.ValidationError{Code: safety.CodeAddressNotPublic, Message: "地址不安全", Detail: "private-secret"},
			want:       CodeUnsafeSource,
		},
		{name: "context canceled", requestErr: context.Canceled, want: Code("canceled"), canceled: true},
		{name: "TLS failure", requestErr: errors.New("TLS handshake failed for signed-secret"), want: CodeUpstream},
		{name: "timeout", requestErr: errors.New("upstream timeout for signed-secret"), want: CodeUpstream},
		{name: "connect failure", requestErr: errors.New("connect failed for signed-secret"), want: CodeUpstream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, tt.requestErr
			})}
			proxy := startTestProxy(t, "https://cdn.example/root.m3u8", mediaPlaylist("segment.ts?source-secret"), client)
			segmentURL := firstProxyResource(t, getBody(t, proxy.URL()), proxy.URL())
			response, err := http.Get(segmentURL)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("resource status = %d, want 502", response.StatusCode)
			}
			proxyErr := proxy.Err()
			assertProxyCode(t, proxyErr, tt.want)
			if errors.Is(proxyErr, context.Canceled) != tt.canceled {
				t.Fatalf("errors.Is(context.Canceled) = %v, want %v", errors.Is(proxyErr, context.Canceled), tt.canceled)
			}
			if strings.Contains(proxyErr.Error(), "secret") {
				t.Fatalf("proxy error leaked upstream detail: %v", proxyErr)
			}
		})
	}
}

func TestProxyCloseStopsListenerAndIsIdempotent(t *testing.T) {
	proxy := startTestProxy(t, "https://cdn.example/root.m3u8", mediaPlaylist("segment.ts"), nil)
	rootURL := proxy.URL()
	closeProxy(t, proxy)
	closeProxy(t, proxy)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	if response, err := client.Get(rootURL); err == nil {
		_ = response.Body.Close()
		t.Fatalf("listener still accepted requests with status %d", response.StatusCode)
	}
}

func TestProxyParentCancellationStopsListenerAndCloseStillWaitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	proxy, err := start(ctx, internalConfig{
		sourceURL:         "https://cdn.example/root.m3u8",
		manifest:          []byte(mediaPlaylist("segment.ts")),
		resolver:          publicResolver{},
		skipURLValidation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := proxy.URL()
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		client := &http.Client{Timeout: 100 * time.Millisecond}
		response, requestErr := client.Get(rootURL)
		if requestErr != nil {
			break
		}
		_ = response.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener remained available after parent cancellation")
		}
	}
	closeProxy(t, proxy)
}

func startTestProxy(t *testing.T, sourceURL, manifest string, client *http.Client) *Proxy {
	t.Helper()
	proxy, err := start(context.Background(), internalConfig{
		sourceURL:         sourceURL,
		manifest:          []byte(manifest),
		resolver:          publicResolver{},
		client:            client,
		skipURLValidation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeProxy(t, proxy) })
	return proxy
}

func closeProxy(t *testing.T, proxy *Proxy) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := proxy.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func getBody(t *testing.T, rawURL string) string {
	t.Helper()
	response, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d body = %q", rawURL, response.StatusCode, body)
	}
	return string(body)
}

func firstProxyResource(t *testing.T, playlist, rootURL string) string {
	t.Helper()
	base, err := url.Parse(rootURL)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "http://127.0.0.1:"
	index := strings.Index(playlist, prefix)
	if index < 0 {
		t.Fatalf("playlist has no proxy resource: %q", playlist)
	}
	end := index
	for end < len(playlist) && !strings.ContainsRune("\r\n\"", rune(playlist[end])) {
		end++
	}
	resource, err := url.Parse(playlist[index:end])
	if err != nil {
		t.Fatal(err)
	}
	if resource.Host != base.Host {
		t.Fatalf("resource host = %q, proxy host = %q", resource.Host, base.Host)
	}
	return resource.String()
}

func mediaPlaylist(resource string) string {
	return "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\n" + resource + "\n#EXT-X-ENDLIST\n"
}

func assertProxyCode(t *testing.T, err error, want Code) {
	t.Helper()
	var proxyErr *Error
	if !errors.As(err, &proxyErr) {
		t.Fatalf("error = %v (%T), want *Error", err, err)
	}
	if proxyErr.Code != want {
		t.Fatalf("error code = %q, want %q", proxyErr.Code, want)
	}
}
