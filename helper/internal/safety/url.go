package safety

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var nonPublicNetworks = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),   // carrier-grade NAT
	mustCIDR("192.0.0.0/24"),    // IETF protocol assignments
	mustCIDR("192.0.2.0/24"),    // documentation
	mustCIDR("192.88.99.0/24"),  // deprecated 6to4 relay anycast
	mustCIDR("198.18.0.0/15"),   // benchmarking
	mustCIDR("198.51.100.0/24"), // documentation
	mustCIDR("203.0.113.0/24"),  // documentation
	mustCIDR("240.0.0.0/4"),     // reserved
	mustCIDR("2001:10::/28"),    // deprecated ORCHID
	mustCIDR("2001:db8::/32"),   // documentation
	mustCIDR("fec0::/10"),       // deprecated site-local
}

const (
	CodeInvalidURL            = "invalid_url"
	CodeSchemeNotAllowed      = "scheme_not_allowed"
	CodeCredentialsNotAllowed = "credentials_not_allowed"
	CodeHostRequired          = "host_required"
	CodeResolveFailed         = "resolve_failed"
	CodeAddressNotPublic      = "address_not_public"
	CodeTooManyRedirects      = "too_many_redirects"
)

// Resolver is the narrow DNS interface needed for download target validation.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// ValidationError exposes a stable code and Chinese user message while keeping
// diagnostic detail available separately for logs.
type ValidationError struct {
	Code    string
	Message string
	Detail  string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// ValidateRemoteURL accepts only HTTP(S) URLs whose host resolves exclusively
// to public IP addresses.
func ValidateRemoteURL(ctx context.Context, rawURL string, resolver Resolver) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, validationError(CodeInvalidURL, "下载地址格式无效", err.Error())
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, validationError(CodeSchemeNotAllowed, "仅支持 HTTP 或 HTTPS 下载地址", fmt.Sprintf("scheme %q is not allowed", parsed.Scheme))
	}
	if parsed.User != nil {
		return nil, validationError(CodeCredentialsNotAllowed, "下载地址不能包含用户名或密码", "URL contains user information")
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, validationError(CodeHostRequired, "下载地址缺少主机名", "URL hostname is empty")
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return nil, validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("host %q is local", host))
	}

	if literal := net.ParseIP(host); literal != nil {
		if !isPublicIP(literal) {
			return nil, validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("address %s is not public", literal))
		}
		return parsed, nil
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, validationError(CodeResolveFailed, "无法确认下载地址是否安全", fmt.Sprintf("resolve %q: %v", host, err))
	}
	if len(addresses) == 0 {
		return nil, validationError(CodeResolveFailed, "无法确认下载地址是否安全", fmt.Sprintf("resolve %q: no addresses", host))
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return nil, validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("host %q resolved to non-public address %s", host, address.IP))
		}
	}

	return parsed, nil
}

// SafeRedirectPolicy returns an http.Client CheckRedirect callback that applies
// the same target validation to every redirect hop.
func SafeRedirectPolicy(resolver Resolver) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return validationError(CodeTooManyRedirects, "下载地址重定向次数过多", fmt.Sprintf("redirect count reached %d", len(via)))
		}
		_, err := ValidateRemoteURL(req.Context(), req.URL.String(), resolver)
		return err
	}
}

func validationError(code, message, detail string) *ValidationError {
	return &ValidationError{Code: code, Message: message, Detail: detail}
}

func isPublicIP(ip net.IP) bool {
	if ip == nil ||
		!ip.IsGlobalUnicast() ||
		ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, network := range nonPublicNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}
