package iputils

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		// --- Public IPv4 addresses: must NOT be blocked ---
		{"public Google DNS v4", "8.8.8.8", false},
		{"public Cloudflare DNS v4", "1.1.1.1", false},
		{"public Quad9 v4", "9.9.9.9", false},
		{"public just below 10/8", "9.255.255.255", false},
		{"public just above 10/8", "11.0.0.0", false},
		{"public just below 172.16/12", "172.15.255.255", false},
		{"public just above 172.16/12", "172.32.0.0", false},
		{"public just below 192.168/16", "192.167.255.255", false},
		{"public just above 192.168/16", "192.169.0.0", false},
		{"public just above 127/8", "128.0.0.1", false},
		{"public above TEST-NET-3", "203.0.114.0", false},
		{"public below TEST-NET-2", "198.51.99.255", false},
		{"public above benchmarking", "198.20.0.0", false},
		{"public address near IETF protocol block", "192.1.0.0", false},

		// --- RFC 1918 private IPv4 ---
		{"private 10/8 low", "10.0.0.0", true},
		{"private 10/8 mid", "10.128.64.1", true},
		{"private 10/8 high", "10.255.255.255", true},
		{"private 172.16 low", "172.16.0.0", true},
		{"private 172.16 mid", "172.20.1.2", true},
		{"private 172.16 high", "172.31.255.255", true},
		{"private 192.168 low", "192.168.0.0", true},
		{"private 192.168 mid", "192.168.1.1", true},
		{"private 192.168 high", "192.168.255.255", true},

		// --- Loopback IPv4 ---
		{"loopback 127.0.0.1", "127.0.0.1", true},
		{"loopback 127 high", "127.255.255.254", true},

		// --- Link-local IPv4 (cloud metadata sits here) ---
		{"link-local 169.254.0.0", "169.254.0.0", true},
		{"AWS/GCP/Azure metadata 169.254.169.254", "169.254.169.254", true},
		{"link-local high", "169.254.255.255", true},

		// --- This-network / unspecified IPv4 ---
		{"this-network 0.0.0.0", "0.0.0.0", true},
		{"this-network 0.1.2.3", "0.1.2.3", true},
		{"this-network high", "0.255.255.255", true},

		// --- Broadcast and multicast IPv4 ---
		{"limited broadcast", "255.255.255.255", true},
		{"multicast low", "224.0.0.0", true},
		{"multicast mid", "230.1.2.3", true},
		{"multicast high", "239.255.255.255", true},

		// --- Reserved / class E IPv4 ---
		{"reserved class E low", "240.0.0.0", true},
		{"reserved class E high", "254.255.255.255", true},

		// --- IETF protocol assignments / documentation / benchmarking IPv4 ---
		{"IETF protocol 192.0.0.1", "192.0.0.1", true},
		{"TEST-NET-1 192.0.2.1", "192.0.2.1", true},
		{"TEST-NET-2 198.51.100.1", "198.51.100.1", true},
		{"TEST-NET-3 203.0.113.1", "203.0.113.1", true},
		{"benchmarking 198.18.0.0", "198.18.0.0", true},
		{"benchmarking 198.19.255.255", "198.19.255.255", true},

		// --- CGNAT / shared address space (RFC 6598): blocked by default ---
		{"CGNAT low 100.64.0.0", "100.64.0.0", true},
		{"CGNAT typical Tailscale 100.64.1.5", "100.64.1.5", true},
		{"CGNAT mid 100.100.100.100", "100.100.100.100", true},
		{"Alibaba Cloud IMDS 100.100.100.200", "100.100.100.200", true},
		{"CGNAT high 100.127.255.255", "100.127.255.255", true},
		{"public just below CGNAT", "100.63.255.255", false},
		{"public just above CGNAT", "100.128.0.0", false},

		// --- IPv4-mapped IPv6: must follow the v4 classification ---
		{"v4-mapped public 8.8.8.8", "::ffff:8.8.8.8", false},
		{"v4-mapped private 10.0.0.1", "::ffff:10.0.0.1", true},
		{"v4-mapped loopback 127.0.0.1", "::ffff:127.0.0.1", true},
		{"v4-mapped metadata 169.254.169.254", "::ffff:169.254.169.254", true},
		{"v4-mapped CGNAT 100.64.0.1", "::ffff:100.64.0.1", true},

		// --- IPv6 loopback / unspecified ---
		{"IPv6 unspecified", "::", true},
		{"IPv6 loopback", "::1", true},

		// --- IPv6 link-local ---
		{"IPv6 link-local low", "fe80::", true},
		{"IPv6 link-local typical", "fe80::1", true},
		{"IPv6 link-local high", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},

		// --- IPv6 ULA (fc00::/7), including Tailscale ULA ---
		{"IPv6 ULA fc00::1", "fc00::1", true},
		{"IPv6 ULA fd00::1", "fd00::1", true},
		{"IPv6 ULA fdff high", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"Tailscale ULA low", "fd7a:115c:a1e0::", true},
		{"Tailscale ULA mid", "fd7a:115c:a1e0:ab12::1", true},
		{"Tailscale ULA high", "fd7a:115c:a1e0:ffff:ffff:ffff:ffff:ffff", true},
		{"Tailscale-adjacent blocked (below)", "fd7a:115c:a1df:ffff::1", true},
		{"Tailscale-adjacent blocked (above)", "fd7a:115c:a1e1::1", true},

		// --- IPv6 multicast ---
		{"IPv6 multicast all-nodes", "ff02::1", true},
		{"IPv6 multicast low", "ff00::", true},
		{"IPv6 multicast high", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},

		// --- IPv6 documentation and special-purpose non-routable blocks ---
		{"IPv6 documentation low", "2001:db8::", true},
		{"IPv6 documentation typical", "2001:db8::1", true},
		{"IPv6 documentation high", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"IPv6 RFC 9637 documentation", "3fff::1", true},
		{"IPv6 local-use NAT64 RFC 8215", "64:ff9b:1::808:808", true},
		{"IPv6 discard-only RFC 6666", "100::1", true},
		{"IPv6 Teredo 2001::/32", "2001::1", true},
		{"IPv6 benchmarking 2001:2::/48", "2001:2::1", true},
		{"IPv6 ORCHID 2001:10::/28", "2001:10::1", true},
		{"IPv6 SRv6 SIDs 5f00::/16", "5f00::1", true},

		// --- Public IPv6 addresses: must NOT be blocked ---
		{"public IPv6 Cloudflare", "2606:4700:4700::1111", false},
		{"public IPv6 Google", "2001:4860:4860::8888", false},
		{"public IPv6 just above 2001::/23", "2001:200::1", false},
		{"public IPv6 just above documentation", "2001:db9::1", false},
		{"public IPv6 just above 3fff::/20", "4000::1", false},
		{"public IPv6 just below fc00", "fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"public IPv6 just above fe80 range", "fec0::1", false},

		// --- IPv4-embedding IPv6 forms must follow the embedded v4 classification (SSRF hardening) ---
		// SIIT IPv4-translated ::ffff:0:0/96
		{"SIIT metadata 169.254.169.254", "::ffff:0:169.254.169.254", true},
		{"SIIT loopback 127.0.0.1", "::ffff:0:127.0.0.1", true},
		{"SIIT private 10.0.0.1", "::ffff:0:10.0.0.1", true},
		{"SIIT public 8.8.8.8 stays routable", "::ffff:0:8.8.8.8", false},
		// NAT64 well-known prefix 64:ff9b::/96
		{"NAT64 metadata 169.254.169.254", "64:ff9b::a9fe:a9fe", true},
		{"NAT64 loopback 127.0.0.1", "64:ff9b::7f00:1", true},
		{"NAT64 private 10.0.0.1", "64:ff9b::a00:1", true},
		{"NAT64 public 8.8.8.8 stays routable", "64:ff9b::808:808", false},
		// 6to4 prefix 2002::/16
		{"6to4 metadata 169.254.169.254", "2002:a9fe:a9fe::", true},
		{"6to4 private 10.0.0.1", "2002:a00:1::", true},
		{"6to4 public 8.8.8.8 stays routable", "2002:808:808::", false},
		// Deprecated IPv4-compatible form ::a.b.c.d
		{"v4-compatible metadata 169.254.169.254", "::169.254.169.254", true},
		{"v4-compatible private 10.0.0.1", "::10.0.0.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("test setup error: ParseIP(%q) returned nil", tt.ip)
			}
			got := IsPrivateIP(ip)
			if got != tt.want {
				t.Errorf("IsPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsTailscaleIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"CGNAT base", "100.64.0.0", true},
		{"CGNAT typical Tailscale", "100.64.1.5", true},
		{"CGNAT broadcast", "100.127.255.255", true},
		{"v4-mapped Tailscale", "::ffff:100.64.0.1", true},
		{"Tailscale ULA base", "fd7a:115c:a1e0::", true},
		{"Tailscale ULA mid", "fd7a:115c:a1e0:1234:5678:90ab:cdef:0123", true},
		{"RFC1918 private", "10.0.0.1", false},
		{"Other IPv6 ULA", "fd00::1", false},
		{"Public IPv4", "8.8.8.8", false},
		{"NAT64 wrapping CGNAT is not native Tailscale", "64:ff9b::6440:1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("test setup error: ParseIP(%q) returned nil", tc.ip)
			}
			got := IsTailscaleIP(ip)
			if got != tc.want {
				t.Errorf("IsTailscaleIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
	if IsTailscaleIP(nil) {
		t.Error("IsTailscaleIP(nil) = true, want false")
	}
}

// A nil or zero-length net.IP should return false
// This matches the behavior of net.IPNet.Contains, which returns false for a length mismatch
// Callers that need fail-closed behavior must validate the input before calling
func TestIsPrivateIP_NilAndEmpty(t *testing.T) {
	if IsPrivateIP(nil) {
		t.Error("IsPrivateIP(nil) = true, want false")
	}
	if IsPrivateIP(net.IP{}) {
		t.Error("IsPrivateIP(empty) = true, want false")
	}
}

// Feeding the same IP twice must yield the same answer, confirming that the pre-parsed CIDR slices are not mutated by Contains
func TestIsPrivateIP_Stable(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	for i := range 5 {
		got := IsPrivateIP(ip)
		if !got {
			t.Fatalf("iteration %d: IsPrivateIP(10.0.0.1) = false, want true", i)
		}
	}
}

func BenchmarkIsPrivateIP(b *testing.B) {
	ips := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("169.254.169.254"),
		net.ParseIP("100.64.0.1"),
		net.ParseIP("2606:4700:4700::1111"),
		net.ParseIP("fd7a:115c:a1e0::1"),
		net.ParseIP("fe80::1"),
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = IsPrivateIP(ips[i%len(ips)])
		i++
	}
}
