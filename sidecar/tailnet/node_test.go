package tailnet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/net/netns"
	"tailscale.com/net/stun/stuntest"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/types/nettype"
	"trill/sidecar/internal/testcert"
	"trill/sidecar/setup"
)

// Two real embedded nodes, a disposable control server and local DERP/STUN.
// No production tailnet, account, daemon, TUN interface or personal session.
func TestEmbeddedTailnetHTTPSAndPersistentIdentity(t *testing.T) {
	t.Setenv("TS_NO_LOGS_NO_SUPPORT", "true")
	netns.SetEnabled(false)
	defer netns.SetEnabled(true)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control := &testcontrol.Server{
		Logf: logger.Discard, RequireAuthKey: "disposable-test-key",
		DERPMap:   localDERPAndSTUN(t),
		DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "test.ts.net",
	}
	control.HTTPTestServer = httptest.NewServer(control)
	defer control.HTTPTestServer.Close()
	nodeDirectory := t.TempDir()
	node := &tsnet.Server{Dir: nodeDirectory, Hostname: "companion", ControlURL: control.BaseURL(), AuthKey: "disposable-test-key", Logf: logger.Discard, UserLogf: logger.Discard}
	defer node.Close()
	identity, err := Up(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	peer := &tsnet.Server{Dir: t.TempDir(), Hostname: "phone", ControlURL: control.BaseURL(), AuthKey: "disposable-test-key", Logf: logger.Discard, UserLogf: logger.Discard}
	defer peer.Close()
	if _, err := Up(ctx, peer); err != nil {
		t.Fatal(err)
	}

	t.Run("trusted HTTPS", func(t *testing.T) {
		certificateDirectory := filepath.Join(t.TempDir(), "certificate")
		if err := setup.PrivateDir(certificateDirectory); err != nil {
			t.Fatal(err)
		}
		certificateBundle, certificateRoots := testcert.Bundle(t, identity.Hostname, time.Now(), 90*24*time.Hour)
		if err := setup.WritePrivate(filepath.Join(certificateDirectory, "tls.pem"), certificateBundle, false); err != nil {
			t.Fatal(err)
		}
		certificates := setup.NewCertificates()
		certificates.Roots = certificateRoots
		pin, err := certificates.Provision(ctx, certificateDirectory, setup.Network{Kind: "tsnet", IP: identity.IP.String(), Host: identity.Hostname}, "Test companion")
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := setup.ReadPrivate(filepath.Join(certificateDirectory, "tls.pem"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := setup.ValidateBundle(bundle, identity.Hostname, pin, time.Now(), certificateRoots); err != nil {
			t.Fatal(err)
		}
		certificate, err := tls.X509KeyPair(bundle, bundle)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(bundle)
		listener, err := node.Listen("tcp", ":7443")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "owned companion") }), ReadHeaderTimeout: time.Second}
		defer server.Close()
		go server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}))
		transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return peer.Dial(ctx, network, net.JoinHostPort(identity.IP.String(), "7443"))
		}, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: identity.Hostname, MinVersion: tls.VersionTLS12}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
		response, err := client.Get("https://" + identity.Hostname + ":7443/health")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != "owned companion" {
			t.Fatalf("HTTPS response %q: %v", body, err)
		}
	})

	if err := node.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := &tsnet.Server{Dir: nodeDirectory, Hostname: "companion", ControlURL: control.BaseURL(), AuthKey: "disposable-test-key", Logf: logger.Discard, UserLogf: logger.Discard}
	defer restarted.Close()
	restored, err := Up(ctx, restarted)
	if err != nil || restored != identity {
		t.Fatalf("embedded node identity changed after restart: %+v vs %+v: %v", restored, identity, err)
	}
}

func localDERPAndSTUN(t *testing.T) *tailcfg.DERPMap {
	t.Helper()
	relay := derpserver.New(key.NewNode(), logger.Discard)
	httpServer := httptest.NewUnstartedServer(derpserver.Handler(relay))
	httpServer.Config.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	httpServer.StartTLS()
	stunAddress, closeSTUN := stuntest.ServeWithPacketListener(t, nettype.Std{})
	t.Cleanup(func() { httpServer.CloseClientConnections(); httpServer.Close(); relay.Close(); closeSTUN() })
	return &tailcfg.DERPMap{Regions: map[int]*tailcfg.DERPRegion{1: {RegionID: 1, RegionCode: "test", Nodes: []*tailcfg.DERPNode{{Name: "local", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", STUNPort: stunAddress.Port, STUNTestIP: "127.0.0.1", DERPPort: httpServer.Listener.Addr().(*net.TCPAddr).Port, InsecureForTests: true}}}}}
}
