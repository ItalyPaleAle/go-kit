package tsnetserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/peercap"
)

func testWhoIsResponse() *apitype.WhoIsResponse {
	return &apitype.WhoIsResponse{
		Node: &tailcfg.Node{
			StableID:             "nABCDEF123",
			Name:                 "alice.example-tailnet.ts.net.",
			ComputedNameWithHost: "alice",
			Tags:                 []string{"tag:ci"},
			Addresses: []netip.Prefix{
				netip.MustParsePrefix("100.64.0.1/32"),
				netip.MustParsePrefix("fd7a:115c:a1e0::1/128"),
			},
		},
		UserProfile: &tailcfg.UserProfile{
			LoginName: "alice@example.com",
		},
		CapMap: tailcfg.PeerCapMap{
			"example.com/cap": []tailcfg.RawMessage{`{"foo":"bar"}`},
		},
	}
}

func TestWhoIsFromResponse(t *testing.T) {
	t.Run("maps the response", func(t *testing.T) {
		res, err := whoIsFromResponse(testWhoIsResponse())
		require.NoError(t, err)
		assert.Equal(t, "nABCDEF123", res.NodeID)
		assert.Equal(t, "alice.example-tailnet.ts.net", res.Name)
		assert.Equal(t, "alice", res.Hostname)
		assert.Equal(t, "100.64.0.1", res.IP4)
		assert.Equal(t, "fd7a:115c:a1e0::1", res.IP6)
		assert.Equal(t, "alice@example.com", res.UserLoginName)
		assert.Equal(t, []string{"tag:ci"}, res.Tags)
		assert.Contains(t, res.CapMap, peercap.Cap("example.com/cap"))
		assert.False(t, res.Shared)
		assert.False(t, res.KeyExpired)
		assert.True(t, res.KeyExpiry.IsZero())
		assert.False(t, res.Jailed)
		assert.False(t, res.UnsignedPeerAPIOnly)
	})

	t.Run("node state", func(t *testing.T) {
		keyExpiry := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		resp := testWhoIsResponse()
		resp.Node.Expired = true
		resp.Node.KeyExpiry = keyExpiry
		resp.Node.IsJailed = true
		resp.Node.UnsignedPeerAPIOnly = true

		res, err := whoIsFromResponse(resp)
		require.NoError(t, err)
		assert.True(t, res.KeyExpired)
		assert.Equal(t, keyExpiry, res.KeyExpiry)
		assert.True(t, res.Jailed)
		assert.True(t, res.UnsignedPeerAPIOnly)
	})

	t.Run("shared node", func(t *testing.T) {
		resp := testWhoIsResponse()
		resp.Node.Sharer = 42

		res, err := whoIsFromResponse(resp)
		require.NoError(t, err)
		assert.True(t, res.Shared)
	})

	t.Run("missing user profile", func(t *testing.T) {
		resp := testWhoIsResponse()
		resp.UserProfile = nil

		res, err := whoIsFromResponse(resp)
		require.NoError(t, err)
		assert.Empty(t, res.UserLoginName)
	})

	t.Run("missing node", func(t *testing.T) {
		_, err := whoIsFromResponse(&apitype.WhoIsResponse{})
		require.ErrorContains(t, err, "does not contain a node")

		_, err = whoIsFromResponse(nil)
		require.ErrorContains(t, err, "does not contain a node")
	})
}

type testWrappedConn struct {
	net.Conn
}

func (w testWrappedConn) NetConn() net.Conn {
	return w.Conn
}

func TestFunnelDetectionAndWhoIsGuard(t *testing.T) {
	t.Run("unwraps nested net.Conn layers to find FunnelConn", func(t *testing.T) {
		var ts TSNetServer
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close() //nolint:errcheck
		defer serverConn.Close() //nolint:errcheck

		fc := &ipn.FunnelConn{
			Conn: serverConn,
			Src:  netip.MustParseAddrPort("203.0.113.9:54321"),
		}
		wrapped := testWrappedConn{testWrappedConn{fc}}

		ctx := ts.ConnContextFn()(context.Background(), wrapped)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "https://example.ts.net/", nil)

		assert.True(t, IsFunneledRequest(req))
		assert.Equal(t, "203.0.113.9", FunnelClientIP(req))

		_, err := ts.WhoIs(req)
		require.ErrorContains(t, err, "WhoIs is not valid for Funnel connections")
	})

	t.Run("fails closed when Funnel is enabled without ConnContextFn", func(t *testing.T) {
		var ts TSNetServer
		ts.funnelEnabled.Store(true)

		req := httptest.NewRequest(http.MethodGet, "https://example.ts.net/", nil)
		_, err := ts.WhoIs(req)
		require.ErrorContains(t, err, "ConnContextFn")
	})
}
