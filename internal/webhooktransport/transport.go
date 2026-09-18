// Package webhooktransport builds the HTTP transport shared by the packages that POST to an operator-configured endpoint.
package webhooktransport

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/italypaleale/go-kit/iputils"
)

// Options for New
type Options struct {
	// AllowPrivateIPs lifts the block on private and otherwise non-routable destinations.
	// Leave it false for an endpoint that is expected to be on the public internet, such as a third-party chat webhook, where a private destination is a misconfiguration or an attack.
	// Set it true only for an endpoint whose whole purpose is usually an internal host, such as a log collector, where blocking would make the feature unusable.
	// Note: It has no effect on redirects: a caller that follows them can still be walked to a private address, so callers disable redirect following regardless of this option.
	AllowPrivateIPs bool
}

// New returns an http.Transport for posting to an operator-configured endpoint.
//
// Unless Options.AllowPrivateIPs is set, the dialer refuses to connect to private or otherwise non-routable addresses.
// net.Dialer.Control is invoked AFTER the OS has resolved the hostname to an IP but BEFORE the connect syscall, which means:
//  1. it sees the actual IP that would be connected to, so there is no TOCTOU window between the check and the connection
//  2. it runs for every A/AAAA candidate in a multi-address result, so a mixed public/private DNS response cannot slip a private address through
func New(opts Options) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	if !opts.AllowPrivateIPs {
		dialer.Control = controlBlockPrivateIPs
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

// controlBlockPrivateIPs is the net.Dialer.Control hook that refuses a connection to a non-routable address
func controlBlockPrivateIPs(network string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q: %w", address, err)
	}

	// Control runs after resolution, so the host is always an IP literal here
	// Anything else means an assumption of this hook no longer holds, so refuse rather than guess
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("dial target %q is not an IP literal", host)
	}

	if iputils.IsPrivateIP(ip) {
		return fmt.Errorf("refusing to dial private/internal IP %s: SSRF protection", ip)
	}

	return nil
}
