// Package outbound is the only way LogSage makes HTTP requests to URLs that
// users configure (alert webhooks, ADR 0024). It defends against SSRF: only
// https to public addresses, every DNS answer checked, the checked address
// dialed directly (no second lookup), no redirects, proxies, cookies or
// credentials, tight timeouts and a response size limit.
package outbound

import (
	"net/netip"
)

// blocked4 are the IPv4 ranges a webhook may never reach: everything that
// is not plain public unicast.
var blocked4 = prefixes(
	"0.0.0.0/8",       // "this network", unspecified
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // CGNAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, cloud metadata (169.254.169.254)
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay anycast
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, includes broadcast
)

// blocked6 are the IPv6 ranges a webhook may never reach. Ranges that embed
// an IPv4 address (mapped, NAT64 well-known prefix, 6to4) are decoded in
// Blocked and their IPv4 address is checked instead.
var blocked6 = prefixes(
	"::/96",          // unspecified, loopback, IPv4-compatible (deprecated)
	"64:ff9b:1::/48", // NAT64 local use
	"100::/64",       // discard-only
	"2001::/32",      // Teredo (the client address is obfuscated)
	"2001:2::/48",    // benchmarking
	"2001:10::/28",   // ORCHID
	"2001:20::/28",   // ORCHIDv2
	"2001:db8::/32",  // documentation
	"3fff::/20",      // documentation (RFC 9637)
	"5f00::/16",      // SRv6 SIDs
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local
	"fec0::/10",      // site-local (deprecated)
	"ff00::/8",       // multicast
)

var (
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	six4  = netip.MustParsePrefix("2002::/16")
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Blocked reports whether an address must not be dialed, and why. Only
// global unicast addresses outside every special-purpose range pass; an
// address with a zone (fe80::1%eth0) never does.
func Blocked(a netip.Addr) (bool, string) {
	if !a.IsValid() {
		return true, "invalid address"
	}
	if a.Zone() != "" {
		return true, "address with a zone"
	}
	if a.Is4In6() {
		return Blocked(a.Unmap())
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64.Contains(a):
			return blockedEmbedded(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), "NAT64")
		case six4.Contains(a):
			return blockedEmbedded(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), "6to4")
		}
		for _, p := range blocked6 {
			if p.Contains(a) {
				return true, "special-purpose IPv6 range " + p.String()
			}
		}
		if !a.IsGlobalUnicast() {
			return true, "not a global unicast address"
		}
		return false, ""
	}
	for _, p := range blocked4 {
		if p.Contains(a) {
			return true, "special-purpose IPv4 range " + p.String()
		}
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return true, "not a public unicast address"
	}
	return false, ""
}

func blockedEmbedded(v4 netip.Addr, kind string) (bool, string) {
	if bad, why := Blocked(v4); bad {
		return true, kind + " address embedding " + v4.String() + ": " + why
	}
	return false, ""
}
