package safety

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

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
	if isIPv4MappedLiteral(host) {
		return nil, validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("host %q is an IPv4-mapped IPv6 literal", host))
	}

	addresses, err := resolveOnce(ctx, host, resolver)
	if err != nil {
		return nil, err
	}
	if err := validateAllAddresses(host, addresses); err != nil {
		return nil, err
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
	if globallyReachable, found := specialPurposeReachability(ip); found {
		return globallyReachable
	}
	return isIANAAllocatedGlobalUnicast(ip)
}
