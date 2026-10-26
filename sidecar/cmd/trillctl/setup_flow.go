package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"trill/sidecar/providers"
	"trill/sidecar/setup"
)

// Only setup's external effects are injected. Tests exercise the public argument
// parser and real private-state/ownership guards without contacting user services.
type setupServices interface {
	Install(setup.Config, string) error
	Start(context.Context, setup.Config, string) error
}

type setupEnvironment struct {
	agents        setupServices
	certs         setup.Certificates
	discovery     setup.Discovery
	executable    func() (string, error)
	checkListener func(string) error
	probe         func(context.Context, setup.Config) error
}

// Defaults do not express an intent to reconfigure. Check only flags the caller
// actually supplied, including explicit false/empty values. Saved state remains
// authoritative; setup never invokes Control, Providers, Renew, Stop or Uninstall.
func compatibleSetupFlags(flags *flag.FlagSet, c setup.Config, dir string) error {
	u, _ := url.Parse(c.Address) // LoadConfig has validated the URL.
	selection, err := providers.Normalize(c.Backends, c.HerdrPath, c.HerdrSessions, c.TmuxSocket != "")
	if err != nil {
		return err
	}
	// Omitted provider flags inherit saved values. Explicit empty values still
	// request provider defaults, so normalize both sides before comparing.
	requested := selection
	var providerFlags []string
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "backends":
			requested.Backends = f.Value.String()
		case "herdr-path":
			requested.HerdrPath = f.Value.String()
		case "herdr-sessions":
			requested.HerdrSessions = f.Value.String()
		default:
			return
		}
		providerFlags = append(providerFlags, "--"+f.Name)
	})
	requested, err = providers.Normalize(requested.Backends, requested.HerdrPath, requested.HerdrSessions, c.TmuxSocket != "")
	if err != nil {
		return fmt.Errorf("saved configuration conflicts with %s; no settings changed. Rerun setup without those flags to resume; use %s --help to change providers: %w", strings.Join(providerFlags, ", "), companionCommand(c, dir, "providers"), err)
	}
	providerValues := map[string]string{"backends": requested.Backends, "herdr-path": requested.HerdrPath, "herdr-sessions": requested.HerdrSessions}
	want := map[string]string{
		"network": c.Network, "ip": c.IP, "hostname": u.Hostname(), "name": c.Name,
		"daemon": c.Daemon, "allow-control": strconv.FormatBool(c.AllowControl),
		"tmux-socket": c.TmuxSocket, "backends": selection.Backends,
		"herdr-path": selection.HerdrPath, "herdr-sessions": selection.HerdrSessions,
		"herdr-config-home": c.HerdrConfigHome,
	}
	var conflicts []string
	var hints []string
	flags.Visit(func(f *flag.Flag) {
		saved, checked := want[f.Name]
		if !checked {
			return // --state-dir, --no-start and --no-pair are run options.
		}
		value := f.Value.String()
		if normalized, ok := providerValues[f.Name]; ok {
			value = normalized
		}
		switch f.Name {
		case "network":
			if value == "auto" {
				return // Automatic discovery applies only to a new configuration.
			}
		case "ip":
			if net.ParseIP(value).Equal(net.ParseIP(saved)) {
				return
			}
		case "hostname":
			normalized, err := setup.NormalizeLANHostname(value)
			if c.Network == "lan" && err == nil && normalized == saved {
				return
			}
			// A LAN hostname override has no meaning for saved Tailscale state.
			conflicts = append(conflicts, "--"+f.Name)
			return
		case "herdr-config-home":
			if value != "" || saved != "" {
				if normalized, err := providers.HerdrHome(value); err == nil {
					value = normalized
				}
			}
		case "daemon":
			if absolute, err := filepath.Abs(value); value != "" && err == nil {
				value = absolute
			}
		}
		if value != saved {
			conflicts = append(conflicts, "--"+f.Name)
			if f.Name == "allow-control" {
				choice := "--enable"
				if value == "false" {
					choice = "--disable"
				}
				hints = append(hints, "To change control explicitly, run "+companionCommand(c, dir, "control")+" "+choice+" (disconnects Trill transport; underlying sessions stay alive).")
			}
			if f.Name == "backends" || strings.HasPrefix(f.Name, "herdr-") {
				hints = append(hints, "To change providers, use "+companionCommand(c, dir, "providers")+" --help.")
			}
		}
	})
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("saved configuration conflicts with %s; no settings changed. Rerun setup without those flags to resume. %s Network/name/executable changes require stopping and uninstalling the old owned configuration, then setup in a fresh --state-dir and re-pairing", strings.Join(conflicts, ", "), strings.Join(hints, " "))
}

