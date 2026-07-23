package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"web-video-downloader/helper/internal/hls"
	"web-video-downloader/helper/internal/media"
	"web-video-downloader/helper/internal/safety"
)

const maxInspectBody = 2 * 1024 * 1024

// productionInspector uses only the SSRF-safe transport. The private
// constructor is the sole seam allowed to use an httptest client.
type productionInspector struct {
	client       *http.Client
	resolver     safety.Resolver
	validateURLs bool
}

// NewMediaInspector builds a production-safe remote media inspector.
func NewMediaInspector(resolver safety.Resolver) MediaInspector {
	return &productionInspector{
		client: &http.Client{
			Transport:     safety.NewSafeTransport(resolver, nil),
			CheckRedirect: safety.SafeRedirectPolicy(resolver),
			Timeout:       30 * time.Second,
		},
		resolver: resolver, validateURLs: true,
	}
}

func newMediaInspectorForTest(client *http.Client) *productionInspector {
	return &productionInspector{client: client}
}

func (i *productionInspector) Inspect(ctx context.Context, rawURL string) (Inspection, error) {
	if i.validateURLs {
		if _, err := safety.ValidateRemoteURL(ctx, rawURL, i.resolver); err != nil {
			return Inspection{}, inspectFailure(InspectUnsafe, "视频地址不安全或无效", http.StatusBadRequest, "source validation failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Inspection{}, inspectFailure(InspectUnsafe, "视频地址格式无效", http.StatusBadRequest, "build inspection request failed")
	}
	req.Header.Set("Accept", "video/mp4,application/vnd.apple.mpegurl,application/x-mpegurl;q=0.9,*/*;q=0.1")
	resp, err := i.client.Do(req)
	if err != nil {
		return Inspection{}, inspectFailure(InspectNetwork, "无法读取视频地址", http.StatusBadGateway, "inspection request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Inspection{}, &InspectError{Code: InspectHTTP, Message: "视频服务器拒绝了检查请求", Status: http.StatusBadGateway, Err: fmt.Errorf("inspection HTTP status %d", resp.StatusCode)}
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	kind := media.Classify(finalURL, resp.Header.Get("Content-Type"))
	if kind == media.MP4 {
		return Inspection{MediaType: "mp4"}, nil
	}
	if kind != media.HLS {
		return Inspection{}, inspectFailure(InspectUnsupported, "未识别到支持的视频格式", http.StatusUnsupportedMediaType, "unsupported response media type")
	}
	manifest, err := io.ReadAll(io.LimitReader(resp.Body, maxInspectBody+1))
	if err != nil {
		return Inspection{}, inspectFailure(InspectNetwork, "无法读取视频清单", http.StatusBadGateway, "read manifest failed")
	}
	if len(manifest) > maxInspectBody {
		return Inspection{}, inspectFailure(InspectTooLarge, "视频清单过大", http.StatusBadGateway, "manifest exceeds size limit")
	}
	playlist, err := hls.ParseBytes(finalURL, manifest)
	if err != nil {
		var playlistErr *hls.Error
		if errors.As(err, &playlistErr) {
			switch playlistErr.Code {
			case hls.CodeUnsupportedEncryption:
				return Inspection{}, inspectFailure(InspectEncrypted, "不支持加密或 DRM 视频", http.StatusUnprocessableEntity, "encrypted manifest")
			case hls.CodePlaylistTooLarge:
				return Inspection{}, inspectFailure(InspectTooLarge, "视频清单过大", http.StatusBadGateway, "manifest exceeds parser size limit")
			}
		}
		return Inspection{}, inspectFailure(InspectMalformed, "视频清单格式无效", http.StatusUnprocessableEntity, "manifest parse failed")
	}
	return Inspection{MediaType: "hls", Variants: playlist.Variants}, nil
}

func inspectFailure(code InspectCode, message string, status int, diagnostic string) *InspectError {
	return &InspectError{Code: code, Message: message, Status: status, Err: errors.New(diagnostic)}
}
