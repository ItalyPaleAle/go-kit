package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTransportRefusesInternalIPs(t *testing.T) {
	tests := []struct {
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := newTransport()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()

			conn, err := transport.DialContext(ctx, "tcp", tc.address)
			if conn != nil {
				err = conn.Close()
				require.Error(t, err)
			}

			require.Error(t, err)
			require.ErrorContains(t, err, "refusing to dial private/internal IP")
		})
	}
}
