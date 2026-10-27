package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"trill/sidecar/internal/testcert"
	"trill/sidecar/pairing"
	"trill/sidecar/setup"
)

type setupRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f setupRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// Real private-file installation, fake service start. LaunchAgent lifecycle and
// unloaded-job behavior are separately exercised in setup's runner-fake tests.
type setupServiceFake struct {
	agents           setup.Agents
	installs, starts int
	startErr         error
}

func (f *setupServiceFake) Install(c setup.Config, dir string) error {
	f.installs++
	return f.agents.Install(c, dir)
}
func (f *setupServiceFake) Start(context.Context, setup.Config, string) error {
	f.starts++
	return f.startErr
}

func fakeSetup(t *testing.T) (*setupEnvironment, *setupServiceFake, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("public setup requires macOS")
	}
	home := t.TempDir()
	services := &setupServiceFake{agents: setup.Agents{Home: home, UID: 501}}
	unexpected := func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected certificate/discovery/service command")
		return nil, errors.New("unexpected command")
	}
	env := &setupEnvironment{
		agents: services, certs: setup.Certificates{Runner: setupRunnerFunc(unexpected), Now: time.Now},
		executable:    func() (string, error) { t.Fatal("rerun located a new executable"); return "", nil },
		checkListener: func(string) error { t.Fatal("rerun checked fresh-setup listener"); return nil },
		probe:         func(context.Context, setup.Config) error { return nil },
		discovery:     setup.Discovery{Interfaces: func() ([]setup.Candidate, error) { t.Fatal("rerun rediscovered network"); return nil, nil }},
	}
	return env, services, home
}

func runSetupTest(args []string, input string, env *setupEnvironment, home string) (string, string, error) {
	var out, errOut bytes.Buffer
	err := runWithSetupEnvironment(args, strings.NewReader(input), &out, &errOut, func() (string, error) { return home, nil }, env)
	return out.String(), errOut.String(), err
}

func privateSnapshot(t *testing.T, dir string, names ...string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		result[name] = data
	}
	return result
}
func assertSnapshot(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	for name, data := range before {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("changed preserved %s: %v", name, err)
		}
	}
}

func TestSetupResumesConfigOnlyAndPartialInstallation(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			env, services, home := fakeSetup(t)
			_, dir := fixture(t)
			if partial {
				c, _ := setup.LoadConfig(dir)
				if err := services.agents.Install(c, dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(home, "Library/LaunchAgents", setup.RenewalLabel+".plist")); err != nil {
					t.Fatal(err)
				}
			}
			before := privateSnapshot(t, dir, "config.json", "tls.pem")
			out, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home)
			if err != nil || services.installs != 1 || services.starts != 1 || !strings.Contains(out, "Using saved configuration") || !strings.Contains(out, "read-only") || !strings.Contains(out, "control --state-dir") || !strings.Contains(out, "--enable") {
				t.Fatalf("resume failed: %v; %s", err, out)
			}
			assertSnapshot(t, dir, before)
			if _, err := os.Stat(filepath.Join(dir, "devices.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("no-pair created auth state")
			}
			if _, err := os.Stat(filepath.Join(home, "Library/LaunchAgents", setup.RenewalLabel+".plist")); err != nil {
				t.Fatal("did not finish partial install", err)
			}
		})
	}
}

