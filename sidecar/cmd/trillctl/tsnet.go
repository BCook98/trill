package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"tailscale.com/tsnet"
	"trill/sidecar/setup"
	"trill/sidecar/tailnet"
)

func enrollTSNet(ctx context.Context, stateDirectory, hostname string, output io.Writer) (setup.Network, error) {
	nodeDirectory := filepath.Join(stateDirectory, "tsnet")
	if err := setup.PrivateDir(nodeDirectory); err != nil {
		return setup.Network{}, err
	}
	unlock, err := setup.LockTSNetState(nodeDirectory)
	if err != nil {
		return setup.Network{}, err
	}
	defer unlock()
	server := &tsnet.Server{Dir: nodeDirectory, Hostname: hostname,
		Logf: func(string, ...any) {}, UserLogf: func(format string, args ...any) { fmt.Fprintf(output, format+"\n", args...) }}
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	identity, err := tailnet.Up(ctx, server)
	if err != nil {
		return setup.Network{}, fmt.Errorf("embedded Tailscale enrollment failed; retry setup with the same state directory: %w", err)
	}
	return setup.Network{Kind: "tsnet", IP: identity.IP.String(), Host: identity.Hostname, BindIP: "127.0.0.1"}, nil
}
