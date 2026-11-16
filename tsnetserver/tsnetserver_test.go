package tsnetserver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// fakeNode is a tsnetNode that returns the configured errors and counts the calls it receives
type fakeNode struct {
	startErr error
	upErr    error

	upCalls    int
	closeCalls int
}

func (n *fakeNode) Start() error {
	return n.startErr
}

func (n *fakeNode) Up(context.Context) (*ipnstate.Status, error) {
	n.upCalls++
	if n.upErr != nil {
		return nil, n.upErr
	}

	return &ipnstate.Status{}, nil
}

func (n *fakeNode) Close() error {
	n.closeCalls++
	return nil
}

func TestBringUp(t *testing.T) {
	t.Run("closes a node that started but did not come up", func(t *testing.T) {
		upErr := context.Canceled
		node := &fakeNode{upErr: upErr}

		state, err := bringUp(t.Context(), node)
		require.ErrorIs(t, err, upErr)
		assert.Nil(t, state)
		assert.Equal(t, 1, node.closeCalls)
	})

	t.Run("does not close a node that failed to start", func(t *testing.T) {
		startErr := errors.New("start failed")
		node := &fakeNode{startErr: startErr}

		state, err := bringUp(t.Context(), node)
		require.ErrorIs(t, err, startErr)
		assert.Nil(t, state)
		assert.Equal(t, 0, node.upCalls)
		assert.Equal(t, 0, node.closeCalls)
	})

	t.Run("does not close a node that came up", func(t *testing.T) {
		node := &fakeNode{}

		state, err := bringUp(t.Context(), node)
		require.NoError(t, err)
		assert.NotNil(t, state)
		assert.Equal(t, 0, node.closeCalls)
	})

	t.Run("returns the error of a tsnet server that failed to start", func(t *testing.T) {
		// tsnet rejects this configuration before starting anything, so no network is involved
		// Calling Close on this server would panic, as it never finished starting
		srv := &tsnet.Server{
			Dir:   t.TempDir(),
			Store: new(mem.Store),
		}

		state, err := bringUp(t.Context(), srv)
		require.ErrorContains(t, err, "failed to start Tailscale node")
		assert.Nil(t, state)
	})
}

func TestNewTLSConfig(t *testing.T) {
	// Borrow the certificate of a test server, whose client trusts it
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer certSrv.Close()
	cert := certSrv.TLS.Certificates[0]

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsLn := tls.NewListener(ln, newTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &cert, nil
	}))

	// Like apps that serve on these listeners, the server has no TLS configuration of its own
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.Serve(tlsLn)
	}()
	defer srv.Close() //nolint:errcheck

	// Returns the protocol of a request made with the transport
	getProto := func(t *testing.T, transport *http.Transport) string {
		t.Helper()
		defer transport.CloseIdleConnections()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+ln.Addr().String(), nil)
		require.NoError(t, err)
		res, err := (&http.Client{Transport: transport}).Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()

		return res.Proto
	}

	baseTransport, ok := certSrv.Client().Transport.(*http.Transport)
	require.True(t, ok)

	t.Run("HTTP/2", func(t *testing.T) {
		transport := baseTransport.Clone()
		transport.ForceAttemptHTTP2 = true
		assert.Equal(t, "HTTP/2.0", getProto(t, transport))
	})

	t.Run("HTTP/1.1", func(t *testing.T) {
		transport := baseTransport.Clone()
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		assert.Equal(t, "HTTP/1.1", getProto(t, transport))
	})
}
