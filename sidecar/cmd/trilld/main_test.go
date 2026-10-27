package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkGuards(t *testing.T) {
	for _, test := range []struct {
		listen, cert, key string
		insecure, valid   bool
	}{
		{"127.0.0.1:7443", "", "", true, true}, {"127.0.0.1:7443", "cert", "key", false, true}, {"0.0.0.0:7443", "cert", "key", false, true}, {"0.0.0.0:7443", "", "", true, false}, {"127.0.0.1:7443", "", "", false, false}, {"localhost:7443", "cert", "key", false, false}, {"127.0.0.1:7443", "cert", "", false, false},
	} {
		if (validateNetwork(test.listen, test.cert, test.key, test.insecure) == nil) != test.valid {
			t.Fatal(test)
		}
	}
}
func TestTokenFileSecurity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private", "token")
	token, err := loadToken(path)
	if err != nil || len(token) != 96 {
		t.Fatalf("create token: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token mode: %v %v", info, err)
	}
	again, err := loadToken(path)
	if err != nil || again != token {
		t.Fatalf("token changed on reload: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(path); err == nil {
		t.Fatal("accepted world-readable token")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(link); err == nil {
		t.Fatal("accepted token symlink")
	}
}

func TestPairingAuthenticationConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultToken := filepath.Join(home, ".config", "trill", "token")
	statePath := filepath.Join(home, "custom", "devices.json")
	token, store, err := loadAuthentication("", statePath)
	if err != nil || token != "" || store == nil {
		t.Fatal("pairing-only configuration failed", err)
	}
	if _, err := os.Stat(defaultToken); !os.IsNotExist(err) {
		t.Fatal("pairing-only mode created a master token")
	}
	// Even an existing malformed legacy token is never read in pairing-only mode.
	if err := os.MkdirAll(filepath.Dir(defaultToken), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultToken, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, _, err := loadAuthentication("", statePath); err != nil || token != "" {
		t.Fatal("pairing consulted legacy token")
	}
	explicit := filepath.Join(home, "explicit-token")
	token, store, err = loadAuthentication(explicit, statePath)
	if err != nil || len(token) != 96 || store == nil {
		t.Fatal("explicit dual authentication failed", err)
	}
	if err := os.Remove(defaultToken); err != nil {
		t.Fatal(err)
	}
	token, store, err = loadAuthentication("", "")
	if err != nil || len(token) != 96 || store != nil {
		t.Fatal("legacy default changed", err)
	}
	again, err := loadToken(defaultToken)
	if err != nil || again != token {
		t.Fatal("legacy default token not used")
	}
	if _, _, err := loadAuthentication("", "relative.json"); err == nil {
		t.Fatal("invalid pairing path accepted")
	}
}

func TestEmbeddedDaemonRequiresExistingEnrollment(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing-enrollment")
	_, err := openTSNet(context.Background(), directory, "trill", "100.73.54.69", "trill.example.ts.net")
	if err == nil || !strings.Contains(err.Error(), "run trill setup") {
		t.Fatalf("daemon attempted implicit enrollment: %v", err)
	}
}
