// Package tailnet connects the companion to an embedded, independent Tailscale node.
package tailnet

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"tailscale.com/tsnet"
)

type Identity struct {
	IP       netip.Addr
	Hostname string
}

// Up waits for enrollment and returns the identity advertised to paired phones.
// The caller owns the server and must close it, including after a failed login.
func Up(ctx context.Context, server *tsnet.Server) (Identity, error) {
	status, err := server.Up(ctx)
	if err != nil {
		return Identity{}, err
	}
	if status.Self == nil || len(status.TailscaleIPs) == 0 {
		return Identity{}, errors.New("embedded Tailscale did not return a node identity")
	}
	hostname := strings.TrimSuffix(status.Self.DNSName, ".")
	if !strings.HasSuffix(hostname, ".ts.net") {
		return Identity{}, errors.New("enable MagicDNS for the embedded Tailscale node")
	}
	// Prefer IPv4, while allowing an IPv6-only tailnet identity.
	address := status.TailscaleIPs[0]
	for _, candidate := range status.TailscaleIPs {
		if candidate.Is4() {
			address = candidate
			break
		}
	}
	return Identity{IP: address, Hostname: hostname}, nil
}
