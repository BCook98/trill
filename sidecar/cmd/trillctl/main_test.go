package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar/pairing"
	"trill/sidecar/setup"
)

func fixture(t *testing.T) (setup.Config, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := setup.PrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	bundle, pin, err := setup.SelfSigned("192.168.1.2", "my-mac.local", "My Mac", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := setup.Config{Version: 1, Name: "My Mac", Network: "lan", IP: "192.168.1.2", Address: "https://my-mac.local:7443", Fingerprint: pin, Certificate: filepath.Join(dir, "tls.pem"), PrivateKey: filepath.Join(dir, "tls.pem"), Daemon: "/opt/trill/trilld", CLI: "/opt/trill/trillctl", Path: "/usr/bin:/bin"}
	if err = setup.WritePrivate(c.Certificate, bundle, false); err != nil {
		t.Fatal(err)
	}
	if err = setup.SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	return c, dir
}
func TestHelpAndArgumentErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"setup", "--help"}, {"pair", "--help"}, {"devices", "--help"}, {"revoke", "--help"}, {"status", "--help"}, {"start", "--help"}, {"stop", "--help"}, {"install", "--help"}, {"uninstall", "--help"}, {"renew", "--help"}, {"control", "--help"}} {
		var out, errOut bytes.Buffer
		if err := run(args, strings.NewReader(""), &out, &errOut); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.Len()+errOut.Len() == 0 {
			t.Fatalf("missing help: %v", args)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"revoke"}, {"pair", "extra"}, {"setup", "--bogus"}} {
		if err := run(args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestPairLocalApprovalAndSingleUseStore(t *testing.T) {
	c, dir := fixture(t)
	var out, errOut bytes.Buffer
	probes := 0
	probe := func(context.Context, setup.Config) error { probes++; return nil }
	err := approvePair(context.Background(), c, dir, "", false, true, false, false, strings.NewReader("cancel\n"), &out, &errOut, probe)
	if err == nil || probes != 0 {
		t.Fatal("created invitation before approval")
	}
	if _, err = os.Stat(filepath.Join(dir, "devices.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled pairing modified auth state")
	}
	out.Reset()
	errOut.Reset()
	before := time.Now()
	err = approvePair(context.Background(), c, dir, "", false, true, false, false, strings.NewReader("pair\n"), &out, &errOut, probe)
	if err != nil || probes != 1 {
		t.Fatalf("pair: %v", err)
	}
	var payload setup.Payload
	if err = json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Address != c.Address || payload.Fingerprint != c.Fingerprint || len(payload.Code) != 64 {
		t.Fatalf("invalid payload: %+v", payload)
	}
	expiry, err := time.Parse(time.RFC3339, payload.ExpiresAt)
	if err != nil || expiry.Sub(before) < 9*time.Minute || expiry.Sub(before) > 10*time.Minute+time.Second {
		t.Fatal("wrong invitation lifetime")
	}
	if strings.Contains(errOut.String(), payload.Code) {
		t.Fatal("invitation logged to diagnostic output")
	}
	store := pairing.NewStore(filepath.Join(dir, "devices.json"))
	device, token, err := store.Redeem(payload.Code, "My phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Redeem(payload.Code, "Other phone"); err == nil {
		t.Fatal("invitation reusable")
	}
	if strings.Contains(out.String()+errOut.String(), token) {
		t.Fatal("permanent bearer printed")
	}
	out.Reset()
	errOut.Reset()
	if err = run([]string{"devices", "--state-dir", dir}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), device.ID) || strings.Contains(out.String(), token) {
		t.Fatal("unsafe devices output")
	}
	out.Reset()
	if err = run([]string{"revoke", device.ID, "--state-dir", dir}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if _, err = pairing.NewStore(filepath.Join(dir, "devices.json")).Authenticate(token); err == nil {
		t.Fatal("revocation not persistent")
	}
}
func TestPairPNGNeverOverwritesAndFormatsAreExclusive(t *testing.T) {
	c, dir := fixture(t)
	png := filepath.Join(dir, "pair.png")
	if err := os.WriteFile(png, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := func(context.Context, setup.Config) error {
		t.Fatal("probed before rejecting invalid output")
		return nil
	}
	for _, tc := range []struct {
		png            string
		open, raw, url bool
	}{{png, false, false, false}, {"", true, false, false}, {"", false, true, true}} {
		if err := approvePair(context.Background(), c, dir, tc.png, tc.open, tc.raw, tc.url, true, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, probe); err == nil {
			t.Fatal("accepted invalid format")
		}
	}
	got, _ := os.ReadFile(png)
	if string(got) != "untouched" {
		t.Fatal("overwrote QR destination")
	}
}
func TestSetupDoesNotReplaceExistingConfiguration(t *testing.T) {
	_, dir := fixture(t)
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := run([]string{"setup", "--state-dir", dir, "--allow-control"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("existing config silently modified")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("config changed")
	}
}

func TestControlCommandRequiresExplicitChoice(t *testing.T) {
	_, dir := fixture(t)
	var out, errOut bytes.Buffer
	agentHome := t.TempDir()
	userHome := func() (string, error) { return agentHome, nil }
	for _, flags := range [][]string{{}, {"--enable", "--disable"}} {
		args := append([]string{"control", "--state-dir", dir}, flags...)
		if err := runWithHome(args, strings.NewReader(""), &out, &errOut, userHome); err == nil {
			t.Fatal("implicit/ambiguous permission change accepted")
		}
	}
	for _, choice := range []string{"--enable", "--disable"} {
		if err := runWithHome([]string{"control", "--state-dir", dir, choice}, strings.NewReader(""), &out, &errOut, userHome); err != nil {
			t.Fatal(err)
		}
		c, err := setup.LoadConfig(dir)
		if err != nil || c.AllowControl != (choice == "--enable") {
			t.Fatalf("control not saved: %+v %v", c, err)
		}
	}
	if !strings.Contains(errOut.String(), "underlying sessions remain alive") {
		t.Fatal("missing transport impact explanation")
	}
}

func TestProvidersCommandPersistsSelectionWithoutRePairing(t *testing.T) {
	_, dir := fixture(t)
	home := t.TempDir()
	var out, errOut bytes.Buffer
	getHome := func() (string, error) { return home, nil }
	if err := runWithHome([]string{"providers", "--state-dir", dir}, strings.NewReader(""), &out, &errOut, getHome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"backends": "xirp"`) {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runWithHome([]string{"providers", "--state-dir", dir, "--backends", "xirp,herdr", "--herdr-path", "/tmp/herdr", "--herdr-sessions", "default,work"}, strings.NewReader(""), &out, &errOut, getHome); err != nil {
		t.Fatal(err)
	}
	next, err := setup.LoadConfig(dir)
	if err != nil || next.Backends != "xirp,herdr" || next.HerdrSessions != "default,work" {
		t.Fatal(next, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); !os.IsNotExist(err) {
		t.Fatal("provider selection changed pairing")
	}
}
