package tsnetserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// TailscaleWhoIs contains the identity of a node in the tailnet
type TailscaleWhoIs struct {
	NodeID              string             `json:"nodeId"`
	Name                string             `json:"name"`
	Hostname            string             `json:"hostname"`
	IP4                 string             `json:"ip4"`
	IP6                 string             `json:"ip6"`
	UserLoginName       string             `json:"userLoginName,omitempty"`
	Tags                []string           `json:"tags,omitempty"`
	CapMap              tailcfg.PeerCapMap `json:"capMap,omitempty"`
	Shared              bool               `json:"shared,omitzero"`
	KeyExpired          bool               `json:"keyExpired,omitzero"`
	KeyExpiry           time.Time          `json:"keyExpiry,omitzero"`
	Jailed              bool               `json:"jailed,omitzero"`
	UnsignedPeerAPIOnly bool               `json:"unsignedPeerApiOnly,omitzero"`
}

// WhoIs performs a "whois" request with the LocalClient and returns information about the node
// WhoIs is only valid for direct tailnet connections
// For Funnel connections the remote address is the relay, not the real client (use FunnelClientIP instead)
// When ListenFunnel is used, http.Server.ConnContext must be set to ConnContextFn
func (t *TSNetServer) WhoIs(r *http.Request) (res TailscaleWhoIs, err error) {
	// Reject Funnel requests: r.RemoteAddr is the relay address, not the real client
	if IsFunneledRequest(r) {
		return res, errors.New("WhoIs is not valid for Funnel connections; use FunnelClientIP to get the real client address")
	}

	// Fail closed when Funnel is active on this server and ConnContextFn was not wired onto http.Server
	if t.funnelEnabled.Load() && !hasConnContext(r) {
		return res, errors.New("WhoIs requires http.Server.ConnContext to be set to ConnContextFn when ListenFunnel is enabled")
	}

	// Get the remote address from the request
	remoteAddr := r.RemoteAddr
	if remoteAddr == "" {
		return res, errors.New("no remote address HTTP in request")
	}

	// Get the LocalClient to query the WhoIs API
	lc, err := t.server.LocalClient()
	if err != nil {
		return res, fmt.Errorf("failed to get Tailscale local client: %w", err)
	}

	// Query the WhoIs API to get secure node identity
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	whois, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return res, fmt.Errorf("failed to query Tailscale whois: %w", err)
	}

	res, err = whoIsFromResponse(whois)
	if err != nil {
		return TailscaleWhoIs{}, err
	}
	if res.UnsignedPeerAPIOnly {
		return TailscaleWhoIs{}, errors.New("whois resolved to an unsigned PeerAPI-only node")
	}

	return res, nil
}

// Converts the response from the LocalAPI whois call
func whoIsFromResponse(whois *apitype.WhoIsResponse) (res TailscaleWhoIs, err error) {
	if whois == nil || whois.Node == nil {
		return res, errors.New("whois response does not contain a node")
	}

	res = TailscaleWhoIs{
		// Node ID: use the "stable" ID, which is a string
		NodeID: string(whois.Node.StableID),

		// Name: remove the trailing dot
		Name: strings.TrimSuffix(whois.Node.Name, "."),

		// For the host name, we use ComputedNameWithHost
		// This is usually the Magic DNS name
		Hostname: whois.Node.ComputedNameWithHost,

		Tags:   whois.Node.Tags,
		CapMap: whois.CapMap,

		// Sharer is set only on nodes shared in from another tailnet
		Shared: !whois.Node.Sharer.IsZero(),

		KeyExpired:          whois.Node.Expired,
		KeyExpiry:           whois.Node.KeyExpiry,
		Jailed:              whois.Node.IsJailed,
		UnsignedPeerAPIOnly: whois.Node.UnsignedPeerAPIOnly,
	}

	if whois.UserProfile != nil {
		res.UserLoginName = whois.UserProfile.LoginName
	}

	// Addresses
	for _, a := range whois.Node.Addresses {
		if !a.IsValid() {
			continue
		}

		addr := a.Addr()
		if addr.Is6() {
			res.IP6 = addr.String()
		} else if addr.Is4() {
			res.IP4 = addr.String()
		}
	}

	return res, nil
}
