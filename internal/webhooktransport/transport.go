// Package webhooktransport builds the HTTP transport shared by the packages that POST to an operator-configured endpoint
package webhooktransport

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/italypaleale/go-kit/iputils"
)

// Options for New
type Options struct {
	// AllowPrivateIPs lifts the block on private and otherwise non-routable destinations
	// Leave it false for an endpoint that is expected to be on the public internet, such as a third-party chat webhook, where a private destination is a misconfiguration or an attack
	// Set it true only for an endpoint whose whole purpose is usually an internal host, such as a log collector, where blocking would make the feature unusable
	// Note: When a proxy is configured in the environment, the block applies to the proxy's address rather than the destination
	AllowPrivateIPs bool

	// Defaults to slog.Default()
	Logger *slog.Logger
}

// New returns an http.Transport for posting to an operator-configured endpoint
//
// Unless Options.AllowPrivateIPs is set, the dialer refuses to connect to private or otherwise non-routable addresses
// net.Dialer.Control runs after the hostname is resolved to an IP and before the connect syscall, which means:
//  1. it sees the IP that is about to be connected to, so there is no TOCTOU window between the check and the connection
//  2. it runs for every A/AAAA candidate in a multi-address result, so a mixed public/private DNS response cannot slip a private address through
func New(opts Options) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	if !opts.AllowPrivateIPs {
		dialer.Control = controlBlockPrivateIPs

		proxyEnv := proxyEnvVar()
		if proxyEnv != "" {
			log := opts.Logger
			if log == nil {
				log = slog.Default()
			}

			// Log the variable name only, as its value may contain credentials
			log.Warn("A proxy is configured in the environment: the block on private addresses applies to the proxy instead of the destination", slog.String("variable", proxyEnv))
		}
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

// proxyEnvVar returns the name of the first environment variable that configures a proxy, or an empty string if there's none
// These are the variables that http.ProxyFromEnvironment reads, in the same order of precedence
func proxyEnvVar() string {
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if os.Getenv(name) != "" {
			return name
		}
	}

	return ""
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
