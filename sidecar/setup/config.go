// Package setup implements local companion provisioning. It never runs a shell.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"trill/sidecar/providers"
)

const Port = "7443"
const Label = "app.trill.companion"
const RenewalLabel = Label + ".renew"

type Config struct {
	Backends        string `json:"backends,omitempty"`
	HerdrPath       string `json:"herdrPath,omitempty"`
	HerdrConfigHome string `json:"herdrConfigHome,omitempty"`
	HerdrSessions   string `json:"herdrSessions,omitempty"`
	Version         int    `json:"version"`
	Name            string `json:"name"`
	Network         string `json:"network"`
	IP              string `json:"ip"`
	BindIP          string `json:"bindIP,omitempty"`
	Address         string `json:"address"`
	Fingerprint     string `json:"fingerprint"`
	Certificate     string `json:"certificate"`
	PrivateKey      string `json:"privateKey"`
	TailscaleCLI    string `json:"tailscaleCLI,omitempty"`
	TailscaleSocket string `json:"tailscaleSocket,omitempty"`
	AllowControl    bool   `json:"allowControl"`
	TmuxSocket      string `json:"tmuxSocket,omitempty"`
	Daemon          string `json:"daemon"`
	CLI             string `json:"cli"`
	Path            string `json:"path"`
}

func DefaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "trill"), nil
}

// PrivateDir validates existing ancestors before creating private state. Resolve
// parent aliases (including macOS /tmp) so a symlink cannot hide an unsafe parent.
func PrivateDir(path string) error {
	if err := privateAncestors(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return checkPrivateDir(path)
}

func checkPrivateDir(path string) error {
	if err := privateAncestors(path); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode().Perm() != 0700 || !ok || owner.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%s must be a user-owned real directory with mode 0700", path)
	}
	return nil
}

func privateAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return errors.New("state directory must be an absolute, clean path below root")
	}
	// Find the nearest existing parent without creating anything in an unsafe tree.
	parent := filepath.Dir(path)
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent = filepath.Dir(parent)
	}
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	for {
		st, err := os.Stat(parent)
		if err != nil {
			return err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !st.IsDir() || !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Getuid())) || (st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("unsafe state ancestor %s: owner or write permissions permit replacement", parent)
		}
		if parent == "/" {
			return nil
		}
		parent = filepath.Dir(parent)
	}
}

