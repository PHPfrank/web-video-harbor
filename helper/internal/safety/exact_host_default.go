//go:build !integration

package safety

// integrationAllowsExactHostPort is compiled into production and ordinary
// tests. It intentionally has no capability interface and can never bypass
// public-address validation.
func integrationAllowsExactHostPort(Resolver, string) bool { return false }
