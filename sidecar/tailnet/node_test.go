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
	"trill/sidecar/setup"
)

// Two real embedded nodes, a disposable control server and local DERP/STUN.
// No production tailnet, account, daemon, TUN interface or personal session.
func TestEmbeddedTailnetHTTPSUDPAndPersistentIdentity(t *testing.T) {
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

	t.Run("pinned HTTPS", func(t *testing.T) {
		certificateDirectory := filepath.Join(t.TempDir(), "certificate")
		if err := setup.PrivateDir(certificateDirectory); err != nil {
			t.Fatal(err)
		}
		certificates := setup.NewCertificates()
		pin, err := certificates.Provision(ctx, certificateDirectory, setup.Network{Kind: "tsnet", IP: identity.IP.String(), Host: identity.Hostname}, "Test companion")
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := setup.ReadPrivate(filepath.Join(certificateDirectory, "tls.pem"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := setup.ValidateBundle(bundle, identity.Hostname, pin, time.Now(), nil); err != nil {
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

	for _, address := range func() []string { v4, v6 := node.TailscaleIPs(); return []string{v4.String(), v6.String()} }() {
		t.Run("UDP "+address, func(t *testing.T) {
			backend, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			sources := make(chan string, 2)
			go func() {
				packet := make([]byte, 65535)
				for {
					count, remote, err := backend.ReadFromUDP(packet)
					if err != nil {
						return
					}
					sources <- remote.String()
					_, _ = backend.WriteToUDP(packet[:count], remote)
				}
			}()
			frontend, err := node.ListenPacket("udp", net.JoinHostPort(address, "60000"))
			if err != nil {
				t.Fatal(err)
			}
			forwarder, err := NewUDPForwarder(frontend, backend.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			stopped := make(chan error, 1)
			go func() { stopped <- forwarder.Serve() }()
			defer forwarder.Close()
			var clients []net.Conn
			for index := 0; index < 2; index++ {
				client, err := peer.Dial(ctx, "udp", net.JoinHostPort(address, "60000"))
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				_ = client.SetDeadline(time.Now().Add(15 * time.Second))
				clients = append(clients, client)
				if _, err := client.Write([]byte(fmt.Sprintf("client-%d", index))); err != nil {
					t.Fatal(err)
				}
			}
			for index, client := range clients {
				packet := make([]byte, 100)
				count, err := client.Read(packet)
				if err != nil || string(packet[:count]) != fmt.Sprintf("client-%d", index) {
					t.Fatalf("crossed or missing UDP reply: %q, %v", packet[:count], err)
				}
			}
			if first, second := <-sources, <-sources; first == second {
				t.Fatal("distinct peers shared one backend endpoint")
			}
			forwarder.Close()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("UDP forwarder did not shut down")
			}
		})
	}
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
