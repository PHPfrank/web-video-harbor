//go:build integration

package safety

// integrationExactHostPortAllowlist exists only in integration-tag builds.
// The shipped helper and ordinary tests cannot discover this capability.
type integrationExactHostPortAllowlist interface {
	AllowExactHostPort(hostPort string) bool
}

func integrationAllowsExactHostPort(resolver Resolver, hostPort string) bool {
	allowlist, ok := resolver.(integrationExactHostPortAllowlist)
	return ok && hostPort != "" && allowlist.AllowExactHostPort(hostPort)
}
