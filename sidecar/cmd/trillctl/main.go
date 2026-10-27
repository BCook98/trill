// Built as public trill and private legacy trillctl. Setup never invokes Xirp or tmux.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"trill/sidecar/buildinfo"
	"trill/sidecar/demoapp"
	"trill/sidecar/distribution"
	"trill/sidecar/pairing"
	"trill/sidecar/providers"
	"trill/sidecar/setup"
)

const help = `Usage: trill <command> [options]

  version     Print companion version, source commit and architecture as JSON
  doctor      Diagnose installation, dependencies, TLS and owned services
  update      Check for or install a verified signed companion release
  demo        Run an isolated scripted demo with real HTTPS and Mosh
  serve       Run the foreground daemon with daemon flags (serve --help)
  setup       Set up or resume this companion, then offer a pairing QR
  pair        Approve one device; single-use invitation expires in 10 minutes
  tailnet-login  Reauthenticate an enrolled embedded node (stop it first)
  devices     List paired devices, including revoked devices
  revoke ID   Revoke a device and save the revocation
  status      Show configuration, TLS expiry, and owned LaunchAgent status
  providers   Show or change Xirp/Herdr providers; restart only the owned companion
  control     Explicitly enable/disable control; restart owned daemon if loaded
  start       Start the installed companion and certificate renewal agent
  stop        Stop only this companion's two LaunchAgents
  install     Install user LaunchAgents without starting
  uninstall   Stop/remove only these LaunchAgents; keep private state
  renew       Renew the managed certificate (used by launchd)

Management commands accept --state-dir (default ~/.config/trill).
Use trill <command> --help for options. Read-only by default; control requires
trill control --enable (or --allow-control on first setup).
Rerunning setup preserves settings and running connections.
Pairing never prints a permanent bearer token.
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "trill:", err)
		os.Exit(1)
	}
}
func run(args []string, in io.Reader, out, errOut io.Writer) error {
	return runWithHome(args, in, out, errOut, os.UserHomeDir)
}

func runWithHome(args []string, in io.Reader, out, errOut io.Writer, userHome func() (string, error)) error {
	return runWithSetupEnvironment(args, in, out, errOut, userHome, nil)
}

func runWithSetupEnvironment(args []string, in io.Reader, out, errOut io.Writer, userHome func() (string, error), environment *setupEnvironment) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, help)
		return err
	}
	command := args[0]
	if command == "version" {
		if len(args) != 1 {
			return errors.New("usage: trill version")
		}
		return buildinfo.Write(out)
	}
	if command == "doctor" || command == "update" {
		home, err := userHome()
		if err != nil {
			return err
		}
		if command == "doctor" {
			return doctor(args[1:], out, errOut, home)
		}
		return update(args[1:], out, errOut, home)
	}
	if command == "demo" {
		file, ok := in.(*os.File)
		if !ok && len(args) > 1 && args[1] == "terminal" {
			return fmt.Errorf("demo terminal requires a PTY")
		}
		return demoapp.Run(args[1:], file, out, errOut)
	}
	if command == "serve" {
		return serve(args[1:], os.Executable, syscall.Exec)
	}
	switch command {
	case "setup", "pair", "devices", "revoke", "status", "start", "stop", "install", "uninstall", "renew", "control", "providers", "tailnet-login":
	default:
		return fmt.Errorf("unknown command %q; use trill --help", command)
	}
	dir, err := setup.DefaultStateDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.Usage = func() {
		fmt.Fprintf(errOut, "Usage: trill %s [options]\n", command)
		if command == "setup" {
			fmt.Fprintln(errOut, "Create a companion or resume saved setup without changing its identity, settings or running connections.")
			fmt.Fprintln(errOut, "New companions are read-only. Use trill control --enable later for terminal access and session changes.")
			fmt.Fprintln(errOut, "Reruns accept matching flags and reject changes. Use --no-pair to keep using existing phone pairings.")
		}
		flags.PrintDefaults()
	}
	flags.StringVar(&dir, "state-dir", dir, "private state directory (includes devices.json)")
	var mode, ip, name, daemon, tmuxSocket, png, hostname, tailscaleCLI, tailscaleSocket, tsnetHostname string
	var backends, herdrPath, herdrSessions, herdrConfigHome string
	var allowControl, noStart, noPair, yes, open, rawJSON, pasteURL, enable, disable bool
	if command == "setup" {
		flags.StringVar(&mode, "network", "auto", "new setup: auto, tailscale, lan, or tsnet (embedded Tailscale); reruns keep saved network")
		flags.StringVar(&ip, "ip", "", "override with an address assigned to this Mac")
		flags.StringVar(&tsnetHostname, "tsnet-hostname", "trill", "embedded Tailscale node hostname; requires --network tsnet")
		flags.StringVar(&hostname, "hostname", "", "LAN Bonjour name override, e.g. my-mac.local (must resolve on phone)")
		flags.StringVar(&tailscaleCLI, "tailscale-cli", "", "absolute Tailscale CLI path (default tailscale on PATH, then the Tailscale app)")
		flags.StringVar(&tailscaleSocket, "tailscale-socket", "", "absolute tailscaled socket, for a second tailnet such as a userspace tailscaled")
		host, _ := os.Hostname()
		flags.StringVar(&name, "name", host, "computer name shown on the phone")
		flags.StringVar(&daemon, "daemon", "", "absolute trilld path (default beside trill)")
		flags.BoolVar(&allowControl, "allow-control", false, "first setup only: enable terminal/session control; later use trill control --enable")
		flags.StringVar(&tmuxSocket, "tmux-socket", "", "absolute dedicated tmux lab socket; isolates backend from Xirp")
		flags.BoolVar(&noStart, "no-start", false, "create or reuse configuration only; leave services as they are")
		flags.BoolVar(&noPair, "no-pair", false, "skip interactive pairing after setup")
	}
	if command == "setup" || command == "providers" {
		flags.StringVar(&herdrConfigHome, "herdr-config-home", "", "absolute XDG config base containing herdr/ (default current XDG_CONFIG_HOME or ~/.config)")
		flags.StringVar(&backends, "backends", "", "xirp (default), herdr, or xirp,herdr")
		flags.StringVar(&herdrPath, "herdr-path", "", "Herdr executable (default herdr on PATH)")
		flags.StringVar(&herdrSessions, "herdr-sessions", "", "comma-separated existing Herdr servers (default default)")
	}

	if command == "control" {
		flags.BoolVar(&enable, "enable", false, "enable control; disconnect Trill transport, preserve underlying sessions")
		flags.BoolVar(&disable, "disable", false, "disable control; disconnect Trill transport, preserve underlying sessions")
	}
	if command == "pair" {
		flags.StringVar(&png, "png", "", "write QR to a new private PNG file instead of terminal")
		flags.BoolVar(&open, "open", false, "open the file specified by --png")
		flags.BoolVar(&rawJSON, "json", false, "print raw invitation JSON instead of QR")
		flags.BoolVar(&pasteURL, "url", false, "print a trill://pair URL instead of QR")
		flags.BoolVar(&yes, "yes", false, "explicitly approve one invitation without prompting")
	}
	if command == "devices" {
		flags.BoolVar(&rawJSON, "json", false, "print device JSON for scripts")
	}
	var id string
	rest := args[1:]
	if command == "revoke" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		id = rest[0]
		rest = rest[1:]
	}
	if err = flags.Parse(rest); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if command == "revoke" && id == "" && flags.NArg() == 1 {
		id = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return errors.New("unexpected arguments; use command --help")
	}
	if command == "revoke" && id == "" {
		return errors.New("revoke requires a device ID")
	}
	if command == "control" && enable == disable {
		return errors.New("control requires exactly one of --enable or --disable")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = filepath.Clean(dir)
	ctx, cancelCommand := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelCommand()
	home, err := userHome()
	if err != nil {
		return err
	}
	agents := setup.Agents{Runner: setup.ExecRunner{}, Home: home, UID: os.Getuid()}
	certs := setup.NewCertificates()
	if command == "setup" {
		if runtime.GOOS != "darwin" {
			return errors.New("companion LaunchAgents require macOS")
		}
		if environment == nil {
			environment = &setupEnvironment{agents: agents, certs: certs, discovery: setup.NewDiscovery(), executable: os.Executable, checkListener: setup.CheckListener, probe: probe, enrollTSNet: enrollTSNet}
		}
		certs = environment.certs
		if _, err = os.Lstat(filepath.Join(dir, "config.json")); err == nil {
			c, err := setup.LoadConfig(dir)
			if err != nil {
				return fmt.Errorf("cannot safely resume saved configuration; no settings changed: %w", err)
			}
			if err := requireMatchingHomebrewInstallation(c); err != nil {
				return err
			}
			if err := compatibleSetupFlags(flags, c, dir); err != nil {
				return err
			}
			fmt.Fprintf(out, "Using saved configuration for %s at %s. Identity and paired devices preserved.\n", c.Name, c.Address)
			return finishSetup(ctx, c, dir, noStart, noPair, *environment, in, out, errOut)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err = os.Lstat(filepath.Join(dir, "tls.pem")); err == nil {
			return errors.New("tls.pem exists without config.json; refusing to replace an existing identity or request another certificate. Preserve this state and use a fresh --state-dir for setup")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = setup.PrivateDir(dir); err != nil {
			return err
		}
		cli, err := environment.executable()
		if err != nil {
			return err
		}
		cli, err = filepath.EvalSymlinks(cli)
		if err != nil {
			return err
		}
		cli = distribution.StableCLI(home, cli)
		if daemon == "" {
			daemon = filepath.Join(filepath.Dir(cli), "trilld")
		}
		daemon, err = filepath.Abs(daemon)
		if err != nil {
			return err
		}
		if err = setup.Executable(daemon); err != nil {
			return err
		}
		var explicitTSNetHostname bool
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "tsnet-hostname" {
				explicitTSNetHostname = true
			}
		})
		if explicitTSNetHostname && mode != "tsnet" {
			return errors.New("--tsnet-hostname requires --network tsnet")
		}
		if mode == "tsnet" && (ip != "" || hostname != "" || tailscaleCLI != "" || tailscaleSocket != "") {
			return errors.New("--network tsnet discovers its own identity; omit --ip, --hostname, --tailscale-cli and --tailscale-socket")
		}
		discovery := environment.discovery
		if tailscaleCLI != "" || tailscaleSocket != "" {
			if mode == "lan" {
				return errors.New("--tailscale-cli and --tailscale-socket require --network tailscale")
			}
			mode = "tailscale"
			for _, path := range []string{tailscaleCLI, tailscaleSocket} {
				if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path) {
					return errors.New("--tailscale-cli and --tailscale-socket must be absolute clean paths")
				}
			}
			discovery.TailscaleCLI, discovery.TailscaleSocket = tailscaleCLI, tailscaleSocket
		}
		if hostname != "" {
			discovery.LocalHostname = func(context.Context) (string, error) { return setup.NormalizeLANHostname(hostname) }
		}
		var n setup.Network
		if mode == "tsnet" {
			if err = setup.ValidateTSNetHostname(tsnetHostname); err != nil {
				return err
			}
			if environment.enrollTSNet == nil {
				return errors.New("embedded Tailscale enrollment unavailable")
			}
			fmt.Fprintln(errOut, "Registering Trill as an independent tailnet device. Open the Tailscale login URL when prompted. The iPhone must join the same tailnet.")
			n, err = environment.enrollTSNet(ctx, dir, tsnetHostname, errOut)
		} else {
			n, err = discovery.Discover(ctx, mode, ip)
		}
		if err != nil {
			return err
		}
		if hostname != "" && n.Kind != "lan" {
			return errors.New("--hostname is LAN-only; choose --network lan with --hostname, or omit --hostname for Tailscale")
		}
		if n.Notice != "" {
			fmt.Fprintln(errOut, n.Notice)
		}
		fmt.Fprintf(errOut, "Selected %s: %s (HTTPS port 7443).\n", n.Kind, n.Host)
		if n.Kind == "lan" {
			fmt.Fprintln(errOut, "LAN pairing requires Bonjour resolution of this .local hostname and both devices on the same reachable LAN. HTTPS uses the hostname; mosh uses the selected IP.")
		}
		if n.Kind == "lan" && len(n.Choices) > 1 {
			fmt.Fprintln(errOut, "Multiple local addresses found; default-route interface and IPv4 are preferred. Use --network lan --ip ADDRESS to choose another:")
			for _, choice := range n.Choices {
				fmt.Fprintf(errOut, "  %s  %s\n", choice.Interface, choice.IP)
			}
		}
		if n.BindIP != "" && n.Kind != "tsnet" {
			fmt.Fprintf(errOut, "%s is not assigned to a local interface, as with a userspace-networking tailscaled. The companion binds %s, where tailscaled delivers inbound tailnet traffic, and advertises %s.\n", n.IP, n.BindIP, n.IP)
		}
		c := setup.Config{Backends: backends, HerdrConfigHome: herdrConfigHome, HerdrPath: herdrPath, HerdrSessions: herdrSessions, Version: 1, Name: name, Network: n.Kind, IP: n.IP, BindIP: n.BindIP, Address: "https://" + net.JoinHostPort(n.Host, setup.Port), Certificate: filepath.Join(dir, "tls.pem"), PrivateKey: filepath.Join(dir, "tls.pem"), TailscaleCLI: n.TailscaleCLI, TailscaleSocket: n.TailscaleSocket, AllowControl: allowControl, TmuxSocket: tmuxSocket, Daemon: daemon, CLI: cli, Path: setup.DependencyPath(home, os.Getenv("PATH"))}
		if n.Kind == "tsnet" {
			c.TSNetHostname = tsnetHostname
		}
		if err = environment.checkListener(c.ListenIP()); err != nil {
			return err
		}
		if strings.Contains(","+backends+",", ",herdr,") {
			c.HerdrConfigHome, err = providers.HerdrHome(herdrConfigHome)
			if err != nil {
				return err
			}
		}
		if n.Kind == "lan" {
			c.Fingerprint = strings.Repeat("0", 64)
		}
		if err = c.Validate(dir); err != nil {
			return err
		}
		if n.Kind == "tailscale" {
			fmt.Fprintln(errOut, "Requesting a trusted certificate using the existing Tailscale CLI. Tailscale HTTPS must already be enabled; its hostname will appear in public certificate transparency logs.")
		}
		pin, err := certs.Provision(ctx, dir, n, name)
		if err != nil {
			return err
		}
		c.Fingerprint = pin
		if err = setup.SaveConfig(dir, c); err != nil {
			return err
		}
		fmt.Fprintf(out, "Configured %s at %s.\n", c.Name, c.Address)
		return finishSetup(ctx, c, dir, noStart, noPair, *environment, in, out, errOut)
	}
	c, err := setup.LoadConfig(dir)
	if err != nil {
		return err
	}
	if err := requireMatchingHomebrewInstallation(c); err != nil {
		return err
	}
	switch command {
	case "tailnet-login":
		if c.Network != "tsnet" {
			return errors.New("tailnet-login requires a saved --network tsnet configuration")
		}
		if _, err := setup.ReadPrivate(filepath.Join(dir, "tsnet", "tailscaled.state")); err != nil {
			return errors.New("embedded node enrollment is missing or unsafe; preserve this directory and configure a fresh companion")
		}
		fmt.Fprintln(errOut, "Stop this companion before logging in; restart it with trill start afterwards. The embedded node must retain its saved tailnet identity.")
		login := enrollTSNet
		if environment != nil && environment.enrollTSNet != nil {
			login = environment.enrollTSNet
		}
		n, err := login(ctx, dir, c.TSNetHostname, errOut)
		if err != nil {
			return err
		}
		u, _ := url.Parse(c.Address)
		if n.IP != c.IP || n.Host != u.Hostname() {
			return errors.New("embedded tailnet identity changed; configure a fresh state directory and pair again")
		}
		fmt.Fprintln(out, "Embedded Tailscale login complete. Start the companion to reconnect paired phones.")
		return nil
	case "providers":
		if backends == "" && (herdrPath != "" || herdrSessions != "" || herdrConfigHome != "") {
			return errors.New("changing providers requires --backends")
		}
		if backends != "" {
			if err = agents.Providers(ctx, c, dir, backends, herdrPath, herdrSessions, herdrConfigHome); err != nil {
				return err
			}
			c, err = setup.LoadConfig(dir)
			if err != nil {
				return err
			}
		}
		if c.Backends == "" {
			if c.TmuxSocket != "" {
				c.Backends = "terminal-lab"
			} else {
				c.Backends = "xirp"
			}
		}

		b, err := json.MarshalIndent(struct {
			Backends        string `json:"backends"`
			HerdrPath       string `json:"herdrPath,omitempty"`
			HerdrSessions   string `json:"herdrSessions,omitempty"`
			HerdrConfigHome string `json:"herdrConfigHome,omitempty"`
		}{c.Backends, c.HerdrPath, c.HerdrSessions, c.HerdrConfigHome}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
		return nil
	case "control":
		if c.AllowControl != enable {
			fmt.Fprintln(errOut, "Changing control disconnects Trill transport and active mosh leases; underlying sessions remain alive. Only the owned companion daemon is restarted if loaded.")
		}
		if err = agents.Control(ctx, c, dir, enable); err != nil {
			return err
		}
		fmt.Fprintf(out, "Control enabled: %t. Configuration saved. Use install/start with this --state-dir if the companion is not installed/running.\n", enable)
		return nil
	case "pair":
		return pair(ctx, c, dir, png, open, rawJSON, pasteURL, yes, in, out, errOut)
	case "devices":
		devices, err := pairing.NewStore(filepath.Join(dir, "devices.json")).Devices()
		if err != nil {
			return err
		}
		if rawJSON {
			if devices == nil {
				devices = []pairing.Device{}
			}
			return json.NewEncoder(out).Encode(devices)
		}
		return printDevices(out, devices)
	case "revoke":
		if err = pairing.NewStore(filepath.Join(dir, "devices.json")).Revoke(id); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Revoked %s.\n", id)
		return err
	case "status":
		fmt.Fprintf(out, "%s\nAddress: %s\nNetwork: %s\n", c.Name, c.Address, c.Network)
		if c.BindIP != "" {
			fmt.Fprintf(out, "Listening: %s (tailnet %s)\n", c.BindIP, c.IP)
		}
		if c.TailscaleSocket != "" {
			fmt.Fprintf(out, "Tailscale socket: %s\n", c.TailscaleSocket)
		}
		fmt.Fprintf(out, "Read-only: %t\nState: %s\n", !c.AllowControl, dir)
		expiry, tlsErr := certs.Check(c)
		if tlsErr != nil {
			fmt.Fprintf(out, "TLS: invalid (%v)\n", tlsErr)
		} else {
			fmt.Fprintf(out, "TLS expires: %s\n", expiry.UTC().Format(time.RFC3339))
		}
		status, agentErr := agents.Status(ctx, c, dir)
		fmt.Fprintln(out, "LaunchAgent:", status)
		if agentErr != nil {
			fmt.Fprintln(out, "LaunchAgent detail:", agentErr)
		}
		if tlsErr != nil {
			return tlsErr
		}
		return agentErr
	case "install":
		return agents.Install(c, dir)
	case "start":
		if _, err = certs.Check(c); err != nil && c.Network != "tsnet" {
			return err
		}
		return agents.Start(ctx, c, dir)
	case "stop":
		return agents.Stop(ctx, c, dir)
	case "uninstall":
		return agents.Uninstall(ctx, c, dir)
	case "renew":
		changed, err := certs.Renew(ctx, dir, c)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintln(out, "Renewed certificate; daemon loads it on new TLS handshakes.")
		}
		return nil
	}
	return nil
}

func requireMatchingHomebrewInstallation(config setup.Config) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	prefix := distribution.HomebrewPrefix(executable)
	if prefix != "" && (config.CLI != filepath.Join(prefix, "bin", "trill") || config.Daemon != filepath.Join(prefix, "bin", "trilld")) {
		return fmt.Errorf("saved configuration belongs to another installation; use %s with the same --state-dir or explicitly migrate it before using Homebrew", config.CLI)
	}
	return nil
}

// Replace the CLI process rather than spawning a child: the foreground daemon
// keeps the caller's PID, environment, standard streams, signals and exit status.
func serve(args []string, executable func() (string, error), execProcess func(string, []string, []string) error) error {
	cli, err := executable()
	if err != nil {
		return fmt.Errorf("locate trill executable: %w", err)
	}
	cli, err = filepath.EvalSymlinks(cli)
	if err != nil {
		return fmt.Errorf("resolve trill executable: %w", err)
	}
	daemon := filepath.Join(filepath.Dir(cli), "trilld")
	// argv[0] also gives the daemon's flag package the public command name.
	argv := append([]string{"trill serve"}, args...)
	if err := execProcess(daemon, argv, os.Environ()); err != nil {
		return fmt.Errorf("execute companion %s (rerun scripts/setup-companion.sh --help to rebuild): %w", daemon, err)
	}
	return nil
}

func probe(ctx context.Context, c setup.Config) error {
	config := companionTLSConfig(c)
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: config}
	conn, err := d.DialContext(ctx, "tcp", companionDialAddress(c))
	if err != nil {
		return fmt.Errorf("cannot reach companion HTTPS at %s: %w; check Bonjour/Tailscale connectivity, TCP 7443, and the saved certificate", c.Address, err)
	}
	return conn.Close()
}

func companionTLSConfig(c setup.Config) *tls.Config {
	u, _ := url.Parse(c.Address)
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if c.Fingerprint != "" {
		config.InsecureSkipVerify = true // exact SHA-256 identity verification replaces CA verification for LAN
		config.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing companion certificate")
			}
			leaf := cs.PeerCertificates[0]
			sum := sha256.Sum256(leaf.Raw)
			if hex.EncodeToString(sum[:]) != c.Fingerprint {
				return errors.New("companion fingerprint mismatch")
			}
			now := time.Now()
			if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
				return errors.New("companion certificate expired")
			}
			return leaf.VerifyHostname(u.Hostname())
		}
	}
	return config
}
func pair(ctx context.Context, c setup.Config, dir, png string, open, rawJSON, pasteURL, yes bool, in io.Reader, out, errOut io.Writer) error {
	return approvePair(ctx, c, dir, png, open, rawJSON, pasteURL, yes, in, out, errOut, probe)
}
func approvePair(ctx context.Context, c setup.Config, dir, png string, open, rawJSON, pasteURL, yes bool, in io.Reader, out, errOut io.Writer, probeHTTPS func(context.Context, setup.Config) error) error {
	if open && png == "" {
		return errors.New("--open requires --png")
	}
	formats := 0
	for _, selected := range []bool{png != "", rawJSON, pasteURL} {
		if selected {
			formats++
		}
	}
	if formats > 1 {
		return errors.New("choose only one of --png, --json or --url")
	}
	if png != "" {
		var err error
		png, err = filepath.Abs(png)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(png); err == nil {
			return errors.New("QR destination exists; choose a new file (will not overwrite)")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := setup.NewCertificates().Check(c); err != nil {
		return certificateHelp(c, dir, err)
	}
	fmt.Fprintf(errOut, "Authorize ONE phone for %s\n%s\nRead-only: %t\n", c.Name, c.Address, !c.AllowControl)
	if !yes {
		fmt.Fprint(errOut, "Type pair to create a single-use invitation valid for 10 minutes: ")
		answer, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && answer != "") {
			return fmt.Errorf("local approval required; run %s in a terminal, or add --yes to explicitly approve one invitation", companionCommand(c, dir, "pair"))
		}
		if strings.TrimSpace(answer) != "pair" {
			return errors.New("pairing cancelled; no invitation created")
		}
	}
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = probeHTTPS(ctx, c); err == nil {
			break
		}
		if attempt < 4 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("HTTPS verification failed; run %s and %s, then retry %s: %w", companionCommand(c, dir, "status"), companionCommand(c, dir, "start"), companionCommand(c, dir, "pair"), err)
	}
	code, expiry, err := pairing.NewStore(filepath.Join(dir, "devices.json")).CreateInvitation(10 * time.Minute)
	if err != nil {
		return err
	}
	payload, err := setup.PairPayload(c, code, expiry)
	if err != nil {
		return err
	}
	switch {
	case png != "":
		data, err := setup.QRPNG(payload)
		if err != nil {
			return err
		}
		if err = setup.WritePrivate(png, data, false); err != nil {
			return err
		}
		fmt.Fprintln(out, "Pairing QR:", png)
		if open {
			if _, err = (setup.ExecRunner{}).Run(ctx, "/usr/bin/open", png); err != nil {
				return errors.New("QR saved, but opening it failed")
			}
		}
	case rawJSON:
		_, err = fmt.Fprintln(out, string(payload))
	case pasteURL:
		_, err = fmt.Fprintln(out, setup.PairURL(payload))
	default:
		err = setup.PrintQR(out, payload)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(errOut, "In Trill on the phone, choose Pair a computer → Scan pairing code (or paste a pairing link), review the computer, and tap Pair. Invitation expires %s and can be used once. Keep this QR/link private.\n", expiry.Local().Format(time.RFC1123))
	if c.Network == "lan" {
		fmt.Fprintln(errOut, "Keep both devices on the same reachable LAN and allow Local Network access for Trill on the phone; guest Wi-Fi/client isolation can block Bonjour and HTTPS.")
	} else {
		fmt.Fprintln(errOut, "Keep Tailscale connected on both devices and permit phone access to this Mac on TCP 7443.")
	}
	fmt.Fprintf(errOut, "If the QR is hard to scan, run %s --png /path/to/new-pair.png --open, or use --url. For an expired/used invitation, run pair again.\n", companionCommand(c, dir, "pair"))
	return nil
}

// Pairing status describes access, not whether a device is currently connected.
func printDevices(out io.Writer, devices []pairing.Device) error {
	if len(devices) == 0 {
		_, err := fmt.Fprintln(out, "No paired devices. Run trill pair to connect your phone.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tSTATUS\tPAIRED (UTC)\tDEVICE ID")
	for _, device := range devices {
		status := "Paired"
		if device.RevokedAt != nil {
			status = "Revoked"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", device.Name, status, device.CreatedAt.UTC().Format("2006-01-02 15:04"), device.ID)
	}
	return w.Flush()
}
