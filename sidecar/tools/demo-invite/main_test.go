package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"trill/sidecar/pairing"
)

func TestDemoInvitationUsesRealSingleUsePairing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := `{"kind":"trill-demo-lab-v1","address":"https://localhost:19443","name":"Demo Mac","fingerprint":"` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "pair.url")
	if err := invite(dir, output); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("invitation not private")
	}
	raw, _ := os.ReadFile(output)
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "trill://pair?data="))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Code    string
		Address string
	}
	if err = json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	store := pairing.NewStore(filepath.Join(dir, "devices.json"))
	if _, _, err = store.Redeem(value.Code, "demo phone"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Redeem(value.Code, "second phone"); err == nil {
		t.Fatal("reused invitation")
	}
	if err = invite(dir, output); err == nil {
		t.Fatal("overwrote invitation file")
	}
	after, _ := os.ReadFile(output)
	if string(after) != string(raw) {
		t.Fatal("changed existing file")
	}
}
func TestDemoInvitationRejectsUnownedState(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	os.WriteFile(filepath.Join(dir, "owner.json"), []byte(`{"kind":"production"}`), 0600)
	if err := invite(dir, filepath.Join(dir, "pair.url")); err == nil {
		t.Fatal("accepted unowned state")
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); !os.IsNotExist(err) {
		t.Fatal("touched pairing store")
	}
}
