package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlConfigOnlyIsExplicitAndPersistent(t *testing.T) {
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	a := Agents{Runner: r, Home: t.TempDir(), UID: 501}
	if err := a.Control(context.Background(), c, dir, true); err != nil {
		t.Fatal(err)
	}
	next, err := LoadConfig(dir)
	if err != nil || !next.AllowControl {
		t.Fatal("did not persist explicit enable", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("config-only control invoked launchctl")
	}
	if err = a.Control(context.Background(), next, dir, false); err != nil {
		t.Fatal(err)
	}
	final, _ := LoadConfig(dir)
	if final.AllowControl {
		t.Fatal("disable did not persist")
	}
	if err = a.Control(context.Background(), next, dir, true); err == nil {
		t.Fatal("stale configuration overwrote current state")
	}
}
func TestControlRestartsOnlyOwnedDaemon(t *testing.T) {
	a, r, dir, c := testAgents(t)
	renewalBefore, _ := ReadPrivate(a.path(true))
	loaded := true
	r.run = func(name string, args []string) ([]byte, error) {
		if name != "/bin/launchctl" {
			t.Fatal(name)
		}
		switch args[0] {
		case "print":
			if strings.HasSuffix(args[1], RenewalLabel) || !loaded {
				return nil, errNotLoaded
			}
			return jobOutput(c, dir, false), nil
		case "bootout":
			if args[1] != "gui/501/"+Label {
				t.Fatalf("stopped renewal/foreign service: %v", args)
			}
			loaded = false
			return nil, nil
		case "bootstrap":
			if args[2] != a.path(false) {
				t.Fatalf("restarted renewal: %v", args)
			}
			return nil, nil
		}
		t.Fatal(args)
		return nil, nil
	}
	if err := a.Control(context.Background(), c, dir, true); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 5 {
		t.Fatalf("unexpected service operations: %+v", r.calls)
	}
	next, err := LoadConfig(dir)
	if err != nil || !next.AllowControl {
		t.Fatal(err)
	}
	if err = a.verify(next, dir, false); err != nil {
		t.Fatal("daemon plist and config diverged:", err)
	}
	renewalAfter, _ := ReadPrivate(a.path(true))
	if !bytes.Equal(renewalBefore, renewalAfter) {
		t.Fatal("altered renewal service")
	}
	count := len(r.calls)
	if err = a.Control(context.Background(), next, dir, true); err != nil || len(r.calls) != count {
		t.Fatal("no-op control restarted daemon")
	}
}
func TestControlFailureKeepsRecoverableState(t *testing.T) {
	a, r, dir, c := testAgents(t)
	loaded := true
	r.run = func(_ string, args []string) ([]byte, error) {
		if args[0] == "print" {
			if strings.HasSuffix(args[1], RenewalLabel) || !loaded {
				return nil, errNotLoaded
			}
			return jobOutput(c, dir, false), nil
		}
		if args[0] == "bootout" {
			return nil, errors.New("cannot stop")
		}
		return nil, nil
	}
	if err := a.Control(context.Background(), c, dir, true); err == nil {
		t.Fatal("ignored stop failure")
	}
	unchanged, _ := LoadConfig(dir)
	if unchanged.AllowControl {
		t.Fatal("stop failure changed permissions")
	}
	if err := a.verify(c, dir, false); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ string, args []string) ([]byte, error) {
		if args[0] == "print" {
			if strings.HasSuffix(args[1], RenewalLabel) || !loaded {
				return nil, errNotLoaded
			}
			return jobOutput(c, dir, false), nil
		}
		if args[0] == "bootout" {
			loaded = false
		}
		if args[0] == "bootstrap" {
			return nil, errors.New("cannot start")
		}
		return nil, nil
	}
	err := a.Control(context.Background(), c, dir, true)
	if err == nil || !strings.Contains(err.Error(), "configuration saved") || !strings.Contains(err.Error(), "--state-dir") {
		t.Fatalf("missing restart recovery: %v", err)
	}
	next, _ := LoadConfig(dir)
	if !next.AllowControl {
		t.Fatal("saved control change lost")
	}
	if err = a.verify(next, dir, false); err != nil {
		t.Fatal("retry cannot recognize installed configuration:", err)
	}
}
func TestControlRejectsForeignOrIncompleteInstallation(t *testing.T) {
	a, r, dir, c := testAgents(t)
	if err := os.Remove(a.path(true)); err != nil {
		t.Fatal(err)
	}
	if err := a.Control(context.Background(), c, dir, true); err == nil || !strings.Contains(err.Error(), "install --state-dir") {
		t.Fatalf("missing partial installation recovery: %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("modified incomplete installation")
	}
	if err := a.Install(c, dir); err != nil {
		t.Fatal(err)
	}
	path := a.path(false)
	if err := WritePrivate(path, []byte("foreign"), true); err != nil {
		t.Fatal(err)
	}
	if err := a.Control(context.Background(), c, dir, true); err == nil {
		t.Fatal("overwrote foreign LaunchAgent")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "foreign" {
		t.Fatal("changed foreign plist")
	}
	lock := filepath.Join(dir, ".control.lock")
	st, err := os.Stat(lock)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("unsafe lock file")
	}
}

func TestProviderSelectionPersistsWithoutChangingControlOrDevices(t *testing.T) {
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	devices := filepath.Join(dir, "devices.json")
	if err := WritePrivate(devices, []byte("private device state"), false); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	a := Agents{Runner: r, Home: t.TempDir(), UID: 501}
	if err := a.Providers(context.Background(), c, dir, "xirp,herdr", "/tmp/herdr", "default,work", ""); err != nil {
		t.Fatal(err)
	}
	next, err := LoadConfig(dir)
	if err != nil || next.Backends != "xirp,herdr" || next.HerdrSessions != "default,work" || next.AllowControl != c.AllowControl {
		t.Fatalf("configuration: %+v %v", next, err)
	}
	b, _ := ReadPrivate(devices)
	if string(b) != "private device state" || len(r.calls) != 0 {
		t.Fatal("provider selection touched devices or services")
	}
	if err = a.Providers(context.Background(), c, dir, "herdr", "/tmp/herdr", "work", ""); err == nil {
		t.Fatal("stale update accepted")
	}
	args := strings.Join(DaemonArgs(next, dir), " ")
	if !strings.Contains(args, "--backends xirp,herdr") || !strings.Contains(args, "--herdr-sessions default,work") {
		t.Fatal(args)
	}
}
func TestProviderSelectionRestartsOnlyTheOwnedCompanion(t *testing.T) {
	a, r, dir, c := testAgents(t)
	before, _ := ReadPrivate(a.path(true))
	loaded := true
	r.run = func(name string, args []string) ([]byte, error) {
		switch args[0] {
		case "print":
			if strings.HasSuffix(args[1], RenewalLabel) || !loaded {
				return nil, errNotLoaded
			}
			return jobOutput(c, dir, false), nil
		case "bootout":
			if args[1] != a.domain()+"/"+Label {
				t.Fatal("stopped another service")
			}
			loaded = false
			return nil, nil
		case "bootstrap":
			if args[2] != a.path(false) {
				t.Fatal("started another service")
			}
			return nil, nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	}
	if err := a.Providers(context.Background(), c, dir, "herdr", "/tmp/herdr", "default", ""); err != nil {
		t.Fatal(err)
	}
	next, _ := LoadConfig(dir)
	if err := a.verify(next, dir, false); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadPrivate(a.path(true))
	if !bytes.Equal(before, after) {
		t.Fatal("renewal agent changed")
	}
}

func TestStartRepairsAnInterruptedProviderCommit(t *testing.T) {
	a, r, dir, old := testAgents(t)
	next := old
	next.Backends = "xirp,herdr"
	next.HerdrPath = "/tmp/herdr"
	next.HerdrSessions = "default,work"
	previous, _ := json.Marshal(old)
	current, _ := json.Marshal(next)
	if err := WritePrivate(filepath.Join(dir, "config.previous.json"), previous, false); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(dir, "config.json"), current, true); err != nil {
		t.Fatal(err)
	}
	r.run = func(name string, args []string) ([]byte, error) {
		if args[0] == "print" {
			return nil, errNotLoaded
		}
		return nil, nil
	}
	if err := a.Start(context.Background(), next, dir); err != nil {
		t.Fatal(err)
	}
	if err := a.verify(next, dir, false); err != nil {
		t.Fatal("new provider plist not recovered", err)
	}
}
func TestStartRejectsProviderRecoveryThatChangesExecutableIdentity(t *testing.T) {
	a, r, dir, c := testAgents(t)
	previous := c
	previous.Backends = "herdr"
	previous.HerdrPath = "/tmp/herdr"
	previous.Daemon = "/tmp/other-daemon"
	b, _ := json.Marshal(previous)
	if err := WritePrivate(filepath.Join(dir, "config.previous.json"), b, false); err != nil {
		t.Fatal(err)
	}
	plist, _ := a.expected(previous, dir, false)
	if err := WritePrivate(a.path(false), plist, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), c, dir); err == nil {
		t.Fatal("accepted a foreign executable")
	}
	if len(r.calls) != 0 {
		t.Fatal("touched launchd before verifying identity")
	}
}

