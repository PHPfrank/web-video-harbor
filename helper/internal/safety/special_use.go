package safety

import (
	"net"
	"net/netip"
)

// specialPurposeRules is a snapshot of the IANA IPv4 and IPv6 Special-Purpose
// Address Registries, last updated by IANA on 2025-10-09 and reviewed here on
// 2026-07-23:
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
//
// Entries marked N/A or blank for "Globally Reachable" are treated as false.
// Lookup uses longest-prefix matching because both registries contain explicit
// globally reachable allocations within broader non-global parent prefixes.
var specialPurposeRules = []specialPurposeRule{
	// IPv4 registry.
	newSpecialPurposeRule("0.0.0.0/8", false),
	newSpecialPurposeRule("0.0.0.0/32", false),
	newSpecialPurposeRule("10.0.0.0/8", false),
	newSpecialPurposeRule("100.64.0.0/10", false),
	newSpecialPurposeRule("127.0.0.0/8", false),
	newSpecialPurposeRule("169.254.0.0/16", false),
	newSpecialPurposeRule("172.16.0.0/12", false),
	newSpecialPurposeRule("192.0.0.0/24", false),
	newSpecialPurposeRule("192.0.0.0/29", false),
	newSpecialPurposeRule("192.0.0.8/32", false),
	newSpecialPurposeRule("192.0.0.9/32", true),
	newSpecialPurposeRule("192.0.0.10/32", true),
	newSpecialPurposeRule("192.0.0.170/32", false),
	newSpecialPurposeRule("192.0.0.171/32", false),
	newSpecialPurposeRule("192.0.2.0/24", false),
	newSpecialPurposeRule("192.31.196.0/24", true),
	newSpecialPurposeRule("192.52.193.0/24", true),
	newSpecialPurposeRule("192.88.99.0/24", false),
	newSpecialPurposeRule("192.88.99.2/32", false),
	newSpecialPurposeRule("192.168.0.0/16", false),
	newSpecialPurposeRule("192.175.48.0/24", true),
	newSpecialPurposeRule("198.18.0.0/15", false),
	newSpecialPurposeRule("198.51.100.0/24", false),
	newSpecialPurposeRule("203.0.113.0/24", false),
	newSpecialPurposeRule("240.0.0.0/4", false),
	newSpecialPurposeRule("255.255.255.255/32", false),

	// IPv6 registry. IPv4-mapped IPv6 literals are rejected before conversion
	// to net.IP because Go intentionally treats their net.IP form as IPv4.
	newSpecialPurposeRule("::1/128", false),
	newSpecialPurposeRule("::/128", false),
	newSpecialPurposeRule("::ffff:0:0/96", false),
	newSpecialPurposeRule("64:ff9b::/96", true),
	newSpecialPurposeRule("64:ff9b:1::/48", false),
	newSpecialPurposeRule("100::/64", false),
	newSpecialPurposeRule("100:0:0:1::/64", false),
	newSpecialPurposeRule("2001::/23", false),
	newSpecialPurposeRule("2001::/32", false),
	newSpecialPurposeRule("2001:1::1/128", true),
	newSpecialPurposeRule("2001:1::2/128", true),
	newSpecialPurposeRule("2001:1::3/128", true),
	newSpecialPurposeRule("2001:2::/48", false),
	newSpecialPurposeRule("2001:3::/32", true),
	newSpecialPurposeRule("2001:4:112::/48", true),
	newSpecialPurposeRule("2001:10::/28", false),
	newSpecialPurposeRule("2001:20::/28", true),
	newSpecialPurposeRule("2001:30::/28", true),
	newSpecialPurposeRule("2001:db8::/32", false),
	newSpecialPurposeRule("2002::/16", false),
	newSpecialPurposeRule("2620:4f:8000::/48", true),
	newSpecialPurposeRule("3fff::/20", false),
	newSpecialPurposeRule("5f00::/16", false),
	newSpecialPurposeRule("fc00::/7", false),
	newSpecialPurposeRule("fe80::/10", false),
}

// The IANA IPv6 Address Space registry limits assigned global unicast space to
// 2000::/3. Special-purpose rules above take precedence, allowing explicit
// global exceptions such as 64:ff9b::/96 outside this allocation.
// Snapshot: https://www.iana.org/assignments/ipv6-address-space/ (2025-10-23).
var ianaIPv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

type specialPurposeRule struct {
	prefix            netip.Prefix
	globallyReachable bool
}

func newSpecialPurposeRule(prefix string, globallyReachable bool) specialPurposeRule {
	return specialPurposeRule{
		prefix:            netip.MustParsePrefix(prefix),
		globallyReachable: globallyReachable,
	}
}

func specialPurposeReachability(ip net.IP) (globallyReachable, found bool) {
	address, ok := normalizedAddr(ip)
	if !ok {
		return false, false
	}

	longestPrefix := -1
	for _, rule := range specialPurposeRules {
		if rule.prefix.Addr().BitLen() != address.BitLen() || !rule.prefix.Contains(address) {
			continue
		}
		if rule.prefix.Bits() > longestPrefix {
			longestPrefix = rule.prefix.Bits()
			globallyReachable = rule.globallyReachable
			found = true
		}
	}
	return globallyReachable, found
}

func isIANAAllocatedGlobalUnicast(ip net.IP) bool {
	address, ok := normalizedAddr(ip)
	return ok && (!address.Is6() || ianaIPv6GlobalUnicast.Contains(address))
}

func normalizedAddr(ip net.IP) (netip.Addr, bool) {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func isIPv4MappedLiteral(host string) bool {
	address, err := netip.ParseAddr(host)
	return err == nil && address.Is4In6()
}
