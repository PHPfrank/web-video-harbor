package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"web-video-harbor/helper/internal/safety"
)

func TestMediaInspectorClassifiesMP4AndParsesHLS(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720\n720/index.m3u8\n"
	for _, tc := range []struct {
		name, contentType, body, wantType string
		wantVariants                      int
	}{
		{name: "mp4", contentType: "video/mp4", body: "video", wantType: "mp4"},
		{name: "hls", contentType: "application/vnd.apple.mpegurl", body: master, wantType: "hls", wantVariants: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: apiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: request}, nil
			})}
			inspector := newMediaInspectorForTest(client)
			got, err := inspector.Inspect(context.Background(), "https://media.example"+map[bool]string{true: "/master.m3u8", false: "/video.mp4"}[tc.wantType == "hls"])
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if got.MediaType != tc.wantType || len(got.Variants) != tc.wantVariants {
				t.Fatalf("Inspect() = %#v", got)
			}
			if tc.wantVariants == 1 && (got.Variants[0].Label != "720p" || !strings.HasSuffix(got.Variants[0].URL, "/720/index.m3u8")) {
				t.Fatalf("variant = %#v", got.Variants[0])
			}
		})
	}
}

func TestMediaInspectorResolvesVariantsAgainstFinalResponseURL(t *testing.T) {
	finalURL, err := url.Parse("https://cdn.example/path/master.m3u8?signature=hidden")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: apiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		responseRequest := request.Clone(request.Context())
		responseRequest.URL = finalURL
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}},
			Body:       io.NopCloser(strings.NewReader("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nvariant.m3u8\n")),
			Request:    responseRequest,
		}, nil
	})}
	got, err := newMediaInspectorForTest(client).Inspect(context.Background(), "https://origin.example/root.m3u8")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(got.Variants) != 1 || got.Variants[0].URL != "https://cdn.example/path/variant.m3u8" {
		t.Fatalf("variants = %#v", got.Variants)
	}
}

func TestMediaInspectorReturnsTypedSafeErrors(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxInspectBody+1)
	tests := []struct {
		name, contentType string
		status            int
		body              []byte
		code              InspectCode
	}{
		{name: "status", status: http.StatusForbidden, contentType: "video/mp4", code: InspectHTTP},
		{name: "unsupported", status: http.StatusOK, contentType: "text/html", body: []byte("<html>"), code: InspectUnsupported},
		{name: "malformed", status: http.StatusOK, contentType: "application/vnd.apple.mpegurl", body: []byte("not hls"), code: InspectMalformed},
		{name: "encrypted", status: http.StatusOK, contentType: "application/vnd.apple.mpegurl", body: []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"secret.key\"\n#EXTINF:4,\nseg.ts\n"), code: InspectEncrypted},
		{name: "oversized", status: http.StatusOK, contentType: "application/vnd.apple.mpegurl", body: oversized, code: InspectTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: apiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(bytes.NewReader(tc.body)), Request: request}, nil
			})}
			_, err := newMediaInspectorForTest(client).Inspect(context.Background(), "https://media.example/master.m3u8?token=signed-secret")
			var inspectErr *InspectError
			if !errors.As(err, &inspectErr) || inspectErr.Code != tc.code {
				t.Fatalf("error = %T %v, want code %q", err, err, tc.code)
			}
			for _, secret := range []string{"signed-secret", "media.example"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestProductionMediaInspectorRejectsPrivateTargetsBeforeRequest(t *testing.T) {
	inspector := NewMediaInspector(apiStaticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}})
	_, err := inspector.Inspect(context.Background(), "https://private.example/video.mp4?token=secret")
	var inspectErr *InspectError
	if !errors.As(err, &inspectErr) || inspectErr.Code != InspectUnsafe {
		t.Fatalf("error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), "private.example") || strings.Contains(err.Error(), "token=secret") {
		t.Fatalf("unsafe error leaked URL: %v", err)
	}

	production := inspector.(*productionInspector)
	transport, ok := production.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("production transport is not safe: %T proxySet=%t", production.client.Transport, transport.Proxy != nil)
	}
	if production.client.CheckRedirect == nil {
		t.Fatal("production redirect policy is missing")
	}
	if production.client.Timeout <= 0 || production.client.Timeout > time.Minute {
		t.Fatalf("production client timeout = %s", production.client.Timeout)
	}
}

type apiStaticResolver struct{ addresses []net.IPAddr }

func (r apiStaticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

var _ safety.Resolver = apiStaticResolver{}

type apiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f apiRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
