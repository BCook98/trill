package setup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state")
	if err := PrivateDir(p); err != nil {
		t.Fatal(err)
	}
	return p
}
func testConfig(dir string) Config {
	return Config{Version: 1, Name: "Mac & <name>", Network: "lan", IP: "192.168.1.2", Address: "https://my-mac.local:7443", Fingerprint: strings.Repeat("a", 64), Certificate: filepath.Join(dir, "tls.pem"), PrivateKey: filepath.Join(dir, "tls.pem"), Daemon: "/opt/Trill & Tools/trilld", CLI: "/opt/Trill & Tools/trillctl", Path: "/usr/bin:/opt/Trill & Tools/bin"}
}
func TestPrivateConfigRoundTripAndNoOverwrite(t *testing.T) {
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("%+v %v", got, err)
	}
	original, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	c.AllowControl = true
	if err = SaveConfig(dir, c); err == nil {
		t.Fatal("overwrote existing permission mode")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(original, after) {
		t.Fatal("changed existing config")
	}
	st, _ := os.Stat(filepath.Join(dir, "config.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	if err = os.Chmod(filepath.Join(dir, "config.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(dir); err == nil {
		t.Fatal("accepted public config")
	}
}
func TestRejectUnsafeFilesAndDirectories(t *testing.T) {
	dir := testDir(t)
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := WritePrivate(target, []byte("untouched"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(link); err == nil {
		t.Fatal("read a symlink")
	}
	if err := WritePrivate(link, []byte("modified"), true); err == nil {
		t.Fatal("replaced a symlink")
	}
	if err := WritePrivate(link, []byte("modified"), false); err == nil {
		t.Fatal("created over a symlink")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "untouched" {
		t.Fatal("modified target")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(alias); err == nil {
		t.Fatal("accepted symlink state dir")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(dir); err == nil {
		t.Fatal("accepted public state dir")
	}
}
func TestConfigValidation(t *testing.T) {
	dir := testDir(t)
	cases := []func(*Config){
		func(c *Config) { c.Address = "http://my-mac.local:7443" }, func(c *Config) { c.Address = "https://192.168.1.2:443" },
		func(c *Config) { c.Address += "/path" }, func(c *Config) { c.Address = "https://user@192.168.1.2:7443" },
		func(c *Config) { c.Fingerprint = "" }, func(c *Config) { c.IP = "0.0.0.0" }, func(c *Config) { c.IP = "127.0.0.1" },
		func(c *Config) { c.Name = "bad\x1bname" }, func(c *Config) { c.Certificate = "/tmp/foreign.pem" },
		func(c *Config) { c.PrivateKey = "/tmp/foreign.key" }, func(c *Config) { c.TmuxSocket = "relative.sock" },
		func(c *Config) { c.TmuxSocket = "/tmp/tmux-501/default" }, func(c *Config) { c.Daemon = "trilld" },
		func(c *Config) { c.Network = "tailscale" }, func(c *Config) { c.Version = 99 },
	}
	for i, change := range cases {
		c := testConfig(dir)
		change(&c)
		if err := c.Validate(dir); err == nil {
			t.Fatalf("accepted case %d: %+v", i, c)
		}
	}
}
func TestDaemonArgsReadOnlyAndLabIsolation(t *testing.T) {
	c := testConfig("/tmp/state")
	args := DaemonArgs(c, "/tmp/state")
	if strings.Contains(strings.Join(args, " "), "allow-control") {
		t.Fatal("control enabled by default")
	}
	if !strings.Contains(strings.Join(args, " "), "--pairing-store /tmp/state/devices.json") {
		t.Fatal(args)
	}
	c.AllowControl = true
	c.TmuxSocket = "/tmp/trill-lab.sock"
	args = DaemonArgs(c, "/tmp/state")
	if !reflect.DeepEqual(args[len(args)-3:], []string{"--allow-control", "--tmux-socket", "/tmp/trill-lab.sock"}) {
		t.Fatal(args)
	}
	path := DependencyPath("/Users/example", "/custom/bin:.:/usr/bin:/custom/bin")
	if !strings.Contains(path, "/Users/example/.local/bin") || strings.Contains(path, ":.:") || strings.Count(path, "/custom/bin") != 1 {
		t.Fatal(path)
	}
}
func TestQRContractAndLocalPNG(t *testing.T) {
	c := testConfig("/tmp/state")
	code := strings.Repeat("ab", 32)
	expiry := time.Date(2026, 10, 24, 9, 10, 11, 0, time.FixedZone("test", 3600))
	data, err := PairPayload(c, code, expiry)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": float64(1), "address": c.Address, "name": c.Name, "fingerprint": c.Fingerprint, "code": code, "expiresAt": "2026-10-24T08:10:11Z"}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("%v", m)
	}
	encoded := strings.TrimPrefix(PairURL(data), "trill://pair?data=")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal("deep link does not preserve raw JSON")
	}
	pngData, err := QRPNG(data)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(bytes.NewReader(pngData))
	if err != nil || image.Bounds().Dx() != 768 {
		t.Fatalf("invalid PNG: %v", err)
	}
	var terminal bytes.Buffer
	if err = PrintQR(&terminal, data); err != nil || terminal.Len() == 0 {
		t.Fatalf("missing terminal QR: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("a", 63), strings.Repeat("z", 64), strings.Repeat("a", 65), "é" + strings.Repeat("a", 64)} {
		if _, err = PairPayload(c, bad, expiry); err == nil {
			t.Fatal("accepted invalid code")
		}
	}
	c.Network = "tailscale"
	c.Address = "https://mac.example.ts.net:7443"
	c.Fingerprint = ""
	data, err = PairPayload(c, code, expiry)
	if err != nil || !bytes.Contains(data, []byte(`"fingerprint":""`)) {
		t.Fatal("empty trusted pin omitted")
	}
}
func TestUserspaceTailscaleConfigBindsLoopbackAndAdvertisesTailnet(t *testing.T) {
	dir := testDir(t)
	tailnet := func() Config {
		c := testConfig(dir)
		c.Network, c.IP, c.BindIP, c.Address, c.Fingerprint = "tailscale", "100.73.54.69", "127.0.0.1", "https://mac.example.ts.net:7443", ""
		c.TailscaleCLI, c.TailscaleSocket = "/opt/tailscale", "/run/ts.sock"
		return c
	}
	for _, advertisedIP := range []string{"100.73.54.69", "fd7a:115c:a1e0::1"} {
		t.Run(advertisedIP, func(t *testing.T) {
			c := tailnet()
			c.IP = advertisedIP
			if err := c.Validate(dir); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(DaemonArgs(c, dir), " ")
			if !strings.Contains(args, "--listen 127.0.0.1:7443 ") || !strings.Contains(args, "--mosh-host "+advertisedIP+" --mosh-bind 127.0.0.1") {
				t.Fatal(args)
			}
			c.BindIP = "::1"
			if err := c.Validate(dir); err == nil {
				t.Fatal("accepted IPv6 loopback instead of Tailscale's IPv4 forwarding target")
			}
		})
	}
	for i, change := range []func(*Config){
		func(c *Config) { c.BindIP = "::1" }, func(c *Config) { c.BindIP = "192.168.1.2" },
		func(c *Config) { c.TailscaleSocket = "relative.sock" }, func(c *Config) { c.TailscaleSocket = "/run/../ts.sock" },
	} {
		c := tailnet()
		change(&c)
		if err := c.Validate(dir); err == nil {
			t.Fatalf("accepted tailnet case %d: %+v", i, c)
		}
	}
	for i, change := range []func(*Config){func(c *Config) { c.BindIP = "127.0.0.1" }, func(c *Config) { c.TailscaleSocket = "/run/ts.sock" }} {
		c := testConfig(dir)
		change(&c)
		if err := c.Validate(dir); err == nil {
			t.Fatalf("accepted LAN case %d: %+v", i, c)
		}
	}
}

func TestEmbeddedConfigRequiresPinnedTailnetIdentity(t *testing.T) {
	directory := testDir(t)
	valid := testConfig(directory)
	valid.Network, valid.IP, valid.Address, valid.BindIP, valid.TSNetHostname = "tsnet", "100.73.54.69", "https://trill.example.ts.net:7443", "127.0.0.1", "trill"
	if err := valid.Validate(directory); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Fingerprint = "" }, func(c *Config) { c.TailscaleCLI = "/opt/tailscale" }, func(c *Config) { c.TailscaleSocket = "/tmp/tailscale.sock" },
		func(c *Config) { c.BindIP = "::1" }, func(c *Config) { c.TSNetHostname = "invalid.host" }, func(c *Config) { c.Address = "https://trill.local:7443" }, func(c *Config) { c.IP = "192.168.1.1" },
	} {
		c := valid
		change(&c)
		if err := c.Validate(directory); err == nil {
			t.Fatalf("accepted invalid embedded configuration: %+v", c)
		}
	}
}

func TestEmbeddedNodeStateHasExclusiveLifetimeLock(t *testing.T) {
	directory := testDir(t)
	unlock, err := LockTSNetState(directory)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := LockTSNetState(directory); err == nil {
		second()
		t.Fatal("two processes can share one embedded identity")
	}
	unlock()
	unlock, err = LockTSNetState(directory)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