func ReadPrivate(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 1<<20 {
		return nil, fmt.Errorf("%s must be a regular 0600 file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, actual) {
		return nil, errors.New("file changed while opening")
	}
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// WritePrivate creates exclusively by default. Replace is used only for the
// companion's own certificate bundle after validation, never arbitrary files.
func WritePrivate(path string, data []byte, replace bool) error {
	if replace {
		if _, err := ReadPrivate(path); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trill-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if replace {
		return os.Rename(tmp.Name(), path)
	}
	// Hard linking is atomic and fails if any destination already exists.
	return os.Link(tmp.Name(), path)
}

func (c Config) Validate(dir string) error {
	if c.HerdrConfigHome != "" {
		if !strings.Contains(","+c.Backends+",", ",herdr,") {
			return errors.New("Herdr config home requires a Herdr backend")
		}
		if _, err := providers.HerdrHome(c.HerdrConfigHome); err != nil {
			return err
		}
	}
	if _, err := providers.Normalize(c.Backends, c.HerdrPath, c.HerdrSessions, c.TmuxSocket != ""); err != nil {
		return err
	}
	ip := net.ParseIP(c.IP)
	if c.Version != 1 || c.Name == "" || len(c.Name) > 128 || !utf8.ValidString(c.Name) || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 || !usableIP(ip) {
		return errors.New("invalid companion configuration")
	}
	if c.Network != "lan" && c.Network != "tailscale" {
		return errors.New("invalid network")
	}
	u, err := url.Parse(c.Address)
	if err != nil || u.Scheme != "https" || u.Port() != Port || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid HTTPS address")
	}
	if c.Network == "lan" {
		host, hostErr := NormalizeLANHostname(u.Hostname())
		if hostErr != nil || host != u.Hostname() || isTailscale(ip) || len(c.Fingerprint) != 64 || strings.Trim(c.Fingerprint, "0123456789abcdef") != "" {
			return errors.New("invalid LAN pin or address")
		}
		if c.BindIP != "" || c.TailscaleSocket != "" {
			return errors.New("bind address and Tailscale socket are Tailscale-only")
		}
	} else if !isTailscale(ip) || !strings.HasSuffix(u.Hostname(), ".ts.net") || c.Fingerprint != "" || !filepath.IsAbs(c.TailscaleCLI) {
		return errors.New("invalid Tailscale HTTPS configuration")
	} else if c.BindIP != "" && c.BindIP != userspaceTailscaleBindIP {
		return errors.New("Tailscale bind address must be 127.0.0.1, the userspace forwarding target")
	} else if c.TailscaleSocket != "" && (!filepath.IsAbs(c.TailscaleSocket) || filepath.Clean(c.TailscaleSocket) != c.TailscaleSocket) {
		return errors.New("Tailscale socket must be an absolute clean path")
	}
	if c.Certificate != filepath.Join(dir, "tls.pem") || c.PrivateKey != c.Certificate {
		return errors.New("certificate must be the managed PEM bundle")
	}
	if !filepath.IsAbs(c.Daemon) || !filepath.IsAbs(c.CLI) || c.Path == "" || strings.ContainsRune(c.Path, 0) {
		return errors.New("absolute executable paths and PATH required")
	}
	if c.TmuxSocket != "" && (!filepath.IsAbs(c.TmuxSocket) || filepath.Clean(c.TmuxSocket) != c.TmuxSocket || filepath.Base(c.TmuxSocket) == "default") {
		return errors.New("lab tmux socket must be an absolute clean path to a dedicated socket, never the default socket")
	}
	return nil
}

func SaveConfig(dir string, c Config) error {
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	if err := c.Validate(dir); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(filepath.Join(dir, "config.json"), append(b, '\n'), false)
}
func LoadConfig(dir string) (Config, error) {
	var c Config
	if err := checkPrivateDir(dir); err != nil {
		return c, err
	}
	b, err := ReadPrivate(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, errors.New("invalid config JSON")
	}
	return c, c.Validate(dir)
}

// ListenIP is the local address the companion binds.
func (c Config) ListenIP() string {
	if c.BindIP != "" {
		return c.BindIP
	}
	return c.IP
}

func DaemonArgs(c Config, dir string) []string {
	args := []string{"--listen", net.JoinHostPort(c.ListenIP(), Port), "--cert", c.Certificate, "--key", c.PrivateKey, "--pairing-store", filepath.Join(dir, "devices.json"), "--mosh-host", c.IP}
	if c.BindIP != "" {
		args = append(args, "--mosh-bind", c.BindIP)
	}
	if c.AllowControl {
		args = append(args, "--allow-control")
	}
	if c.TmuxSocket != "" {
		args = append(args, "--tmux-socket", c.TmuxSocket)
	}
	if c.Backends != "" {
		args = append(args, "--backends", c.Backends)
	}
	if c.HerdrPath != "" {
		args = append(args, "--herdr-path", c.HerdrPath)
	}
	if c.HerdrConfigHome != "" {
		args = append(args, "--herdr-config-home", c.HerdrConfigHome)
	}
	if c.HerdrSessions != "" {
		args = append(args, "--herdr-sessions", c.HerdrSessions)
	}
	return args
}

// DependencyPath includes the invoking user's dependency directories as well as
// the standard macOS/Homebrew paths unavailable in a default launchd environment.
func DependencyPath(home, current string) string {
	parts := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	parts = append(parts, filepath.SplitList(current)...)
	seen := map[string]bool{}
	out := []string{}
	for _, p := range parts {
		if filepath.IsAbs(p) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}
