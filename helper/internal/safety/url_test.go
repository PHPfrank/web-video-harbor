package safety

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

type fakeResolver struct {
	answers map[string][]net.IPAddr
	err     error
	calls   int
}

func (r *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.answers[host], nil
}

func TestValidateRemoteURLAllowsPublicHTTPSURL(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"media.example.com": {{IP: net.ParseIP("93.184.216.34")}},
	}}

	got, err := ValidateRemoteURL(context.Background(), "https://media.example.com/video.mp4", resolver)
	if err != nil {
		t.Fatalf("ValidateRemoteURL() error = %v", err)
	}
	if got.String() != "https://media.example.com/video.mp4" {
		t.Fatalf("ValidateRemoteURL() = %q", got)
	}
}

func TestValidateRemoteURLAllowsGloballyReachableProtocolAssignmentAddresses(t *testing.T) {
	tests := []string{
		"http://192.0.0.9/video.mp4",
		"https://192.0.0.10/video.m3u8",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			got, err := ValidateRemoteURL(context.Background(), rawURL, &fakeResolver{})
			if err != nil {
				t.Fatalf("ValidateRemoteURL() error = %v", err)
			}
			if got.String() != rawURL {
				t.Fatalf("ValidateRemoteURL() = %q, want %q", got, rawURL)
			}
		})
	}
}

func TestValidateRemoteURLRejectsInvalidURLs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "file scheme", raw: "file:///tmp/video.mp4", code: CodeSchemeNotAllowed},
		{name: "ftp scheme", raw: "ftp://media.example.com/video.mp4", code: CodeSchemeNotAllowed},
		{name: "missing host", raw: "https:///video.mp4", code: CodeHostRequired},
		{name: "credentials", raw: "https://user:password@media.example.com/video.mp4", code: CodeCredentialsNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeResolver{answers: map[string][]net.IPAddr{
				"media.example.com": {{IP: net.ParseIP("93.184.216.34")}},
			}}
			_, err := ValidateRemoteURL(context.Background(), tt.raw, resolver)
			assertValidationCode(t, err, tt.code)
		})
	}
}

func TestValidateRemoteURLRejectsNonPublicTargets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "localhost", raw: "http://localhost/video.mp4"},
		{name: "IPv4 loopback", raw: "http://127.0.0.1/video.mp4"},
		{name: "IPv6 loopback", raw: "http://[::1]/video.mp4"},
		{name: "10 slash 8", raw: "http://10.1.2.3/video.mp4"},
		{name: "172.16 slash 12 lower", raw: "http://172.16.0.1/video.mp4"},
		{name: "172.16 slash 12 upper", raw: "http://172.31.255.254/video.mp4"},
		{name: "192.168 slash 16", raw: "http://192.168.1.5/video.mp4"},
		{name: "IPv4 link local", raw: "http://169.254.1.2/video.mp4"},
		{name: "IPv6 link local", raw: "http://[fe80::1]/video.mp4"},
		{name: "IPv4 multicast", raw: "http://224.0.0.1/video.mp4"},
		{name: "IPv6 multicast", raw: "http://[ff02::1]/video.mp4"},
		{name: "IPv4 unspecified", raw: "http://0.0.0.0/video.mp4"},
		{name: "IPv6 unspecified", raw: "http://[::]/video.mp4"},
		{name: "IPv6 unique local", raw: "http://[fd00::1]/video.mp4"},
		{name: "carrier grade NAT", raw: "http://100.64.0.1/video.mp4"},
		{name: "IPv4 documentation", raw: "http://192.0.2.1/video.mp4"},
		{name: "IPv4 benchmarking", raw: "http://198.18.0.1/video.mp4"},
		{name: "IPv4 reserved", raw: "http://240.0.0.1/video.mp4"},
		{name: "IPv6 documentation", raw: "http://[2001:db8::1]/video.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateRemoteURL(context.Background(), tt.raw, &fakeResolver{})
			assertValidationCode(t, err, CodeAddressNotPublic)
		})
	}
}

func TestValidateRemoteURLRequiresEveryResolvedAddressToBePublic(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"mixed.example.com": {
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("192.168.1.10")},
		},
	}}

	_, err := ValidateRemoteURL(context.Background(), "https://mixed.example.com/video.m3u8", resolver)
	assertValidationCode(t, err, CodeAddressNotPublic)
}

func TestValidateRemoteURLRejectsResolutionFailuresAndEmptyAnswers(t *testing.T) {
	t.Run("resolver error", func(t *testing.T) {
		resolver := &fakeResolver{err: errors.New("temporary DNS failure")}
		_, err := ValidateRemoteURL(context.Background(), "https://media.example.com/video.mp4", resolver)
		assertValidationCode(t, err, CodeResolveFailed)
	})

	t.Run("empty answer", func(t *testing.T) {
		resolver := &fakeResolver{answers: map[string][]net.IPAddr{"media.example.com": {}}}
		_, err := ValidateRemoteURL(context.Background(), "https://media.example.com/video.mp4", resolver)
		assertValidationCode(t, err, CodeResolveFailed)
	})
}

func TestSafeRedirectPolicyRevalidatesEveryTarget(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"cdn.example.com": {{IP: net.ParseIP("93.184.216.34")}},
	}}
	checkRedirect := SafeRedirectPolicy(resolver)

	publicRequest, err := http.NewRequest(http.MethodGet, "https://cdn.example.com/next.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRedirect(publicRequest, nil); err != nil {
		t.Fatalf("public redirect rejected: %v", err)
	}

	privateRequest, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/private", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRedirect(privateRequest, []*http.Request{publicRequest}); err == nil {
		t.Fatal("private redirect accepted")
	}

	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 for public hostname redirect", resolver.calls)
	}
}

func TestValidationErrorKeepsUserMessageSeparateFromInternalDetail(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("dial udp: internal resolver detail")}
	_, err := ValidateRemoteURL(context.Background(), "https://media.example.com/video.mp4", resolver)

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationErr.Code != CodeResolveFailed {
		t.Fatalf("code = %q", validationErr.Code)
	}
	if validationErr.Message != "无法确认下载地址是否安全" {
		t.Fatalf("message = %q", validationErr.Message)
	}
	if validationErr.Detail == "" || validationErr.Detail == validationErr.Message {
		t.Fatalf("detail was not kept separate: %q", validationErr.Detail)
	}
	if validationErr.Error() != validationErr.Message {
		t.Fatalf("Error() = %q, want user-facing message", validationErr.Error())
	}
}

func assertValidationCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want validation code %q", want)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationErr.Code != want {
		t.Fatalf("code = %q, want %q (error: %v)", validationErr.Code, want, err)
	}
}
