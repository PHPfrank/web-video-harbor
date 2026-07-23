package safety

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"testing"
)

type fakeResolver struct {
	answers map[string][]net.IPAddr
	err     error
	calls   int
}

type alternatingResolver struct {
	answers [][]net.IPAddr
	calls   int
}

func (r *alternatingResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	answerIndex := r.calls
	r.calls++
	if answerIndex >= len(r.answers) {
		answerIndex = len(r.answers) - 1
	}
	return r.answers[answerIndex], nil
}

type dialCall struct {
	network string
	address string
}

type recordingDialer struct {
	calls []dialCall
	err   error
}

func (d *recordingDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.calls = append(d.calls, dialCall{network: network, address: address})
	return nil, d.err
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

func TestValidateRemoteURLRejectsEveryIANANonGlobalSpecialPurposePrefix(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{name: "IPv4 this network", ip: "0.1.2.3"},
		{name: "IPv4 unspecified host", ip: "0.0.0.0"},
		{name: "IPv4 private 10", ip: "10.0.0.1"},
		{name: "IPv4 shared", ip: "100.64.0.1"},
		{name: "IPv4 loopback", ip: "127.0.0.1"},
		{name: "IPv4 link local", ip: "169.254.1.1"},
		{name: "IPv4 private 172", ip: "172.16.0.1"},
		{name: "IPv4 IETF assignments parent", ip: "192.0.0.11"},
		{name: "IPv4 service continuity", ip: "192.0.0.1"},
		{name: "IPv4 dummy", ip: "192.0.0.8"},
		{name: "IPv4 NAT64 discovery 170", ip: "192.0.0.170"},
		{name: "IPv4 NAT64 discovery 171", ip: "192.0.0.171"},
		{name: "IPv4 documentation 1", ip: "192.0.2.1"},
		{name: "IPv4 deprecated 6to4", ip: "192.88.99.1"},
		{name: "IPv4 6a44 relay", ip: "192.88.99.2"},
		{name: "IPv4 private 192", ip: "192.168.0.1"},
		{name: "IPv4 benchmarking", ip: "198.18.0.1"},
		{name: "IPv4 documentation 2", ip: "198.51.100.1"},
		{name: "IPv4 documentation 3", ip: "203.0.113.1"},
		{name: "IPv4 reserved", ip: "240.0.0.1"},
		{name: "IPv4 limited broadcast", ip: "255.255.255.255"},
		{name: "IPv6 unspecified", ip: "::"},
		{name: "IPv6 loopback", ip: "::1"},
		{name: "IPv6 local translation", ip: "64:ff9b:1::1"},
		{name: "IPv6 discard only", ip: "100::1"},
		{name: "IPv6 dummy", ip: "100:0:0:1::1"},
		{name: "IPv6 IETF assignments parent", ip: "2001:100::1"},
		{name: "IPv6 TEREDO", ip: "2001::1"},
		{name: "IPv6 benchmarking", ip: "2001:2::1"},
		{name: "IPv6 deprecated ORCHID", ip: "2001:10::1"},
		{name: "IPv6 documentation 1", ip: "2001:db8::1"},
		{name: "IPv6 6to4", ip: "2002::1"},
		{name: "IPv6 documentation 2", ip: "3fff::1"},
		{name: "IPv6 segment routing SIDs", ip: "5f00::1"},
		{name: "IPv6 unique local", ip: "fc00::1"},
		{name: "IPv6 link local", ip: "fe80::1"},
		{name: "IPv6 reserved outside global unicast allocation", ip: "fec0::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIPRejectedThroughLiteralAndResolver(t, tt.ip)
		})
	}
}

func TestValidateRemoteURLRejectsIPv4MappedIPv6Literal(t *testing.T) {
	_, err := ValidateRemoteURL(context.Background(), "http://[::ffff:8.8.8.8]/video.mp4", &fakeResolver{})
	assertValidationCode(t, err, CodeAddressNotPublic)
}

