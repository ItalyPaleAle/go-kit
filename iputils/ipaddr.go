package iputils

import (
	"net"
)

// Pre-parsed CIDR ranges used by IsPrivateIP
// Parsing once at package init avoids repeating the work on every call
var (
	ipAllowRanges   []*net.IPNet
	ipPrivateRanges []*net.IPNet
)

func init() {
	// Explicit allow-list overrides the block list
	// CGNAT (RFC 6598) 100.64.0.0/10 is widely used as legitimate routable address space by Tailscale and similar overlays
	// fd7a:115c:a1e0::/48 is Tailscale's well-known ULA subset (while this is inside fc00::/7, it is used for legitimate cross-host traffic)
	ipAllowRanges = mustParseCIDRs([]string{
		"100.64.0.0/10",
		"fd7a:115c:a1e0::/48",
	})

	ipPrivateRanges = mustParseCIDRs([]string{
		// IPv4 non-routable and private ranges
		"0.0.0.0/8",          // this host / current network (RFC 1122)
		"10.0.0.0/8",         // private-use (RFC 1918)
		"127.0.0.0/8",        // loopback (RFC 1122)
		"169.254.0.0/16",     // link-local (RFC 3927), covers AWS/GCP/Azure metadata 169.254.169.254
		"172.16.0.0/12",      // private-use (RFC 1918)
		"192.0.0.0/24",       // IETF protocol assignments (RFC 6890)
		"192.0.2.0/24",       // documentation TEST-NET-1 (RFC 5737)
		"192.168.0.0/16",     // private-use (RFC 1918)
		"198.18.0.0/15",      // benchmarking (RFC 2544)
		"198.51.100.0/24",    // documentation TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",     // documentation TEST-NET-3 (RFC 5737)
		"224.0.0.0/4",        // multicast (RFC 5771)
		"240.0.0.0/4",        // reserved, class E (RFC 1112)
		"255.255.255.255/32", // limited broadcast (RFC 919)

		// IPv6 non-routable and private ranges
		"::/128",        // unspecified address
		"::1/128",       // loopback
		"2001:db8::/32", // documentation (RFC 3849)
		"fc00::/7",      // unique local address (RFC 4193) - Tailscale subset carved out above
		"fe80::/10",     // link-local (RFC 4291)
		"ff00::/8",      // multicast (RFC 4291)
	})
}

// mustParseCIDRs parses each CIDR literal and panics on failure
// Only intended for package-init use with trusted string literals
func mustParseCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, network, err := net.ParseCIDR(c)
		if err != nil {
			panic("utils: invalid CIDR literal " + c + ": " + err.Error())
		}
		out[i] = network
	}
	return out
}

// IsPrivateIP returns true if ip is in a private, loopback, link-local, or otherwise non-routable range
// Addresses in the CGNAT range (100.64.0.0/10) and Tailscale ULA subset (fd7a:115c:a1e0::/48) are treated as routable, because they are used as legitimate cross-host address space by overlays like Tailscale
// A nil or zero-length ip yields false, matching the behavior of net.IPNet.Contains
func IsPrivateIP(ip net.IP) bool {
	// Normalize any IPv4 address embedded in an IPv6 address back to its IPv4 form so the v4 block list applies uniformly
	// Covers IPv4-mapped (::ffff:a.b.c.d), NAT64 (64:ff9b::/96), 6to4 (2002::/16), and the deprecated IPv4-compatible (::a.b.c.d) forms
	// Without this an attacker could smuggle a blocked IPv4 (loopback, link-local metadata, RFC1918) past the check inside an IPv6 wrapper
	v4 := to4(ip)
	if v4 != nil {
		ip = v4
	}

	for _, network := range ipAllowRanges {
		if network.Contains(ip) {
			return false
		}
	}

	for _, network := range ipPrivateRanges {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// to4 returns the IPv4 address embedded in ip, or nil if ip does not embed one
// It recognizes plain IPv4, IPv4-mapped IPv6 (::ffff:a.b.c.d), NAT64 (64:ff9b::/96), 6to4 (2002::/16), and the deprecated IPv4-compatible form (::a.b.c.d)
func to4(ip net.IP) net.IP {
	// To4 already handles plain IPv4 and the IPv4-mapped form
	v4 := ip.To4()
	if v4 != nil {
		return v4
	}

	// Only a full 16-byte IPv6 address can embed an IPv4 in the forms below
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}

	switch {
	// NAT64 well-known prefix 64:ff9b::/96 (RFC 6052) carries the IPv4 in the low 32 bits
	case ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b && isZero(ip16[4:12]):
		return net.IP{ip16[12], ip16[13], ip16[14], ip16[15]}

	// 6to4 prefix 2002::/16 (RFC 3056) carries the IPv4 in bytes 2-5
	case ip16[0] == 0x20 && ip16[1] == 0x02:
		return net.IP{ip16[2], ip16[3], ip16[4], ip16[5]}

	// Deprecated IPv4-compatible form ::a.b.c.d (::/96, RFC 4291) carries the IPv4 in the low 32 bits
	// This also matches :: and ::1, whose extracted v4 (0.0.0.0 / 0.0.0.1) still lands in the blocked 0.0.0.0/8 range
	case isZero(ip16[0:12]):
		return net.IP{ip16[12], ip16[13], ip16[14], ip16[15]}
	}

	return nil
}

// isZero reports whether every byte in b is zero
func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
