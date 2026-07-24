package safety

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

const CodeInvalidDialTarget = "invalid_dial_target"

// ContextDialer is the narrow dialing interface used by the safe transport.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// NewSafeDialContext resolves a hostname exactly once per call, validates the
// complete answer set, and passes only validated literal IP addresses to the
// underlying dialer. net/http retains the original URL host for Host and TLS
// server-name handling.
func NewSafeDialContext(resolver Resolver, dialer ContextDialer) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dialer == nil {
		dialer = &net.Dialer{}
	}

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, validationError(CodeInvalidDialTarget, "下载连接类型无效", fmt.Sprintf("network %q is not supported", network))
		}

		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, validationError(CodeInvalidDialTarget, "下载地址端口无效", err.Error())
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, validationError(CodeInvalidDialTarget, "下载地址端口无效", fmt.Sprintf("port %q is not numeric or outside 1-65535", port))
		}

		addresses, err := resolveOnce(ctx, host, resolver)
		if err != nil {
			return nil, err
		}
		if !integrationAllowsExactHostPort(resolver, address) {
			if err := validateAllAddresses(host, addresses); err != nil {
				return nil, err
			}
		}

		var lastErr error
		attempted := false
		for _, resolved := range addresses {
			if (network == "tcp4" && resolved.IP.To4() == nil) || (network == "tcp6" && resolved.IP.To4() != nil) {
				continue
			}
			attempted = true
			target := net.JoinHostPort(resolved.IP.String(), port)
			conn, dialErr := dialer.DialContext(ctx, network, target)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if !attempted {
			return nil, validationError(CodeResolveFailed, "无法连接下载地址", fmt.Sprintf("host %q has no address compatible with %s", host, network))
		}
		return nil, fmt.Errorf("dial validated download address: %w", lastErr)
	}
}

// NewSafeTransport returns a transport that cannot route around target
// validation through HTTP_PROXY/HTTPS_PROXY environment variables.
func NewSafeTransport(resolver Resolver, dialer ContextDialer) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = NewSafeDialContext(resolver, dialer)
	transport.DialTLSContext = nil
	return transport
}

func resolveOnce(ctx context.Context, host string, resolver Resolver) ([]net.IPAddr, error) {
	if isIPv4MappedLiteral(host) {
		return nil, validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("host %q is an IPv4-mapped IPv6 literal", host))
	}
	if literal := net.ParseIP(host); literal != nil {
		return []net.IPAddr{{IP: literal}}, nil
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
	return addresses, nil
}

func validateAllAddresses(host string, addresses []net.IPAddr) error {
	for _, address := range addresses {
		if address.Zone != "" || !isPublicIP(address.IP) {
			return validationError(CodeAddressNotPublic, "下载地址不能指向本机或局域网", fmt.Sprintf("host %q resolved to non-public address %s", host, address.String()))
		}
	}
	return nil
}

var _ ContextDialer = (*net.Dialer)(nil)