func TestSetupRerunRunningCompanionPreservesConnectionsAndCredentials(t *testing.T) {
	env, _, home := fakeSetup(t)
	c, dir := fixture(t)
	store := pairing.NewStore(filepath.Join(dir, "devices.json"))
	code, _, err := store.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.Redeem(code, "Existing phone")
	if err != nil {
		t.Fatal(err)
	}
	before := privateSnapshot(t, dir, "config.json", "tls.pem", "devices.json")
	queries := 0
	agents := setup.Agents{Home: home, UID: 501, ListenCheck: func(string) error { t.Fatal("probed occupied listener for loaded companion"); return nil }}
	agents.Runner = setupRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		queries++
		if name != "/bin/launchctl" || len(args) != 2 || args[0] != "print" {
			t.Fatalf("rerun interrupted loaded service: %s %v", name, args)
		}
		argv := append([]string{c.Daemon}, setup.DaemonArgs(c, dir)...)
		if strings.HasSuffix(args[1], setup.RenewalLabel) {
			argv = []string{c.CLI, "renew", "--state-dir", dir}
		}
		return []byte("gui/501/job = {\n\tprogram = " + argv[0] + "\n\targuments = {\n\t\t" + strings.Join(argv, "\n\t\t") + "\n\t}\n\tstate = running\n}\n"), nil
	})
	if err := agents.Install(c, dir); err != nil {
		t.Fatal(err)
	}
	env.agents = agents
	for _, input := range []string{"", "cancel\n"} {
		_, _, err := runSetupTest([]string{"setup", "--state-dir", dir}, input, env, home)
		if err == nil || !strings.Contains(err.Error(), "pairing not completed") || !strings.Contains(err.Error(), dir) {
			t.Fatalf("missing pairing recovery: %v", err)
		}
		assertSnapshot(t, dir, before)
	}
	out, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home)
	if err != nil || queries != 6 {
		t.Fatalf("not idempotent: queries=%d err=%v", queries, err)
	}
	assertSnapshot(t, dir, before)
	out, diagnostics, err := runSetupTest([]string{"setup", "--state-dir", dir}, "pair\n", env, home)
	if err != nil || !strings.Contains(out, "▀") && !strings.Contains(out, "▄") || !strings.Contains(diagnostics, "Scan pairing code") {
		t.Fatalf("missing terminal QR or instructions: %v", err)
	}
	if strings.Contains(out+diagnostics, token) {
		t.Fatal("printed permanent bearer")
	}
	if _, err := store.Authenticate(token); err != nil {
		t.Fatal("existing phone credential lost", err)
	}
	assertSnapshot(t, dir, map[string][]byte{"config.json": before["config.json"], "tls.pem": before["tls.pem"]})
}

func TestSetupRerunRejectsEveryConflictingCreationFlagBeforeEffects(t *testing.T) {
	cases := [][]string{
		{"--allow-control"}, {"--network", "tailscale"}, {"--network", "bogus"}, {"--ip", "192.168.9.9"},
		{"--hostname", "other.local"}, {"--hostname="}, {"--name", "Other Mac"}, {"--name="}, {"--daemon", "/other/trilld"},
		{"--backends", "removed-provider"}, {"--herdr-path", "/other/herdr"},
		{"--herdr-sessions", "work"}, {"--herdr-config-home", "/other/config"},
	}
	for _, flags := range cases {
		t.Run(flags[0], func(t *testing.T) {
			env, services, home := fakeSetup(t)
			_, dir := fixture(t)
			before := privateSnapshot(t, dir, "config.json", "tls.pem")
			args := append([]string{"setup", "--state-dir", dir, "--no-start"}, flags...)
			_, _, err := runSetupTest(args, "", env, home)
			if err == nil || !strings.Contains(err.Error(), "saved configuration conflicts") || !strings.Contains(err.Error(), strings.Split(flags[0], "=")[0]) || services.installs != 0 || services.starts != 0 {
				t.Fatalf("flag ignored or took effect: %v", err)
			}
			if flags[0] == "--allow-control" && (!strings.Contains(err.Error(), "control --state-dir") || !strings.Contains(err.Error(), "--enable")) {
				t.Fatal("missing explicit control next step", err)
			}
			assertSnapshot(t, dir, before)
		})
	}
}

func TestSetupMatchingFlagsAndNoStartPreserveSavedControl(t *testing.T) {
	for _, allow := range []bool{false, true} {
		env, services, home := fakeSetup(t)
		c, dir := fixture(t)
		c.AllowControl = allow
		if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
			t.Fatal(err)
		}
		if err := setup.SaveConfig(dir, c); err != nil {
			t.Fatal(err)
		}
		before := privateSnapshot(t, dir, "config.json", "tls.pem")
		for _, network := range []string{"lan", "auto"} {
			args := []string{"setup", "--state-dir", dir, "--no-start", "--no-pair", "--network", network, "--ip", c.IP, "--hostname", "MY-MAC.local.", "--name", c.Name, "--daemon", c.Daemon, fmt.Sprintf("--allow-control=%t", allow), "--backends", "herdr"}
			out, _, err := runSetupTest(args, "", env, home)
			if err != nil || services.installs != 0 || services.starts != 0 || !strings.Contains(out, "Services left unchanged") {
				t.Fatalf("matching flags rejected: %v %s", err, out)
			}
		}
		// Omitting --allow-control must not disable a previously enabled companion.
		if _, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-start"}, "", env, home); err != nil {
			t.Fatal(err)
		}
		assertSnapshot(t, dir, before)
	}
}