func finishSetup(ctx context.Context, c setup.Config, dir string, noStart, noPair bool, env setupEnvironment, in io.Reader, out, errOut io.Writer) error {
	if _, err := env.certs.Check(c); err != nil {
		return certificateHelp(c, dir, err)
	}
	if c.AllowControl {
		fmt.Fprintln(out, "Access: control enabled (saved explicit choice).")
	} else {
		fmt.Fprintln(out, "Access: read-only. Terminal access and session changes require an explicit control choice.")
		fmt.Fprintf(out, "To enable later: %s --enable (disconnects Trill transport; underlying sessions stay alive).\n", companionCommand(c, dir, "control"))
	}
	if c.TmuxSocket != "" {
		fmt.Fprintf(out, "Terminal lab uses only the explicitly supplied socket: %s\n", c.TmuxSocket)
	}
	if noStart {
		fmt.Fprintf(out, "Services left unchanged. When ready, run %s to install/start and pair using this saved configuration.\n", companionCommand(c, dir, "setup"))
		return nil
	}
	if err := env.agents.Install(c, dir); err != nil {
		return fmt.Errorf("configuration preserved; could not install owned LaunchAgents: %w. Inspect %s; resolve permissions/ownership, then rerun %s. For an interrupted control/provider change, use %s", err, companionCommand(c, dir, "status"), companionCommand(c, dir, "setup"), companionCommand(c, dir, "start"))
	}
	if err := env.agents.Start(ctx, c, dir); err != nil {
		return fmt.Errorf("configuration preserved; could not start companion: %w. Inspect %s and private log %s; resolve the cause, then rerun %s", err, companionCommand(c, dir, "status"), shellQuote(filepath.Join(dir, setup.Label+".log")), companionCommand(c, dir, "setup"))
	}
	fmt.Fprintln(out, "Companion services are loaded. Existing connections remain open.")
	if noPair {
		fmt.Fprintf(out, "Already paired phones can keep using their saved connection. To authorize another phone: %s\n", companionCommand(c, dir, "pair"))
		return nil
	}
	if err := approvePair(ctx, c, dir, "", false, false, false, false, in, out, errOut, env.probe); err != nil {
		return fmt.Errorf("pairing not completed; configuration and services left in place. Authorize a phone later with %s: %w", companionCommand(c, dir, "pair"), err)
	}
	return nil
}

func certificateHelp(c setup.Config, dir string, err error) error {
	if c.Network == "tailscale" {
		return fmt.Errorf("saved HTTPS certificate is unusable: %w. Check the Mac clock and Tailscale connection/HTTPS permissions, then run %s and retry setup; setup did not replace the certificate", err, companionCommand(c, dir, "renew"))
	}
	return fmt.Errorf("saved LAN certificate is unusable: %w. Check the Mac clock and restore the original private tls.pem if available. Setup will not rotate its pinned identity. To replace it, stop/uninstall the old owned configuration, use a fresh --state-dir and pair again; inspect %s", err, companionCommand(c, dir, "status"))
}

func companionCommand(c setup.Config, dir, command string) string {
	return shellQuote(c.CLI) + " " + command + " --state-dir " + shellQuote(dir)
}

// Recovery commands are pasted into a shell; Go's %q permits shell expansion.
func shellQuote(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-:") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
