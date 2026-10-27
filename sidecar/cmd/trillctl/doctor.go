package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trill/sidecar/buildinfo"
	"trill/sidecar/distribution"
	"trill/sidecar/setup"
)

type doctorFinding struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

func doctor(args []string, output, errorOutput io.Writer, home string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	stateDir := flags.String("state-dir", filepath.Join(home, ".config", "trill"), "companion state directory")
	rawJSON := flags.Bool("json", false, "print structured diagnostics; no credentials")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
	}
	absoluteStateDir, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	findings := []doctorFinding{{"version", "ok", fmt.Sprintf("%s (%s), %s/%s", buildinfo.Current().Version, buildinfo.Current().Commit, buildinfo.Current().OS, buildinfo.Current().Architecture), ""}}
	homebrewInstallation := false
	if executable, err := os.Executable(); err == nil && distribution.HomebrewPrefix(executable) != "" {
		homebrewInstallation = true
		findings = append(findings, doctorFinding{"installation", "ok", "Managed by Homebrew; update with brew upgrade --cask bcook98/tap/trill", ""})
	}
	add := func(check string, failure error, detail, remedy string) {
		if failure != nil {
			findings = append(findings, doctorFinding{check, "fail", failure.Error(), remedy})
		} else {
			findings = append(findings, doctorFinding{check, "ok", detail, ""})
		}
	}
	if buildinfo.Version == "dev" {
		findings = append(findings, doctorFinding{"distribution", "warn", "Development/source build; Developer ID and notarization are not expected", "Update this installation with scripts/setup-companion.sh."})
	} else if _, err := distribution.SigningTeam(); err != nil {
		findings = append(findings, doctorFinding{"distribution", "warn", "Developer ID distribution is not configured in this build", "Use the documented source installer until signed releases are available."})
	} else if executable, err := os.Executable(); err != nil {
		add("signature", err, "", "Reinstall an official companion release.")
	} else {
		add("signature", distribution.VerifyExecutable(ctx, executable, distribution.CLIIdentifier), "CLI Developer ID signature verified", "Reinstall an official companion release.")
	}
	c, configErr := setup.LoadConfig(absoluteStateDir)
	add("configuration", configErr, "Private configuration and permissions are valid", "Run trill setup with the intended --state-dir; preserve existing state.")
	if configErr == nil {
		add("installation-ownership", requireMatchingHomebrewInstallation(c), "Configuration matches this installation", "Use the CLI recorded in the saved configuration or explicitly migrate before using Homebrew.")
		expiry, certificateErr := setup.NewCertificates().Check(c)
		add("certificate", certificateErr, "Certificate identity, hostname and expiry are valid", "Run trill status, then trill renew for a managed Tailscale certificate; preserve LAN identity.")
		if certificateErr == nil && time.Until(expiry) < 7*24*time.Hour {
			findings = append(findings, doctorFinding{"certificate-expiry", "warn", "Certificate expires within seven days", "Check the renewal LaunchAgent and run trill renew for Tailscale. Preserve the pinned LAN identity."})
		}
		if err := findDependency("trill", os.Getenv("PATH")); err != nil {
			findings = append(findings, doctorFinding{"path", "warn", "trill is missing from the current shell PATH", `Add ~/.local/bin to PATH: export PATH="$HOME/.local/bin:$PATH"`})
		}
		agents := setup.Agents{Runner: setup.ExecRunner{}, Home: home, UID: os.Getuid()}
		for _, finding := range agents.Diagnostics(ctx, c, absoluteStateDir) {
			add(finding.Label, finding.Err, finding.Status, "Run trill status with the same --state-dir; resolve ownership errors before trill install/start.")
		}
		add("cli", setup.Executable(c.CLI), "Configured CLI is executable", "Restore the configured executable; do not reset pairing state.")
		add("daemon", setup.Executable(c.Daemon), "Configured daemon is executable", "Restore the configured executable; do not reset pairing state.")
		if _, err := distribution.SigningTeam(); err == nil && buildinfo.Version != "dev" {
			add("daemon-signature", distribution.VerifyExecutable(ctx, c.Daemon, distribution.DaemonIdentifier), "Daemon Developer ID signature verified", "Reinstall an official companion release.")
		}
		add("https", probe(ctx, c), "Local TLS connection and companion identity verified", "Check trill status, hostname resolution and TCP 7443. Start the companion if stopped.")
		version, commit, runningErr := runningCompanion(ctx, c)
		if runningErr == nil && (version != buildinfo.Current().Version || commit != buildinfo.Current().Commit) {
			runningErr = errors.New("running daemon differs from this CLI build")
		}
		versionRemedy := "Use trill update for a signed installation; source builds may need an owned service stop/start after rebuilding."
		if homebrewInstallation {
			versionRemedy = "Run brew upgrade --cask bcook98/tap/trill, then trill stop and trill start with the same --state-dir to load the new daemon."
		}
		add("running-version", runningErr, "Running daemon and CLI versions match", versionRemedy)
		if _, err := os.Lstat(filepath.Join(absoluteStateDir, ".binary-update.json")); err == nil {
			add("update-recovery", errors.New("a binary update was interrupted"), "", "Run trill update with the same --state-dir to restore the previous release and services.")
		}
		dependencies := []string{"mosh-server"}
		backends := c.Backends
		if backends == "" && c.TmuxSocket == "" {
			backends = "xirp"
		}
		if strings.Contains(backends, "xirp") {
			dependencies = append(dependencies, "xirp", "tmux")
		}
		if c.TmuxSocket != "" {
			dependencies = append(dependencies, "tmux")
		}
		if strings.Contains(backends, "herdr") {
			dependencies = append(dependencies, c.HerdrPath)
		}
		for _, dependency := range dependencies {
			if dependency == "" {
				dependency = "herdr"
			}
			add("dependency:"+filepath.Base(dependency), findDependency(dependency, c.Path), "Available on the saved service PATH", "Install the dependency and ensure its directory is on the companion's saved PATH.")
		}
		findings = append(findings, doctorFinding{"phone-connectivity", "warn", "Local diagnostics cannot prove phone-to-Mac HTTPS, Bonjour or Mosh UDP reachability, or provider authentication", "Check the connection from Trill on the phone; allow TCP 7443 and UDP 60000–60010."})
	}
	failed := false
	for _, finding := range findings {
		failed = failed || finding.Status == "fail"
	}
	if *rawJSON {
		err = json.NewEncoder(output).Encode(findings)
	} else {
		for _, finding := range findings {
			if _, err = fmt.Fprintf(output, "%s %s: %s\n", strings.ToUpper(finding.Status), finding.Check, finding.Detail); err != nil {
				return err
			}
			if finding.Remedy != "" {
				if _, err = fmt.Fprintf(output, "  %s\n", finding.Remedy); err != nil {
					return err
				}
			}
		}
	}
	if err != nil {
		return err
	}
	if failed {
		return errors.New("doctor found problems; see diagnostics above")
	}
	return nil
}