func TestSetupFailuresPreserveStateAndProvideRecovery(t *testing.T) {
	for _, failure := range []string{"expired LAN", "invalid config", "config symlink", "orphan certificate", "foreign plist", "start failed"} {
		t.Run(failure, func(t *testing.T) {
			env, services, home := fakeSetup(t)
			c, dir := fixture(t)
			want := ""
			switch failure {
			case "expired LAN":
				env.certs.Now = func() time.Time { return time.Now().AddDate(2, 0, 0) }
				want = "will not rotate its pinned identity"
			case "invalid config":
				if err := setup.WritePrivate(filepath.Join(dir, "config.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
				want = "cannot safely resume"
			case "config symlink":
				if err := os.Rename(filepath.Join(dir, "config.json"), filepath.Join(dir, "original.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original.json", filepath.Join(dir, "config.json")); err != nil {
					t.Fatal(err)
				}
				want = "cannot safely resume"
			case "orphan certificate":
				if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
					t.Fatal(err)
				}
				want = "tls.pem exists without config.json"
			case "foreign plist":
				foreign := c
				foreign.AllowControl = true
				if err := services.agents.Install(c, dir); err != nil {
					t.Fatal(err)
				}
				plist, err := setup.LaunchAgent(foreign, dir, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := setup.WritePrivate(filepath.Join(home, "Library/LaunchAgents", setup.Label+".plist"), plist, true); err != nil {
					t.Fatal(err)
				}
				want = "ownership"
			case "start failed":
				services.startErr = errors.New("fake launchd/listener failure")
				want = "private log"
			}
			names := []string{"tls.pem"}
			if failure != "orphan certificate" {
				names = append(names, "config.json")
			}
			before := privateSnapshot(t, dir, names...)
			_, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("not actionable: %v", err)
			}
			if failure != "start failed" && services.starts != 0 {
				t.Fatal("started after failed preflight")
			}
			if failure != "foreign plist" && failure != "start failed" && services.installs != 0 {
				t.Fatal("installed after failed preflight")
			}
			assertSnapshot(t, dir, before)
		})
	}
}

func TestFreshSetupThenResumeUsesSameIdentityAndExplicitControlChoice(t *testing.T) {
	for _, allow := range []bool{false, true} {
		env, services, home := fakeSetup(t)
		bin := filepath.Join(t.TempDir(), "private tools")
		if err := os.Mkdir(bin, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"trill", "trilld"} {
			if err := os.WriteFile(filepath.Join(bin, name), nil, 0700); err != nil {
				t.Fatal(err)
			}
		}
		env.executable = func() (string, error) { return filepath.Join(bin, "trill"), nil }
		env.checkListener = func(string) error { return nil }
		env.discovery = setup.Discovery{
			Interfaces: func() ([]setup.Candidate, error) {
				return []setup.Candidate{{Interface: "en0", IP: net.ParseIP("192.168.1.2")}}, nil
			},
			DefaultInterface: func(context.Context) string { return "en0" },
			LocalHostname:    func(context.Context) (string, error) { return "my-mac.local", nil },
		}
		dir := filepath.Join(t.TempDir(), "private state")
		args := []string{"setup", "--state-dir", dir, "--network", "lan", "--no-start", "--name", "Fixture Mac"}
		if allow {
			args = append(args, "--allow-control")
		}
		if _, _, err := runSetupTest(args, "", env, home); err != nil {
			t.Fatal(err)
		}
		c, err := setup.LoadConfig(dir)
		if err != nil || c.AllowControl != allow {
			t.Fatalf("wrong initial control: %v", err)
		}
		before := privateSnapshot(t, dir, "config.json", "tls.pem")
		if _, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home); err != nil {
			t.Fatal(err)
		}
		if services.installs != 1 || services.starts != 1 {
			t.Fatal("did not resume config-only setup")
		}
		assertSnapshot(t, dir, before)
	}
}

func TestRecoveryCommandsQuoteShellMetacharacters(t *testing.T) {
	c := setup.Config{CLI: "/a path/$(never)/trill'cli"}
	got := companionCommand(c, "/state/`never`/a'b", "pair")
	want := "'/a path/$(never)/trill'\"'\"'cli' pair --state-dir '/state/`never`/a'\"'\"'b'"
	if got != want {
		t.Fatalf("unsafe recovery command: %s", got)
	}
}

func TestSavedTailscaleTLSFailurePointsToExplicitRenewalWithoutIssuance(t *testing.T) {
	env, services, home := fakeSetup(t)
	c, dir := fixture(t)
	c.Network, c.IP, c.Address, c.Fingerprint, c.TailscaleCLI = "tailscale", "100.73.54.69", "https://mac.example.ts.net:7443", "", "/fake/tailscale"
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	before := privateSnapshot(t, dir, "config.json", "tls.pem")
	_, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home)
	if err == nil || !strings.Contains(err.Error(), companionCommand(c, dir, "renew")) || !strings.Contains(err.Error(), "setup did not replace") || services.installs != 0 || services.starts != 0 {
		t.Fatalf("unsafe or unhelpful saved TLS failure: %v", err)
	}
	assertSnapshot(t, dir, before)
}

