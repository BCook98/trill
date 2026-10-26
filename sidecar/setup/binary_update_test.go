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

func binaryUpdateFixture(t *testing.T) (Agents, *fakeRunner, string, Config, string, map[string]bool) {
	t.Helper()
	dir := testDir(t)
	root := filepath.Join(t.TempDir(), "installation")
	for _, folder := range []string{root, filepath.Join(root, "releases"), filepath.Join(root, "releases", "old"), filepath.Join(root, "releases", "new")} {
		if err := PrivateDir(folder); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetCurrentRelease(root, "releases/old"); err != nil {
		t.Fatal(err)
	}
	c := testConfig(dir)
	c.CLI = filepath.Join(root, "bin", "trill")
	c.Daemon = filepath.Join(root, "bin", "trilld")
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	agents := Agents{Runner: runner, Home: t.TempDir(), UID: 501}
	if err := agents.Install(c, dir); err != nil {
		t.Fatal(err)
	}
	loaded := map[string]bool{}
	runner.run = func(name string, arguments []string) ([]byte, error) {
		if name != "/bin/launchctl" {
			t.Fatalf("unexpected executable %s", name)
		}
		label := strings.TrimPrefix(arguments[1], "gui/501/")
		switch arguments[0] {
		case "print":
			if loaded[label] {
				return jobOutput(c, dir, label == RenewalLabel), nil
			}
			return nil, errNotLoaded
		case "bootout":
			delete(loaded, label)
			return nil, nil
		case "bootstrap":
			label = strings.TrimSuffix(filepath.Base(arguments[2]), ".plist")
			loaded[label] = true
			return nil, nil
		default:
			t.Fatalf("unexpected launchctl action %v", arguments)
			return nil, nil
		}
	}
	return agents, runner, dir, c, root, loaded
}

func TestBinaryUpdatePreservesLoadedStateAndPrivateData(t *testing.T) {
	for _, daemon := range []bool{false, true} {
		for _, renewal := range []bool{false, true} {
			t.Run(strings.Join([]string{boolName(daemon), boolName(renewal)}, "/"), func(t *testing.T) {
				agents, _, dir, c, root, loaded := binaryUpdateFixture(t)
				loaded[Label] = daemon
				loaded[RenewalLabel] = renewal
				before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
				if err := WritePrivate(filepath.Join(dir, "devices.json"), []byte("paired-device-fixture"), false); err != nil {
					t.Fatal(err)
				}
				healthCalls := 0
				err := agents.SwitchRelease(context.Background(), c, dir, root, "releases/new", func(context.Context) error {
					healthCalls++
					if unlock, err := lockControl(dir); err == nil {
						unlock()
						t.Fatal("service lock released during verification")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if loaded[Label] != daemon || loaded[RenewalLabel] != renewal {
					t.Fatal("changed previously loaded service state")
				}
				if (healthCalls == 1) != daemon {
					t.Fatal("health check did not match daemon state")
				}
				current, _ := os.Readlink(filepath.Join(root, "current"))
				if current != "releases/new" {
					t.Fatal("did not switch release")
				}
				after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
				if !bytes.Equal(before, after) {
					t.Fatal("modified configuration")
				}
				devices, _ := ReadPrivate(filepath.Join(dir, "devices.json"))
				if string(devices) != "paired-device-fixture" {
					t.Fatal("modified pairings")
				}
				if _, err := os.Lstat(filepath.Join(dir, ".binary-update.json")); !os.IsNotExist(err) {
					t.Fatal("left successful journal")
				}
			})
		}
	}
}

func boolName(value bool) string {
	if value {
		return "loaded"
	}
	return "stopped"
}

func TestBinaryUpdateRollsBackOnHealthFailure(t *testing.T) {
	agents, _, dir, c, root, loaded := binaryUpdateFixture(t)
	loaded[Label] = true
	err := agents.SwitchRelease(context.Background(), c, dir, root, "releases/new", func(context.Context) error { return errors.New("unhealthy fixture") })
	if err == nil || !strings.Contains(err.Error(), "previous release") {
		t.Fatalf("%v", err)
	}
	current, _ := os.Readlink(filepath.Join(root, "current"))
	if current != "releases/old" || !loaded[Label] || loaded[RenewalLabel] {
		t.Fatal("rollback did not restore release and services")
	}
}

func TestBinaryUpdateRecoversInterruptedTransactionAndRejectsForeignServices(t *testing.T) {
	agents, _, dir, c, root, loaded := binaryUpdateFixture(t)
	journal, _ := json.Marshal(binaryUpdateJournal{"releases/old", true, false})
	if err := WritePrivate(filepath.Join(dir, ".binary-update.json"), journal, false); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentRelease(root, "releases/new"); err != nil {
		t.Fatal(err)
	}
	loaded[RenewalLabel] = true
	recovered, err := agents.RecoverRelease(context.Background(), c, dir, root)
	if err != nil || !recovered {
		t.Fatalf("%t %v", recovered, err)
	}
	current, _ := os.Readlink(filepath.Join(root, "current"))
	if current != "releases/old" || !loaded[Label] || loaded[RenewalLabel] {
		t.Fatal("interrupted update not restored")
	}
	if err := WritePrivate(agents.path(true), []byte("foreign plist"), true); err != nil {
		t.Fatal(err)
	}
	if err := agents.SwitchRelease(context.Background(), c, dir, root, "releases/new", func(context.Context) error { return nil }); err == nil {
		t.Fatal("updated with a foreign service")
	}
}

func TestReleasePointerRejectsTraversalAndSymlinkedTargets(t *testing.T) {
	_, _, _, _, root, _ := binaryUpdateFixture(t)
	for _, target := range []string{"../escape", "releases/../new", "/absolute", "releases/new/child"} {
		if err := SetCurrentRelease(root, target); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	if err := os.Symlink(filepath.Join(root, "releases", "new"), filepath.Join(root, "releases", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentRelease(root, "releases/linked"); err == nil {
		t.Fatal("followed release symlink")
	}
}

func TestMissingConfigurationCannotUpdateUnderExistingServices(t *testing.T) {
	agents, runner, _, _, _, _ := binaryUpdateFixture(t)
	if err := agents.RequireNoServices(context.Background()); err == nil {
		t.Fatal("ignored installed LaunchAgents")
	}
	agents.Home = t.TempDir()
	runner.run = func(string, []string) ([]byte, error) { return nil, nil }
	if err := agents.RequireNoServices(context.Background()); err == nil {
		t.Fatal("ignored a loaded job without an on-disk plist")
	}
	runner.run = func(string, []string) ([]byte, error) { return nil, errors.New("unknown failure") }
	if err := agents.RequireNoServices(context.Background()); err == nil {
		t.Fatal("treated query failure as absent job")
	}
	runner.run = func(string, []string) ([]byte, error) { return nil, errNotLoaded }
	if err := agents.RequireNoServices(context.Background()); err != nil {
		t.Fatal(err)
	}
}
