package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar/pairing"
)

func TestDevicesReadableAndJSON(t *testing.T) {
	_, dir := fixture(t)
	storePath := filepath.Join(dir, "devices.json")
	store := pairing.NewStore(storePath)
	var out, diagnostics bytes.Buffer
	list := func(jsonOutput bool) string {
		t.Helper()
		out.Reset()
		diagnostics.Reset()
		args := []string{"devices", "--state-dir", dir}
		if jsonOutput {
			args = append(args, "--json")
		}
		if err := run(args, strings.NewReader(""), &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if diagnostics.Len() != 0 {
			t.Fatalf("unexpected diagnostics: %s", &diagnostics)
		}
		return out.String()
	}
	if got := list(false); !strings.Contains(got, "No paired devices") {
		t.Fatal(got)
	}
	if got := list(true); got != "[]\n" {
		t.Fatal(got)
	}
	var devices []pairing.Device
	var secrets []string
	for _, name := range []string{"My iPhone Duo", "Old iPhone"} {
		code, _, err := store.CreateInvitation(time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		device, token, err := store.Redeem(code, name)
		if err != nil {
			t.Fatal(err)
		}
		devices = append(devices, device)
		secrets = append(secrets, token, code)
	}
	if err := store.Revoke(devices[1].ID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	readable := list(false)
	for _, value := range []string{"DEVICE", "STATUS", "PAIRED (UTC)", "DEVICE ID", "Paired", "Revoked", devices[0].Name, devices[1].Name, devices[0].ID, devices[1].ID, devices[0].CreatedAt.UTC().Format("2006-01-02 15:04")} {
		if !strings.Contains(readable, value) {
			t.Errorf("missing %q in %s", value, readable)
		}
	}
	raw := list(true)
	var decoded []pairing.Device
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0].ID != devices[0].ID || decoded[0].RevokedAt != nil || decoded[1].RevokedAt == nil {
		t.Fatalf("wrong device JSON: %+v", decoded)
	}
	for _, secret := range secrets {
		if strings.Contains(readable+raw, secret) {
			t.Fatal("device output exposed pairing credential material")
		}
	}
	if strings.Contains(readable+raw, "tokenHash") {
		t.Fatal("device output contains private credential fields")
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("listing devices changed pairing state")
	}
}