func TestPairHTTPSFailureCreatesNoInvitationAndProvidesExactRecoveryCommands(t *testing.T) {
	c, dir := fixture(t)
	var out, diagnostics bytes.Buffer
	calls := 0
	err := approvePair(context.Background(), c, dir, "", false, false, false, true, strings.NewReader(""), &out, &diagnostics, func(context.Context, setup.Config) error {
		calls++
		return errors.New("fake unreachable HTTPS")
	})
	if err == nil || calls != 5 {
		t.Fatalf("HTTPS verification did not fail: %v", err)
	}
	for _, command := range []string{"status", "start", "pair"} {
		if !strings.Contains(err.Error(), companionCommand(c, dir, command)) {
			t.Fatalf("missing recovery %s: %v", command, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created invitation before verifying HTTPS")
	}
	if out.Len() != 0 {
		t.Fatal("printed QR despite unreachable HTTPS")
	}
}

func TestSetupCannotSilentlyDisableSavedControl(t *testing.T) {
	env, services, home := fakeSetup(t)
	c, dir := fixture(t)
	c.AllowControl = true
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	before := privateSnapshot(t, dir, "config.json", "tls.pem")
	_, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--allow-control=false", "--no-pair"}, "", env, home)
	if err == nil || !strings.Contains(err.Error(), "--disable") || services.installs != 0 || services.starts != 0 {
		t.Fatalf("accepted implicit permission change: %v", err)
	}
	assertSnapshot(t, dir, before)
}

func TestFreshTailscaleSetupRejectsLANHostnameBeforeCertificateOrListener(t *testing.T) {
	env, services, home := fakeSetup(t)
	bin := t.TempDir()
	for _, name := range []string{"trill", "trilld"} {
		if err := os.WriteFile(filepath.Join(bin, name), nil, 0700); err != nil {
			t.Fatal(err)
		}
	}
	env.executable = func() (string, error) { return filepath.Join(bin, "trill"), nil }
	env.discovery = setup.Discovery{
		LookPath: func(string) (string, error) { return "/fake/tailscale", nil },
		Runner: setupRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") != "status --json" {
				t.Fatal("mutating discovery command", args)
			}
			return []byte(`{"BackendState":"Running","Self":{"DNSName":"mac.example.ts.net.","TailscaleIPs":["100.73.54.69"]}}`), nil
		}),
	}
	dir := filepath.Join(t.TempDir(), "state")
	_, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--network", "tailscale", "--hostname", "my-mac.local"}, "", env, home)
	if err == nil || !strings.Contains(err.Error(), "--hostname is LAN-only") || services.installs != 0 || services.starts != 0 {
		t.Fatalf("ignored incompatible hostname: %v", err)
	}
	for _, name := range []string{"config.json", "tls.pem", "devices.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created state despite incompatible flags", name)
		}
	}
}