func TestManagedProvidersPersistTheInvokingShellsHerdrLocation(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "selected")
	t.Setenv("XDG_CONFIG_HOME", selected)
	dir := testDir(t)
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	a := Agents{Runner: &fakeRunner{}, Home: t.TempDir(), UID: 501}
	if err := a.Providers(context.Background(), c, dir, "herdr", "/tmp/herdr", "work", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "launchd"))
	next, err := LoadConfig(dir)
	if err != nil || next.HerdrConfigHome != selected {
		t.Fatalf("lost configured server location: %+v %v", next, err)
	}
	if !strings.Contains(strings.Join(DaemonArgs(next, dir), " "), "--herdr-config-home "+selected) {
		t.Fatal("launchd did not receive the pinned location")
	}
}

// A failed recovery-journal write must not disconnect a healthy companion.
func TestProviderJournalFailureLeavesCompanionRunning(t *testing.T) {
	a, r, dir, c := testAgents(t)
	// A directory at the journal path deterministically fails its private-file
	// validation, covering the same pre-commit error path as a failed write.
	if err := os.Mkdir(filepath.Join(dir, "config.previous.json"), 0700); err != nil {
		t.Fatal(err)
	}
	r.run = func(_ string, args []string) ([]byte, error) {
		if args[0] != "print" {
			t.Fatalf("changed running service before journal was durable: %v", args)
		}
		if strings.HasSuffix(args[1], RenewalLabel) {
			return nil, errNotLoaded
		}
		return jobOutput(c, dir, false), nil
	}
	if err := a.Providers(context.Background(), c, dir, "herdr", "/tmp/herdr", "default", ""); err == nil {
		t.Fatal("accepted invalid recovery journal")
	}
	actual, err := LoadConfig(dir)
	if err != nil || actual != c {
		t.Fatalf("changed configuration: %+v %v", actual, err)
	}
	if err := a.verify(c, dir, false); err != nil {
		t.Fatal(err)
	}
}
