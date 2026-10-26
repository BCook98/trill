package setup

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	name string
	args []string
}
type fakeRunner struct {
	calls []call
	run   func(string, []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, append([]string(nil), args...)})
	if f.run != nil {
		return f.run(name, args)
	}
	return nil, errNotLoaded
}
func candidate(iface, ip string) Candidate { return Candidate{iface, net.ParseIP(ip)} }
func TestLANSelection(t *testing.T) {
	choices := []Candidate{candidate("lo0", "127.0.0.1"), candidate("en0", "fe80::1"), candidate("utun3", "100.73.54.69"), candidate("en1", "192.168.2.3"), candidate("en0", "2001:db8::2"), candidate("en0", "192.168.1.2")}
	cases := []struct {
		route, override, want string
		bad                   bool
	}{
		{"en0", "", "192.168.1.2", false}, {"en1", "", "192.168.2.3", false}, {"missing", "", "192.168.1.2", false},
		{"en0", "192.168.2.3", "192.168.2.3", false}, {"en0", "2001:db8::2", "2001:db8::2", false},
		{"en0", "192.168.9.9", "", true}, {"en0", "127.0.0.1", "", true}, {"en0", "invalid", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.route+tc.override, func(t *testing.T) {
			c, err := SelectLAN(choices, tc.route, tc.override)
			if tc.bad {
				if err == nil {
					t.Fatal("accepted unavailable override")
				}
				return
			}
			if err != nil || c.IP.String() != tc.want {
				t.Fatalf("got %v, %v; want %s", c, err, tc.want)
			}
		})
	}
	if _, err := SelectLAN(nil, "", ""); err == nil {
		t.Fatal("accepted no interfaces")
	}
	if got := RouteInterface(" route to: default\n interface: en7\n flags: UP\n"); got != "en7" {
		t.Fatal(got)
	}
}
func discovery(r *fakeRunner) Discovery {
	return Discovery{Runner: r, LocalHostname: func(context.Context) (string, error) { return "my-mac.local", nil }, LookPath: func(string) (string, error) { return "/usr/local/bin/tailscale", nil }, Interfaces: func() ([]Candidate, error) { return []Candidate{candidate("en0", "192.168.1.2")}, nil }, DefaultInterface: func(context.Context) string { return "en0" }}
}
func TestTailscaleDiscoveryPrefersConnectedNode(t *testing.T) {
	r := &fakeRunner{run: func(name string, args []string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"status", "--json"}) {
			t.Fatalf("unexpected mutating command: %v", args)
		}
		return []byte(`{"BackendState":"Running","Self":{"DNSName":"mac.example.ts.net.","TailscaleIPs":["100.73.54.69"],"Online":true}}`), nil
	}}
	n, err := discovery(r).Discover(context.Background(), "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "tailscale" || n.IP != "100.73.54.69" || n.Host != "mac.example.ts.net" {
		t.Fatalf("%+v", n)
	}
	if len(r.calls) != 1 {
		t.Fatalf("unexpected fallback calls: %v", r.calls)
	}
	n, err = discovery(r).Discover(context.Background(), "auto", "192.168.1.2")
	if err != nil || n.Kind != "lan" {
		t.Fatalf("override: %+v %v", n, err)
	}
	if _, err = discovery(r).Discover(context.Background(), "tailscale", "192.168.1.2"); err == nil {
		t.Fatal("accepted mismatched Tailscale override")
	}
}
func TestTailscaleAppDiscoveryAndFallback(t *testing.T) {
	r := &fakeRunner{run: func(name string, args []string) ([]byte, error) {
		if name != "/Applications/Tailscale.app/Contents/MacOS/Tailscale" {
			return nil, errors.New("missing")
		}
		return []byte(`{"BackendState":"Running","Self":{"DNSName":"mac.example.ts.net.","TailscaleIPs":["100.73.54.69"]}}`), nil
	}}
	n, err := discovery(r).Discover(context.Background(), "tailscale", "")
	if err != nil || !strings.HasPrefix(n.TailscaleCLI, "/Applications/") {
		t.Fatalf("%+v %v", n, err)
	}
	for _, status := range []string{`{}`, `invalid`, `{"BackendState":"Stopped"}`, `{"BackendState":"Running","Self":{"DNSName":"mac","TailscaleIPs":["100.73.54.69"]}}`, `{"BackendState":"Running","Self":{"DNSName":"mac.example.ts.net","TailscaleIPs":["100.73.54.69"],"Online":false}}`} {
		r.run = func(string, []string) ([]byte, error) { return []byte(status), nil }
		n, err = discovery(r).Discover(context.Background(), "auto", "")
		if err != nil || n.Kind != "lan" {
			t.Fatalf("%s: %+v %v", status, n, err)
		}
		if _, err = discovery(r).Discover(context.Background(), "tailscale", ""); err == nil {
			t.Fatal("accepted disconnected/invalid Tailscale")
		}
	}
	r.calls = nil
	n, err = discovery(r).Discover(context.Background(), "lan", "")
	if err != nil || n.Kind != "lan" || len(r.calls) != 0 {
		t.Fatalf("LAN unexpectedly queried tailscale: %v %v", r.calls, err)
	}
	if _, err = discovery(r).Discover(context.Background(), "funnel", ""); err == nil {
		t.Fatal("accepted invalid mode")
	}
}
func TestBoundedOutput(t *testing.T) {
	var b boundedOutput
	if _, err := b.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("secret")); err == nil {
		t.Fatal("output cap absent")
	}
	if b.Len() != 4<<20 {
		t.Fatal("oversize bytes retained")
	}
}

