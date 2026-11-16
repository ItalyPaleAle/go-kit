package tsnetserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// TSNetServer wraps a tsnet.Server for use in the application
type TSNetServer struct {
	server   *tsnet.Server
	hostname string
	ip4      string
	ip6      string
}

// NewTSNetServerOpts contains options for NewTSNetServer
type NewTSNetServerOpts struct {
	// Hostname for the node
	Hostname string

	// Auth key
	// This is only used on first startup (or if the node key has expired and it needs to be re-authenticated)
	// If this is empty, the auth key can also be read from the TS_AUTH_KEY env var or users can use interactive login
	AuthKey string

	// ClientID for workload identity federation
	// Optional query attributes can be appended, e.g. `?ephemeral=false&preauthorized=true` (without them the generated auth key is ephemeral)
	// See: https://tailscale.com/docs/features/workload-identity-federation#register-new-nodes-using-workload-identity
	// If empty, falls back to the TS_CLIENT_ID env var
	// Note: tsnet performs the token exchange on every startup, even when the node is already enrolled
	ClientID string

	// IDToken from the identity provider, exchanged with the control server for workload identity federation
	// If empty, falls back to the TS_ID_TOKEN env var
	// Mutually exclusive with Audience
	IDToken string

	// Audience to use when requesting an ID token from a well-known identity provider (e.g. AWS, GCP, GitHub Actions) for workload identity federation
	// If empty, falls back to the TS_AUDIENCE env var
	// Mutually exclusive with IDToken
	Audience string

	// If true, makes the node ephemeral
	Ephemeral bool

	// Directory where to store tsnet's state
	StateDir string

	// Optional store for the IPN state
	// Note that even when using a store, tsnet still needs to write data in StateDir
	Store ipn.StateStore

	// Tags that should be applied to this node in the tailnet, for purposes of ACL enforcement
	// These can be referenced from the ACL policy document
	// Tags are generally required when a node is authenticated using OAuth2
	AdvertiseTags []string

	// Enables debug logging
	DebugLogging bool
}

// NewTSNetServer creates a new TSNetServer instance
func NewTSNetServer(ctx context.Context, opts NewTSNetServerOpts) (*TSNetServer, error) {
	tsLogger := slog.With("scope", "tsnet")
	tsrv := &tsnet.Server{
		Hostname:  opts.Hostname,
		ClientID:  opts.ClientID,
		IDToken:   opts.IDToken,
		Audience:  opts.Audience,
		AuthKey:   opts.AuthKey,
		Dir:       opts.StateDir,
		Ephemeral: opts.Ephemeral,
		Store:     opts.Store,
		UserLogf: func(format string, args ...any) {
			tsLogger.Info(fmt.Sprintf(format, args...))
		},
		AdvertiseTags: opts.AdvertiseTags,
	}

	if opts.DebugLogging {
		tsrv.Logf = func(format string, args ...any) {
			tsLogger.Debug(fmt.Sprintf(format, args...))
		}
	}

	// Bring up the Tailscale node, which also gives us its IPs
	state, err := bringUp(ctx, tsrv)
	if err != nil {
		return nil, err
	}

	t := &TSNetServer{
		hostname: strings.TrimSuffix(state.Self.DNSName, "."),
		server:   tsrv,
	}

	for _, addr := range state.TailscaleIPs {
		if !addr.IsValid() {
			continue
		}
		if addr.Is6() {
			t.ip6 = addr.String()
		} else if addr.Is4() {
			t.ip4 = addr.String()
		}
	}

	return t, nil
}

// tsnetNode is the part of tsnet.Server that bringUp uses, so tests can replace it
type tsnetNode interface {
	Start() error
	Up(ctx context.Context) (*ipnstate.Status, error)
	Close() error
}

// bringUp starts the node and waits until it's running
func bringUp(ctx context.Context, node tsnetNode) (*ipnstate.Status, error) {
	// Up would start the node too, but starting it separately tells apart the two failures
	// If starting fails, tsnet has already released what it allocated, and Close must not be called: it panics on a node that didn't finish starting
	err := node.Start()
	if err != nil {
		return nil, fmt.Errorf("failed to start Tailscale node: %w", err)
	}

	state, err := node.Up(ctx)
	if err != nil {
		// Up can fail after the node started, for example when ctx is canceled while waiting for authentication
		_ = node.Close()
		return nil, fmt.Errorf("failed to bring up Tailscale node: %w", err)
	}

	return state, nil
}

func (t *TSNetServer) Hostname() string {
	return t.hostname
}

func (t *TSNetServer) TailscaleIPs() (ip4 string, ip6 string) {
	return t.ip4, t.ip6
}

func (t *TSNetServer) LocalClient() (*local.Client, error) {
	lc, err := t.server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get Tailscale local client: %w", err)
	}

	return lc, nil
}

// Listen returns a TLS listener on the tailnet, which offers HTTP/2 and HTTP/1.1 through ALPN
func (t *TSNetServer) Listen(port int) (net.Listener, error) {
	// tsnet's ListenTLS doesn't accept a TLS configuration, and its default one doesn't offer HTTP/2, so this does the same with a custom configuration
	st, err := t.server.Up(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get Tailscale node status: %w", err)
	}

	if st.CurrentTailnet == nil || !st.CurrentTailnet.MagicDNSEnabled {
		return nil, errors.New("MagicDNS must be enabled in the tailnet; see https://tailscale.com/s/https")
	}

	if len(st.CertDomains) == 0 {
		return nil, errors.New("HTTPS must be enabled in the tailnet; see https://tailscale.com/s/https")
	}

	lc, err := t.server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get Tailscale local client: %w", err)
	}

	ln, err := t.server.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("failed to create tsnet listener: %w", err)
	}

	tlsLn := tls.NewListener(ln, newTLSConfig(lc.GetCertificate))
	return tlsLn, nil
}

// ListenFunnel returns a TLS listener on the tailnet and on Tailscale Funnel, which offers HTTP/2 and HTTP/1.1 through ALPN
func (t *TSNetServer) ListenFunnel(port int) (net.Listener, error) {
	lc, err := t.server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get Tailscale local client: %w", err)
	}

	ln, err := t.server.ListenFunnel("tcp", ":"+strconv.Itoa(port), tsnet.FunnelTLSConfig(newTLSConfig(lc.GetCertificate)))
	if err != nil {
		return nil, fmt.Errorf("failed to create tsnet funnel listener: %w", err)
	}

	return ln, nil
}

// Returns the TLS configuration for the listeners
// http.Server only serves HTTP/2 on connections where ALPN negotiated it
func newTLSConfig(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		GetCertificate: getCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}

// Close closes the tsnet server
func (t *TSNetServer) Close(_ context.Context) error {
	err := t.server.Close()
	if err != nil {
		return fmt.Errorf("failed to close tsnet server: %w", err)
	}
	return nil
}
