package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Runner is injected in tests; stdout from credential/cert commands is never
// surfaced to the caller. No commands connect, log in, or change a tailnet.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

var errNotLoaded = errors.New("LaunchAgent is not loaded")

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var output boundedOutput
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if name == "/bin/launchctl" && len(args) == 2 && args[0] == "print" && errors.As(err, &exit) && exit.ExitCode() == 113 {
			return nil, errNotLoaded
		}
		return nil, errors.New("local command failed")
	}
	return output.Bytes(), nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Len() int      { return b.buffer.Len() }
func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > (4<<20)-b.Len() {
		return 0, errors.New("local command output too large")
	}
	return b.buffer.Write(p)
}

type Candidate struct {
	Interface string
	IP        net.IP
}
type Network struct {
	Kind, IP, Host, TailscaleCLI string
	Choices                      []Candidate
	Notice                       string
}
type Discovery struct {
	Runner           Runner
	LookPath         func(string) (string, error)
	Interfaces       func() ([]Candidate, error)
	DefaultInterface func(context.Context) string
	LocalHostname    func(context.Context) (string, error)
}

func NewDiscovery() Discovery {
	r := ExecRunner{}
	return Discovery{Runner: r, LocalHostname: func(ctx context.Context) (string, error) {
		b, err := r.Run(ctx, "/usr/sbin/scutil", "--get", "LocalHostName")
		if err != nil {
			return "", errors.New("cannot detect a Bonjour hostname; set the Mac LocalHostName or pass --hostname my-mac.local")
		}
		return NormalizeLANHostname(strings.TrimSpace(string(b)))
	}, LookPath: exec.LookPath, Interfaces: LocalAddresses, DefaultInterface: func(ctx context.Context) string {
		b, err := r.Run(ctx, "/sbin/route", "-n", "get", "default")
		if err != nil {
			return ""
		}
		return RouteInterface(string(b))
	}}
}
func RouteInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "interface" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func usableIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func isTailscale(ip net.IP) bool {
	_, v4, _ := net.ParseCIDR("100.64.0.0/10")
	_, v6, _ := net.ParseCIDR("fd7a:115c:a1e0::/48")
	return v4.Contains(ip) || v6.Contains(ip)
}
func LocalAddresses() ([]Candidate, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && usableIP(ip) && !isTailscale(ip) {
				out = append(out, Candidate{i.Name, ip})
			}
		}
	}
	return out, nil
}
func SelectLAN(candidates []Candidate, defaultInterface, override string) (Candidate, error) {
	valid := []Candidate{}
	for _, c := range candidates {
		if usableIP(c.IP) && !isTailscale(c.IP) {
			valid = append(valid, c)
		}
	}
	if override != "" {
		ip := net.ParseIP(override)
		if !usableIP(ip) {
			return Candidate{}, errors.New("--ip requires a concrete reachable local IP")
		}
		for _, c := range valid {
			if c.IP.Equal(ip) {
				return c, nil
			}
		}
		return Candidate{}, errors.New("--ip is not assigned to an active LAN interface")
	}
	sort.SliceStable(valid, func(i, j int) bool {
		a, b := valid[i], valid[j]
		if (a.Interface == defaultInterface) != (b.Interface == defaultInterface) {
			return a.Interface == defaultInterface
		}
		if (a.IP.To4() != nil) != (b.IP.To4() != nil) {
			return a.IP.To4() != nil
		}
		if a.Interface != b.Interface {
			return a.Interface < b.Interface
		}
		return a.IP.String() < b.IP.String()
	})
	if len(valid) == 0 {
		return Candidate{}, errors.New("no active LAN address; connect Wi-Fi/Ethernet or use a connected Tailscale node")
	}
	return valid[0], nil
}
func (d Discovery) tailscale(ctx context.Context, override string) (Network, error) {
	paths := []string{}
	if path, err := d.LookPath("tailscale"); err == nil {
		paths = append(paths, path)
	}
	paths = append(paths, "/Applications/Tailscale.app/Contents/MacOS/Tailscale")
	for _, path := range paths {
		b, err := d.Runner.Run(ctx, path, "status", "--json")
		if err != nil {
			continue
		}
		var status struct {
			BackendState string
			Self         *struct {
				DNSName      string
				TailscaleIPs []string
				Online       *bool
			}
		}
		if json.Unmarshal(b, &status) != nil || status.BackendState != "Running" || status.Self == nil || (status.Self.Online != nil && !*status.Self.Online) {
			continue
		}
		host := strings.TrimSuffix(status.Self.DNSName, ".")
		if !strings.HasSuffix(host, ".ts.net") || strings.ContainsAny(host, " /:\t\n") {
			continue
		}
		addresses := append([]string(nil), status.Self.TailscaleIPs...)
		if override != "" {
			for i, s := range addresses {
				if net.ParseIP(s).Equal(net.ParseIP(override)) {
					addresses[0], addresses[i] = addresses[i], addresses[0]
					break
				}
			}
		}
		for _, s := range addresses {
			ip := net.ParseIP(s)
			if usableIP(ip) && isTailscale(ip) {
				return Network{Kind: "tailscale", IP: ip.String(), Host: host, TailscaleCLI: path}, nil
			}
		}
	}
	return Network{}, errors.New("no connected Tailscale node with a full .ts.net DNS name; open/connect Tailscale on this Mac, check tailscale status --json (BackendState Running, Self.DNSName ending .ts.net), and ensure MagicDNS is enabled; retry --network tailscale, or explicitly choose --network lan")
}
func (d Discovery) Discover(ctx context.Context, mode, override string) (Network, error) {
	if mode != "auto" && mode != "tailscale" && mode != "lan" {
		return Network{}, errors.New("--network must be auto, tailscale, or lan")
	}
	if override != "" && !usableIP(net.ParseIP(override)) {
		return Network{}, errors.New("invalid --ip; use an address assigned to an active interface on this Mac")
	}
	var notice string
	if mode != "lan" {
		n, err := d.tailscale(ctx, override)
		if err == nil {
			if override == "" || net.ParseIP(override).Equal(net.ParseIP(n.IP)) {
				return n, nil
			}
			if mode == "tailscale" {
				return Network{}, errors.New("--ip does not match the connected Tailscale node")
			}
		} else if mode == "tailscale" {
			return Network{}, err
		}
		notice = "Auto selected LAN: no usable connected Tailscale address matched this request. For remote access, connect Tailscale with a full .ts.net name before first setup, or choose --network tailscale to diagnose it."
	}
	choices, err := d.Interfaces()
	if err != nil {
		return Network{}, err
	}
	selected, err := SelectLAN(choices, d.DefaultInterface(ctx), override)
	if err != nil {
		return Network{}, err
	}
	if d.LocalHostname == nil {
		return Network{}, errors.New("Bonjour hostname discovery unavailable; provide --hostname my-mac.local")
	}
	host, err := d.LocalHostname(ctx)
	if err != nil {
		return Network{}, err
	}
	host, err = NormalizeLANHostname(host)
	if err != nil {
		return Network{}, err
	}
	return Network{Kind: "lan", IP: selected.IP.String(), Host: host, Choices: choices, Notice: notice}, nil
}
func CheckListener(ip string) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, Port))
	if err != nil {
		return fmt.Errorf("cannot bind %s:%s; another listener or an unavailable address may be present. Check trill status with the same --state-dir and confirm the saved IP is still assigned; setup/start never stop another listener", ip, Port)
	}
	return ln.Close()
}

func Executable(path string) error {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("executable unavailable: %s", path)
	}
	return nil
}

// NormalizeLANHostname accepts a single Bonjour label, optionally suffixed with
// .local. A numeric-IP URL does not support the iOS local-network TLS flow.
func NormalizeLANHostname(host string) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	label := strings.TrimSuffix(host, ".local")
	if len(label) < 1 || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || label[0] == '-' || label[len(label)-1] == '-' {
		return "", errors.New("LAN HTTPS requires a Bonjour name such as my-mac.local; pass --hostname and ensure it resolves on the phone's LAN")
	}
	return label + ".local", nil
}
