package setup

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLaunchAgentXMLAndRenewalIsolation(t *testing.T) {
	dir := testDir(t)
	c := testConfig(dir)
	c.TmuxSocket = "/tmp/lab<&>.sock"
	for _, renew := range []bool{false, true} {
		b, err := LaunchAgent(c, dir, renew)
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(bytes.NewReader(b))
		var values []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid XML: %s: %v", b, err)
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
				var value string
				if err = decoder.DecodeElement(&value, &start); err != nil {
					t.Fatal(err)
				}
				values = append(values, value)
			}
		}
		if !strings.Contains(string(b), "&amp;") {
			t.Fatal("unescaped user text")
		}
		if renew {
			if !contains(values, c.CLI) || !contains(values, "renew") || contains(values, c.Daemon) || strings.Contains(string(b), "KeepAlive") {
				t.Fatalf("renewal can restart daemon: %s", b)
			}
			if !strings.Contains(string(b), "21600") {
				t.Fatal("missing renewal interval")
			}
		} else {
			for _, want := range []string{c.Daemon, c.TmuxSocket, c.Path, filepath.Join(dir, "devices.json")} {
				if !contains(values, want) {
					t.Fatalf("lost argument %q: %v", want, values)
				}
			}
			if contains(values, "--allow-control") {
				t.Fatal("enabled control implicitly")
			}
		}
	}
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func testAgents(t *testing.T) (Agents, *fakeRunner, string, Config) {
	t.Helper()
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	a := Agents{Runner: r, Home: t.TempDir(), UID: 501, ListenCheck: func(string) error { return nil }}
	if err := a.Install(c, dir); err != nil {
		t.Fatal(err)
	}
	return a, r, dir, c
}
func TestLaunchAgentOwnershipAndExclusiveInstall(t *testing.T) {
	a, r, dir, c := testAgents(t)
	if err := a.Install(c, dir); err != nil {
		t.Fatal("idempotent install:", err)
	}
	original, err := ReadPrivate(a.path(false))
	if err != nil {
		t.Fatal(err)
	}
	other := c
	other.AllowControl = true
	if err = a.Install(other, dir); err == nil {
		t.Fatal("overwrote different config")
	}
	if err = a.Stop(context.Background(), other, dir); err == nil {
		t.Fatal("stopped other configuration")
	}
	if err = a.Uninstall(context.Background(), other, dir); err == nil {
		t.Fatal("removed other configuration")
	}
	if len(r.calls) != 0 {
		t.Fatal("ownership failure invoked launchctl")
	}
	got, _ := ReadPrivate(a.path(false))
	if !bytes.Equal(original, got) {
		t.Fatal("modified existing plist")
	}
	// A foreign file/symlink is never overwritten, even at this stable label.
	if err = os.Remove(a.path(true)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(a.path(false), a.path(true)); err != nil {
		t.Fatal(err)
	}
	if err = a.Install(c, dir); err == nil {
		t.Fatal("followed plist symlink")
	}
}
func TestLaunchAgentRefusesLogSymlink(t *testing.T) {
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	a := Agents{Runner: &fakeRunner{}, Home: t.TempDir(), UID: 501}
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(dir, Label+".log")); err != nil {
		t.Fatal(err)
	}
	if err := a.Install(c, dir); err == nil {
		t.Fatal("accepted log symlink")
	}
	got, _ := os.ReadFile(foreign)
	if string(got) != "untouched" {
		t.Fatal("modified foreign log")
	}
	if _, err := os.Stat(a.path(false)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial install on preflight failure")
	}
}
func TestLaunchAgentStartCollisionAndScopedLifecycle(t *testing.T) {
	a, r, dir, c := testAgents(t)
	ctx := context.Background()
	a.ListenCheck = func(string) error { return errors.New("existing listener") }
	if err := a.Start(ctx, c, dir); err == nil {
		t.Fatal("ignored listener collision")
	}
	if len(r.calls) != 2 || !reflect.DeepEqual(r.calls[0].args, []string{"print", "gui/501/" + Label}) {
		t.Fatalf("collision mutated service: %+v", r.calls)
	}
	a.ListenCheck = func(string) error { return nil }
	r.calls = nil
	loaded := map[string]bool{}
	r.run = func(name string, args []string) ([]byte, error) {
		if name != "/bin/launchctl" {
			t.Fatalf("unexpected executable: %s", name)
		}
		switch args[0] {
		case "print":
			if loaded[args[1]] {
				return jobOutput(c, dir, strings.HasSuffix(args[1], RenewalLabel)), nil
			}
			return nil, errNotLoaded
		case "bootstrap":
			label := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
			loaded["gui/501/"+label] = true
			return nil, nil
		case "bootout":
			if !strings.HasPrefix(args[1], "gui/501/"+Label) {
				t.Fatalf("unowned bootout: %v", args)
			}
			delete(loaded, args[1])
			return nil, nil
		}
		t.Fatalf("unapproved command: %v", args)
		return nil, nil
	}
	if err := a.Start(ctx, c, dir); err != nil {
		t.Fatal(err)
	}
	before := len(r.calls)
	if err := a.Start(ctx, c, dir); err != nil {
		t.Fatal(err)
	}
	for _, op := range r.calls[before:] {
		if op.args[0] != "print" {
			t.Fatalf("start restarted service: %v", op)
		}
	}
	status, err := a.Status(ctx, c, dir)
	if err != nil || status != "running" {
		t.Fatalf("%s %v", status, err)
	}
	if err = a.Uninstall(ctx, c, dir); err != nil {
		t.Fatal(err)
	}
	for _, renew := range []bool{false, true} {
		if _, err = os.Stat(a.path(renew)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("plist left installed")
		}
	}
	if len(loaded) != 0 {
		t.Fatalf("loaded after uninstall: %v", loaded)
	}
	if _, err = os.Stat(filepath.Join(dir, Label+".log")); err != nil {
		t.Fatal("uninstall deleted private state/logs")
	}
}
