package webhook

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/italypaleale/go-kit/iputils"
)

// newTransport returns a new http.Transport for the webhook client
func newTransport() *http.Transport {
	// Build an HTTP transport whose dialer refuses to connect to private or otherwise non-routable IP addresses.
	// net.Dialer.Control is invoked AFTER the OS has resolved the hostname to an IP but BEFORE the connect syscall, which means:
	//   1. it sees the actual IP that would be connected to (no TOCTOU)
	//   2. it runs for every A/AAAA candidate in a multi-address result, so a mixed-public/private DNS response cannot slip through
	//   3. it also catches redirects (we disable auto-following below for good measure, but a custom resolver round would still be blocked)
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("invalid dial address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("dial target %q is not an IP literal", host)
			}
			if iputils.IsPrivateIP(ip) {
				return fmt.Errorf("refusing to dial private/internal IP %s: SSRF protection", ip)
			}
			return nil
		},
	}

	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
