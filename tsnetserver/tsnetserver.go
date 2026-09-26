package tsnetserver

import (
	"context"
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

func (t *TSNetServer) Listen(port int) (net.Listener, error) {
	ln, err := t.server.ListenTLS("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("failed to create tsnet listener: %w", err)
	}

	return ln, nil
}

func (t *TSNetServer) ListenFunnel(port int) (net.Listener, error) {
	ln, err := t.server.ListenFunnel("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("failed to create tsnet funnel listener: %w", err)
	}

	return ln, nil
}

// Close closes the tsnet server
func (t *TSNetServer) Close(_ context.Context) error {
	err := t.server.Close()
	if err != nil {
		return fmt.Errorf("failed to close tsnet server: %w", err)
	}
	return nil
}