func TestSetupResumeRejectsInterveningConfigOnlyControlDisable(t *testing.T) {
	env, services, home := fakeSetup(t)
	c, dir := fixture(t)
	if err := services.agents.Control(context.Background(), c, dir, true); err != nil {
		t.Fatal(err)
	}
	enabled, err := setup.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Certificate preflight runs after setup loads its configuration snapshot,
	// providing a deterministic point for a competing command to finish.
	disabled := false
	env.certs.Now = func() time.Time {
		if !disabled {
			if err := services.agents.Control(context.Background(), enabled, dir, false); err != nil {
				t.Fatal(err)
			}
			disabled = true
		}
		return time.Now()
	}
	before := privateSnapshot(t, dir, "tls.pem")
	_, _, err = runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair"}, "", env, home)
	if err == nil || !strings.Contains(err.Error(), "configuration changed") || !disabled || services.starts != 0 {
		t.Fatalf("resumed stale enabled configuration: %v", err)
	}
	actual, err := setup.LoadConfig(dir)
	if err != nil || actual.AllowControl {
		t.Fatalf("lost the intervening disable: %+v %v", actual, err)
	}
	assertSnapshot(t, dir, before)
	for _, label := range []string{setup.Label, setup.RenewalLabel} {
		if _, err := os.Stat(filepath.Join(home, "Library/LaunchAgents", label+".plist")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("installed a stale LaunchAgent: %v", err)
		}
	}
}

func TestSetupProviderFlagsCompareEffectiveSelection(t *testing.T) {
	for _, tc := range []struct {
		name, backends, path, sessions string
		flags                          []string
		conflict                       bool
	}{
		{name: "implicit Herdr", flags: []string{"--backends=", "--herdr-path=", "--herdr-sessions="}},
		{name: "explicit Herdr", backends: "herdr", flags: []string{"--backends="}},
		{name: "implicit Herdr defaults", backends: "herdr", flags: []string{"--backends", "herdr", "--herdr-path=", "--herdr-sessions=", "--herdr-config-home="}},
		{name: "explicit Herdr defaults", backends: "herdr", path: "herdr", sessions: "default", flags: []string{"--herdr-path=", "--herdr-sessions="}},
		{name: "omitted custom options", backends: "herdr", path: "/custom/herdr", sessions: "work", flags: []string{"--backends", "herdr"}},
		{name: "empty path changes custom option", backends: "herdr", path: "/custom/herdr", flags: []string{"--herdr-path="}, conflict: true},
		{name: "empty sessions changes custom option", backends: "herdr", sessions: "work", flags: []string{"--herdr-sessions="}, conflict: true},
		{name: "empty backend keeps Herdr", backends: "herdr", flags: []string{"--backends="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, services, home := fakeSetup(t)
			c, dir := fixture(t)
			c.Backends, c.HerdrPath, c.HerdrSessions = tc.backends, tc.path, tc.sessions
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "herdr-config"))
			if tc.backends == "herdr" {
				c.HerdrConfigHome = os.Getenv("XDG_CONFIG_HOME")
			}
			if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
				t.Fatal(err)
			}
			if err := setup.SaveConfig(dir, c); err != nil {
				t.Fatal(err)
			}
			before := privateSnapshot(t, dir, "config.json", "tls.pem")
			args := append([]string{"setup", "--state-dir", dir, "--no-start"}, tc.flags...)
			_, _, err := runSetupTest(args, "", env, home)
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "saved configuration conflicts") {
					t.Fatalf("accepted changed provider defaults: %v", err)
				}
			} else if err != nil {
				t.Fatal("rejected equivalent provider options:", err)
			}
			if services.installs != 0 || services.starts != 0 {
				t.Fatal("provider comparison changed services")
			}
			assertSnapshot(t, dir, before)
		})
	}
}

func TestTailscaleSocketFlagsConflictWithSavedConfiguration(t *testing.T) {
	env, services, home := fakeSetup(t)
	_, dir := fixture(t)
	before := privateSnapshot(t, dir, "config.json", "tls.pem")
	for _, flag := range []string{"--tailscale-socket", "--tailscale-cli"} {
		_, _, err := runSetupTest([]string{"setup", "--state-dir", dir, "--no-pair", flag, "/run/ts.sock"}, "", env, home)
		if err == nil || !strings.Contains(err.Error(), flag) || services.installs != 0 {
			t.Fatalf("%s: accepted a different Tailscale daemon for saved state: %v", flag, err)
		}
	}
	assertSnapshot(t, dir, before)
}

