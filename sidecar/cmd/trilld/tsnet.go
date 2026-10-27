package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"tailscale.com/tsnet"
	"trill/sidecar/setup"
	"trill/sidecar/tailnet"
)

// No second daemon or kernel interface is needed. Setup must enroll the node
// first; a missing state file must never register an unexpected new identity.
func openTSNet(ctx context.Context, directory, hostname, expectedIP, expectedDNSName string) (*tsnet.Server, error) {
	if err := setup.PrivateDir(directory); err != nil {
		return nil, err
	}
	if _, err := setup.ReadPrivate(filepath.Join(directory, "tailscaled.state")); err != nil {
		return nil, errors.New("embedded Tailscale state is missing or unsafe; run trill setup --network tsnet first")
	}
	server := &tsnet.Server{Dir: directory, Hostname: hostname, Logf: func(string, ...any) {}, UserLogf: log.Printf}
	startupContext, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	identity, err := tailnet.Up(startupContext, server)
	if err != nil {
		server.Close()
		return nil, fmt.Errorf("embedded Tailscale failed to connect: %w", err)
	}
	if identity.IP.String() != expectedIP || identity.Hostname != expectedDNSName {
		server.Close()
		return nil, errors.New("embedded Tailscale identity changed; preserve this state and configure a new companion before re-pairing")
	}
	return server, nil
}
