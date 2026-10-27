// Package providers validates shared daemon and managed-setup provider options.
package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type Selection struct{ Backends, HerdrPath, HerdrSessions string }

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func Normalize(backends, path, sessions string, lab bool) (Selection, error) {
	if lab {
		return Selection{}, fmt.Errorf("tmux support was removed; use a Herdr server")
	}
	if backends != "" && backends != "herdr" {
		return Selection{}, fmt.Errorf("Trill now supports Herdr only; configure --backends herdr")
	}
	backends = "herdr"
	if path == "" {
		path = "herdr"
	}
	if strings.HasPrefix(path, "-") || strings.IndexFunc(path, unicode.IsControl) >= 0 || (!filepath.IsAbs(path) && (filepath.Base(path) != path || strings.Contains(path, "\\"))) {
		return Selection{}, fmt.Errorf("Herdr path must be an absolute path or executable name")
	}
	if sessions == "" {
		sessions = "default"
	}
	names := map[string]bool{}
	for _, name := range strings.Split(sessions, ",") {
		if !serverName.MatchString(name) || names[name] {
			return Selection{}, fmt.Errorf("Herdr sessions must be unique names (letters, numbers, hyphens, underscores; at most 64 characters)")
		}
		names[name] = true
	}
	if len(names) > 16 {
		return Selection{}, fmt.Errorf("configure at most 16 Herdr servers")
	}
	return Selection{backends, path, sessions}, nil
}

// HerdrHome resolves XDG_CONFIG_HOME once, when configuring the companion. The
// resulting explicit base is persisted and applied to every Herdr subprocess.
func HerdrHome(value string) (string, error) {
	if value == "" {
		value = os.Getenv("XDG_CONFIG_HOME")
	}
	if value == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("Herdr config home must be an absolute directory containing herdr/")
	}
	return filepath.Clean(value), nil
}