func TestValidateRemoteURLAllowsEveryIANAExplicitGlobalException(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{name: "IPv4 PCP anycast", ip: "192.0.0.9"},
		{name: "IPv4 TURN anycast", ip: "192.0.0.10"},
		{name: "IPv4 AS112", ip: "192.31.196.1"},
		{name: "IPv4 AMT", ip: "192.52.193.1"},
		{name: "IPv4 direct delegation AS112", ip: "192.175.48.1"},
		{name: "IPv6 well-known translation", ip: "64:ff9b::808:808"},
		{name: "IPv6 PCP anycast", ip: "2001:1::1"},
		{name: "IPv6 TURN anycast", ip: "2001:1::2"},
		{name: "IPv6 DNS-SD anycast", ip: "2001:1::3"},
		{name: "IPv6 AMT", ip: "2001:3::1"},
		{name: "IPv6 AS112", ip: "2001:4:112::1"},
		{name: "IPv6 ORCHIDv2", ip: "2001:20::1"},
		{name: "IPv6 DETs", ip: "2001:30::1"},
		{name: "IPv6 direct delegation AS112", ip: "2620:4f:8000::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIPAllowedThroughLiteralAndResolver(t, tt.ip)
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

func TestSafeDialContextPinsValidatedIPAddressWithoutSecondDNSLookup(t *testing.T) {
	dialErr := errors.New("stop after recording dial target")
	resolver := &alternatingResolver{answers: [][]net.IPAddr{
		{{IP: net.ParseIP("93.184.216.34")}},
		{{IP: net.ParseIP("127.0.0.1")}},
	}}
	dialer := &recordingDialer{err: dialErr}

	_, err := NewSafeDialContext(resolver, dialer)(context.Background(), "tcp", "media.example.com:443")
	if !errors.Is(err, dialErr) {
		t.Fatalf("dial error = %v, want %v", err, dialErr)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want exactly 1", resolver.calls)
	}
	wantCalls := []dialCall{{network: "tcp", address: "93.184.216.34:443"}}
	if !reflect.DeepEqual(dialer.calls, wantCalls) {
		t.Fatalf("dial calls = %#v, want %#v", dialer.calls, wantCalls)
	}
}

func TestSafeDialContextValidatesEveryAnswerBeforeDialing(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"mixed.example.com": {
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("127.0.0.1")},
		},
	}}
	dialer := &recordingDialer{}

	_, err := NewSafeDialContext(resolver, dialer)(context.Background(), "tcp", "mixed.example.com:80")
	assertValidationCode(t, err, CodeAddressNotPublic)
	if len(dialer.calls) != 0 {
		t.Fatalf("dialer received calls before all answers were validated: %#v", dialer.calls)
	}
}

func TestNewSafeTransportDisablesEnvironmentProxy(t *testing.T) {
	transport := NewSafeTransport(&fakeResolver{}, &recordingDialer{})
	if transport.Proxy != nil {
		t.Fatal("safe transport has a proxy callback; environment proxy could bypass validation")
	}
	if transport.DialContext == nil {
		t.Fatal("safe transport has no safe DialContext")
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

func assertIPRejectedThroughLiteralAndResolver(t *testing.T, rawIP string) {
	t.Helper()
	ip := net.ParseIP(rawIP)
	if ip == nil {
		t.Fatalf("invalid test IP %q", rawIP)
	}

	literalURL := "http://" + net.JoinHostPort(rawIP, "80") + "/video.mp4"
	_, err := ValidateRemoteURL(context.Background(), literalURL, &fakeResolver{})
	assertValidationCode(t, err, CodeAddressNotPublic)

	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"special.example": {{IP: ip}},
	}}
	_, err = ValidateRemoteURL(context.Background(), "https://special.example/video.mp4", resolver)
	assertValidationCode(t, err, CodeAddressNotPublic)
}

func assertIPAllowedThroughLiteralAndResolver(t *testing.T, rawIP string) {
	t.Helper()
	ip := net.ParseIP(rawIP)
	if ip == nil {
		t.Fatalf("invalid test IP %q", rawIP)
	}

	literalURL := "http://" + net.JoinHostPort(rawIP, "80") + "/video.mp4"
	if _, err := ValidateRemoteURL(context.Background(), literalURL, &fakeResolver{}); err != nil {
		t.Fatalf("literal %s rejected: %v", rawIP, err)
	}

	resolver := &fakeResolver{answers: map[string][]net.IPAddr{
		"global-special.example": {{IP: ip}},
	}}
	if _, err := ValidateRemoteURL(context.Background(), "https://global-special.example/video.mp4", resolver); err != nil {
		t.Fatalf("resolved %s rejected: %v", rawIP, err)
	}
}
