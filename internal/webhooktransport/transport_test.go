package webhooktransport

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var internalAddresses = []struct {
	name    string
	address string
}{
	{name: "loopback ipv4", address: "127.0.0.1:443"},
	{name: "private ipv4", address: "10.0.0.25:443"},
	{name: "link local ipv4", address: "169.254.169.254:80"},
	{name: "documentation ipv4", address: "198.51.100.25:443"},
	{name: "loopback ipv6", address: "[::1]:443"},
	{name: "link local ipv6", address: "[fe80::1]:443"},
}

func TestRefusesInternalIPsByDefault(t *testing.T) {
	for _, tc := range internalAddresses {
		t.Run(tc.name, func(t *testing.T) {
			transport := New(Options{})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()

			conn, err := transport.DialContext(ctx, "tcp", tc.address)
			if conn != nil {
				_ = conn.Close()
			}

			require.Error(t, err)
			require.ErrorContains(t, err, "refusing to dial private/internal IP")
		})
	}
}

func TestProxyWarning(t *testing.T) {
	setProxyEnv := func(t *testing.T, name string, value string) {
		for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			t.Setenv(n, "")
		}
		if name != "" {
			t.Setenv(name, value)
		}
	}

	newWithLogs := func(opts Options) (*http.Transport, string) {
		var buf bytes.Buffer
		opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		transport := New(opts)
		return transport, buf.String()
	}

	t.Run("warns when a proxy is set and private IPs are blocked", func(t *testing.T) {
		setProxyEnv(t, "HTTPS_PROXY", "http://user:secret@proxy.example.com:3128")

		transport, logs := newWithLogs(Options{})
		require.NotNil(t, transport.Proxy)
		require.Contains(t, logs, "level=WARN")
		require.Contains(t, logs, "variable=HTTPS_PROXY")

		// The value may contain credentials, so it must not be logged
		require.NotContains(t, logs, "secret")
	})

	t.Run("no warning when private IPs are allowed", func(t *testing.T) {
		setProxyEnv(t, "http_proxy", "http://proxy.example.com:3128")

		transport, logs := newWithLogs(Options{AllowPrivateIPs: true})
		require.NotNil(t, transport.Proxy)
		require.Empty(t, logs)
	})

	t.Run("no warning without a proxy", func(t *testing.T) {
		setProxyEnv(t, "", "")

		_, logs := newWithLogs(Options{})
		require.Empty(t, logs)
	})
}

func TestAllowsInternalIPsWhenEnabled(t *testing.T) {
	// Listen on loopback so the test proves private connections are allowed without depending on external network state
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	transport := New(Options{AllowPrivateIPs: true})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	conn, err := transport.DialContext(ctx, "tcp", listener.Addr().String())
	require.NoError(t, err)
	require.NotNil(t, conn)
	err = conn.Close()
	if err != nil {
		t.Logf("failed to close test connection: %v", err)
	}
}

func TestControlRejectsANonLiteralHost(t *testing.T) {
	// Control runs after resolution, so a name reaching it means an assumption no longer holds
	err := controlBlockPrivateIPs("tcp", "collector.example.com:443", nil)

	require.Error(t, err)
	require.ErrorContains(t, err, "is not an IP literal")
}

func TestControlRejectsAMalformedAddress(t *testing.T) {
	err := controlBlockPrivateIPs("tcp", "no-port-here", nil)

	require.Error(t, err)
	require.ErrorContains(t, err, "invalid dial address")
}

func TestAllowsPublicIPs(t *testing.T) {
	err := controlBlockPrivateIPs("tcp", "93.184.216.34:443", nil)

	require.NoError(t, err)
}