func TestEmbeddedSetupEnrollsOnceAndPreservesTrustedIdentity(t *testing.T) {
	env, services, home := fakeSetup(t)
	bin := t.TempDir()
	for _, name := range []string{"trill", "trilld"} {
		if err := os.WriteFile(filepath.Join(bin, name), nil, 0700); err != nil {
			t.Fatal(err)
		}
	}
	env.executable = func() (string, error) { return filepath.Join(bin, "trill"), nil }
	env.checkListener = func(ip string) error {
		if ip != "127.0.0.1" {
			t.Fatalf("unexpected listener %s", ip)
		}
		return nil
	}
	certificateBundle, certificateRoots := testcert.Bundle(t, "trill-test.example.ts.net", time.Now(), 90*24*time.Hour)
	env.certs.Roots = certificateRoots
	enrollments := 0
	env.enrollTSNet = func(_ context.Context, directory, hostname string, _ io.Writer) (setup.Network, error) {
		enrollments++
		if hostname != "trill-test" {
			t.Fatalf("unexpected embedded hostname %s", hostname)
		}
		certificatePath := filepath.Join(directory, "tls.pem")
		if _, err := os.Stat(certificatePath); errors.Is(err, os.ErrNotExist) {
			if err := setup.WritePrivate(certificatePath, certificateBundle, false); err != nil {
				t.Fatal(err)
			}
		}
		return setup.Network{Kind: "tsnet", IP: "100.73.54.69", Host: "trill-test.example.ts.net", BindIP: "127.0.0.1"}, nil
	}
	directory := filepath.Join(t.TempDir(), "state")
	_, _, err := runSetupTest([]string{"setup", "--state-dir", directory, "--network", "tsnet", "--tsnet-hostname", "trill-test", "--no-start", "--no-pair"}, "", env, home)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := setup.LoadConfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Network != "tsnet" || configuration.TSNetHostname != "trill-test" || configuration.Fingerprint != "" || configuration.TailscaleCLI != "" || configuration.TailscaleSocket != "" || configuration.AllowControl {
		t.Fatalf("incorrect embedded configuration: %+v", configuration)
	}
	before := privateSnapshot(t, directory, "config.json", "tls.pem")
	_, _, err = runSetupTest([]string{"setup", "--state-dir", directory, "--no-pair"}, "", env, home)
	if err != nil || enrollments != 1 || services.installs != 1 || services.starts != 1 {
		t.Fatalf("embedded rerun changed identity or failed to start: %v; enrollments=%d", err, enrollments)
	}
	assertSnapshot(t, directory, before)
	// Login must not create a replacement node when the stored identity is lost.
	_, _, err = runSetupTest([]string{"tailnet-login", "--state-dir", directory}, "", env, home)
	if err == nil || enrollments != 1 {
		t.Fatal("login attempted to replace a missing embedded identity")
	}
	nodeDirectory := filepath.Join(directory, "tsnet")
	if err := setup.PrivateDir(nodeDirectory); err != nil {
		t.Fatal(err)
	}
	if err := setup.WritePrivate(filepath.Join(nodeDirectory, "tailscaled.state"), []byte("synthetic enrollment"), false); err != nil {
		t.Fatal(err)
	}
	_, _, err = runSetupTest([]string{"tailnet-login", "--state-dir", directory}, "", env, home)
	if err != nil || enrollments != 2 || services.starts != 1 {
		t.Fatalf("explicit login changed service state: %v", err)
	}
	assertSnapshot(t, directory, before)
	args := strings.Join(setup.DaemonArgs(configuration, directory), " ")
	if !strings.Contains(args, "--tsnet-state-dir "+filepath.Join(directory, "tsnet")) || !strings.Contains(args, "--tsnet-ip 100.73.54.69") || !strings.Contains(args, "--tsnet-hostname trill-test") {
		t.Fatal(args)
	}
	_, _, err = runSetupTest([]string{"setup", "--state-dir", directory, "--tsnet-hostname", "another-node", "--no-pair"}, "", env, home)
	if err == nil || enrollments != 2 {
		t.Fatal("accepted an implicit embedded identity change")
	}
	assertSnapshot(t, directory, before)
}
