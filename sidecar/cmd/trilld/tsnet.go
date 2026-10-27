package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
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

func parseUDPPortRange(value string) (int, int, error) {
	pieces := strings.Split(value, ":")
	if len(pieces) != 2 {
		return 0, 0, errors.New("UDP ports must be a bounded start:end range")
	}
	first, firstError := strconv.Atoi(pieces[0])
	last, lastError := strconv.Atoi(pieces[1])
	if firstError != nil || lastError != nil || first < 1 || last > 65535 || last < first || last-first > 100 {
		return 0, 0, errors.New("UDP ports must contain at most 101 valid ports")
	}
	return first, last, nil
}

func tsnetUDPForwarders(server *tsnet.Server, advertisedIP, ports string) ([]*tailnet.UDPForwarder, error) {
	first, last, err := parseUDPPortRange(ports)
	if err != nil {
		return nil, err
	}
	var forwarders []*tailnet.UDPForwarder
	for port := first; port <= last; port++ {
		frontend, listenError := server.ListenPacket("udp", net.JoinHostPort(advertisedIP, strconv.Itoa(port)))
		if listenError != nil {
			for _, forwarder := range forwarders {
				forwarder.Close()
			}
			return nil, listenError
		}
		forwarder, forwardError := tailnet.NewUDPForwarder(frontend, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if forwardError != nil {
			frontend.Close()
			for _, existing := range forwarders {
				existing.Close()
			}
			return nil, forwardError
		}
		forwarders = append(forwarders, forwarder)
	}
	return forwarders, nil
}