func TestBonjourHostnameRequired(t *testing.T) {
	for _, host := range []string{"", "192.168.1.2", "https://mac.local", "-bad.local", "bad-.local", "foo.example.com", "Mac name", "mac\n.local"} {
		if _, err := NormalizeLANHostname(host); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, host := range []string{"my-mac", "My-Mac.local", "my-mac.local."} {
		if got, err := NormalizeLANHostname(host); err != nil || got != "my-mac.local" {
			t.Fatalf("%s: %s %v", host, got, err)
		}
	}
	d := discovery(&fakeRunner{})
	d.LocalHostname = func(context.Context) (string, error) { return "", errors.New("Bonjour unavailable; use --hostname") }
	if _, err := d.Discover(context.Background(), "lan", ""); err == nil {
		t.Fatal("issued an unusable numeric-IP LAN address")
	}
}

func TestCommandOutputLimitCannotBeBypassedByIOCopy(t *testing.T) {
	var output boundedOutput
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", (4<<20)+1))}
	if _, err := io.Copy(&output, source); err == nil {
		t.Fatal("copy bypassed the subprocess stdout limit")
	}
	if output.Len() > 4<<20 {
		t.Fatal("captured unbounded certificate stdout")
	}
}

func TestNetworkDiagnosticsExplainFallbackWithoutLeakingCommandOutput(t *testing.T) {
	r := &fakeRunner{run: func(string, []string) ([]byte, error) {
		return []byte("SECRET STATUS OUTPUT"), errors.New("SECRET COMMAND ERROR")
	}}
	n, err := discovery(r).Discover(context.Background(), "auto", "")
	if err != nil || n.Kind != "lan" || !strings.Contains(n.Notice, "Auto selected LAN") || !strings.Contains(n.Notice, "--network tailscale") || strings.Contains(n.Notice, "SECRET") {
		t.Fatalf("unhelpful or unsafe fallback: %v %q", err, n.Notice)
	}
	_, err = discovery(r).Discover(context.Background(), "tailscale", "")
	if err == nil || !strings.Contains(err.Error(), "tailscale status --json") || !strings.Contains(err.Error(), "MagicDNS") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unhelpful or unsafe explicit failure: %v", err)
	}
	n, err = discovery(r).Discover(context.Background(), "lan", "")
	if err != nil || n.Notice != "" {
		t.Fatal("explicit LAN incorrectly described as fallback", err)
	}
}