func runningCompanion(ctx context.Context, config setup.Config) (string, string, error) {
	transport := &http.Transport{TLSClientConfig: companionTLSConfig(config), Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, companionDialAddress(config))
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected companion redirect") }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.Address+"/v1/health", nil)
	if err != nil {
		return "", "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", errors.New("cannot read running companion version over verified HTTPS")
	}
	defer response.Body.Close()
	version, commit := response.Header.Get("X-Trill-Version"), response.Header.Get("X-Trill-Commit")
	if response.StatusCode != http.StatusUnauthorized || version == "" || commit == "" {
		return "", "", errors.New("running companion does not expose build identity; it may need updating")
	}
	return version, commit, nil
}

func companionDialAddress(config setup.Config) string {
	address, _ := url.Parse(config.Address)
	if config.BindIP != "" {
		return net.JoinHostPort(config.BindIP, address.Port())
	}
	return address.Host
}

func findDependency(name, savedPath string) error {
	if filepath.IsAbs(name) {
		return setup.Executable(name)
	}
	for _, directory := range filepath.SplitList(savedPath) {
		if filepath.IsAbs(directory) && setup.Executable(filepath.Join(directory, name)) == nil {
			return nil
		}
	}
	return fmt.Errorf("%s is missing from the saved companion PATH", name)
}
