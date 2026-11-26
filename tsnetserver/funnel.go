package tsnetserver

import (
	"context"
	"net"
	"net/http"

	"tailscale.com/ipn"
)

type (
	ctxFunnelConn       struct{}
	ctxConnContextWired struct{}
)

// netConnWrapper matches connection wrappers such as *tls.Conn that expose the underlying net.Conn
type netConnWrapper interface {
	NetConn() net.Conn
}

// ConnContextFn returns a function that can be passed to http.Server.ConnContext
func (t *TSNetServer) ConnContextFn() func(ctx context.Context, c net.Conn) context.Context {
	return func(ctx context.Context, nc net.Conn) context.Context {
		ctx = context.WithValue(ctx, ctxConnContextWired{}, true)

		// Unwrap connection layers (such as *tls.Conn or custom wrappers) until we find *ipn.FunnelConn or hit the base connection
		var funnelConn *ipn.FunnelConn
		for nc != nil {
			fc, ok := nc.(*ipn.FunnelConn)
			if ok {
				funnelConn = fc
				break
			}

			wrapper, ok := nc.(netConnWrapper)
			if !ok {
				break
			}
			nc = wrapper.NetConn()
		}

		if funnelConn != nil {
			ctx = context.WithValue(ctx, ctxFunnelConn{}, funnelConn)
		}

		return ctx
	}
}

func hasConnContext(r *http.Request) bool {
	wired, ok := r.Context().Value(ctxConnContextWired{}).(bool)
	return ok && wired
}

// IsFunneledRequest returns true if the HTTP request is coming over Tailscale Funnel, as opposed to a direct connection
func IsFunneledRequest(r *http.Request) bool {
	// The value in the context exists only if the request is from a Funnel
	_, ok := r.Context().Value(ctxFunnelConn{}).(*ipn.FunnelConn)
	return ok
}

// FunnelClientIP returns the IP of the client invoking over Tailscale Funnel
// If the request is not coming through a Tailscale Funnel, the result is empty
func FunnelClientIP(r *http.Request) string {
	// The value in the context exists only if the request is from a Funnel
	fc, ok := r.Context().Value(ctxFunnelConn{}).(*ipn.FunnelConn)
	if !ok || fc == nil {
		return ""
	}

	addr := fc.Src.Addr()
	if !addr.IsValid() {
		return ""
	}

	return addr.String()
}
