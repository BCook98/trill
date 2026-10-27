package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar/pairing"
)

// Match the native launchctl print layout, including argv[0] and nested fields.
func jobOutput(c Config, dir string, renew bool) []byte {
	args := agentArgs(c, dir, renew)
	var b strings.Builder
	fmt.Fprintf(&b, "gui/501/job = {\n\tprogram = %s\n\targuments = {\n", args[0])
	for _, arg := range args {
		fmt.Fprintf(&b, "\t\t%s\n", arg)
	}
	b.WriteString("\t}\n\tstate = running\n\tenvironment = {\n\t\tSECRET_ENV => hidden\n\t}\n}\n")
	return []byte(b.String())
}

func TestLoadedOwnershipAcceptsNestedCodeSigningRequirements(t *testing.T) {
	_, _, dir, config := testAgents(t)
	output := bytes.Replace(jobOutput(config, dir, false), []byte("\tstate = running\n"), []byte("\tLWCR = {\n\t\t\"reqs\" => {\n\t\t\t\"cdhash\" => {\n\t\t\t\tprogram = /unrelated/program\n\t\t\t}\n\t\t}\n\t}\n\tstate = running\n"), 1)
	job, err := parseLoadedJob(output)
	if err != nil {
		t.Fatal(err)
	}
	if !job.running || job.program != config.Daemon || strings.Join(job.args, "\x00") != strings.Join(agentArgs(config, dir, false), "\x00") {
		t.Fatalf("nested signing requirements changed loaded identity: %+v", job)
	}
}

func TestLoadedOwnershipRequiredForEveryServiceAction(t *testing.T) {
	for _, mutation := range []string{"program", "state", "extra argument", "missing argument", "malformed", "nested identity", "query failed"} {
		for _, action := range []string{"start", "stop", "uninstall", "status", "control"} {
			t.Run(mutation+"/"+action, func(t *testing.T) {
				a, r, dir, c := testAgents(t)
				before, _ := ReadPrivate(a.path(false))
				foreign := c
				foreign.Certificate = "/another/state/tls.pem"
				foreign.PrivateKey = foreign.Certificate
				output := jobOutput(c, dir, false)
				switch mutation {
				case "program":
					output = bytes.Replace(output, []byte("\tprogram = "+c.Daemon), []byte("\tprogram = /another/trilld"), 1)
				case "state":
					output = jobOutput(foreign, "/another/state", false)
				case "extra argument":
					output = bytes.Replace(output, []byte("\t}\n"), []byte("\t\t--allow-control\n\t}\n"), 1)
				case "missing argument":
					output = bytes.Replace(output, []byte("\t\t--pairing-store\n"), nil, 1)
				case "malformed":
					output = []byte("state = running")
				case "nested identity":
					output = append([]byte("gui/501/job = {\n\tenvironment = {\n"), append(output, []byte("\t}\n}\n")...)...)
				}
				r.run = func(_ string, args []string) ([]byte, error) {
					if args[0] != "print" {
						t.Fatalf("unverified service action: %v", args)
					}
					if mutation == "query failed" {
						return nil, errors.New("permission denied")
					}
					if strings.HasSuffix(args[1], RenewalLabel) {
						return nil, errNotLoaded
					}
					return output, nil
				}
				var err error
				switch action {
				case "start":
					err = a.Start(context.Background(), c, dir)
				case "stop":
					err = a.Stop(context.Background(), c, dir)
				case "uninstall":
					err = a.Uninstall(context.Background(), c, dir)
				case "status":
					_, err = a.Status(context.Background(), c, dir)
				case "control":
					err = a.Control(context.Background(), c, dir, !c.AllowControl)
				}
				if err == nil {
					t.Fatal("accepted unverified loaded identity")
				}
				after, _ := ReadPrivate(a.path(false))
				if !bytes.Equal(before, after) {
					t.Fatal("changed plist before verifying runtime ownership")
				}
				saved, err := LoadConfig(dir)
				if err != nil || saved != c {
					t.Fatal("changed config before verifying ownership", err)
				}
			})
		}
	}
}

func TestForeignRenewalPreflightPreventsPartialServiceActions(t *testing.T) {
	for _, action := range []string{"start", "stop", "control"} {
		t.Run(action, func(t *testing.T) {
			a, r, dir, c := testAgents(t)
			r.run = func(_ string, args []string) ([]byte, error) {
				if args[0] != "print" {
					t.Fatalf("action before all identities verified: %v", args)
				}
				if strings.HasSuffix(args[1], RenewalLabel) {
					return jobOutput(c, "/foreign/state", true), nil
				}
				return jobOutput(c, dir, false), nil
			}
			var err error
			switch action {
			case "start":
				err = a.Start(context.Background(), c, dir)
			case "stop":
				err = a.Stop(context.Background(), c, dir)
			case "control":
				err = a.Control(context.Background(), c, dir, true)
			}
			if err == nil {
				t.Fatal("accepted foreign loaded renewal job")
			}
		})
	}
}

func TestStartRecoversInterruptedControlWrites(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, loaded := range []string{"absent", "old", "new"} {
			t.Run(fmt.Sprintf("allow=%t/loaded=%s", allow, loaded), func(t *testing.T) {
				a, r, dir, initial := testAgents(t)
				desired := initial
				desired.AllowControl = allow
				old := desired
				old.AllowControl = !allow
				// Both historical write orders reduce to the same safe recovery condition:
				// persisted config plus its exact plist with only AllowControl inverted.
				current, err := json.Marshal(desired)
				if err != nil {
					t.Fatal(err)
				}
				if err := WritePrivate(filepath.Join(dir, "config.json"), current, true); err != nil {
					t.Fatal(err)
				}
				opposite, _ := a.expected(old, dir, false)
				if err := WritePrivate(a.path(false), opposite, true); err != nil {
					t.Fatal(err)
				}
				var daemon *Config
				if loaded == "old" {
					daemon = &old
				} else if loaded == "new" {
					daemon = &desired
				}
				renewal := false
				stops := 0
				r.run = func(_ string, args []string) ([]byte, error) {
					switch args[0] {
					case "print":
						if strings.HasSuffix(args[1], RenewalLabel) {
							if renewal {
								return jobOutput(desired, dir, true), nil
							}
							return nil, errNotLoaded
						}
						if daemon == nil {
							return nil, errNotLoaded
						}
						return jobOutput(*daemon, dir, false), nil
					case "bootout":
						if args[1] != "gui/501/"+Label || loaded != "old" {
							t.Fatal("unexpected recovery stop", args)
						}
						stops++
						daemon = nil
						return nil, nil
					case "bootstrap":
						if args[2] == a.path(true) {
							renewal = true
						} else if args[2] == a.path(false) {
							daemon = &desired
						} else {
							t.Fatal(args)
						}
						return nil, nil
					}
					t.Fatal(args)
					return nil, nil
				}
				if err := a.Start(context.Background(), desired, dir); err != nil {
					t.Fatal(err)
				}
				if err := a.verify(desired, dir, false); err != nil {
					t.Fatal("did not repair plist", err)
				}
				saved, err := LoadConfig(dir)
				if err != nil || saved != desired {
					t.Fatal("recovery changed persisted choice", err)
				}
				if loaded == "old" && stops != 1 {
					t.Fatal("old owned runtime not replaced")
				}
				if loaded != "old" && stops != 0 {
					t.Fatal("unnecessary restart")
				}
			})
		}
	}
}

func TestControlRecoveryRefusesUnrelatedChanges(t *testing.T) {
	for _, change := range []string{"disk arguments", "saved config", "runtime", "renewal"} {
		t.Run(change, func(t *testing.T) {
			a, r, dir, c := testAgents(t)
			old := c
			old.AllowControl = !c.AllowControl
			if change == "disk arguments" {
				old.RemovedTmuxSocket = "/foreign/lab.sock"
			}
			plist, _ := a.expected(old, dir, false)
			if err := WritePrivate(a.path(false), plist, true); err != nil {
				t.Fatal(err)
			}
			requested := c
			if change == "saved config" {
				requested.Name = "Different"
			}
			r.run = func(_ string, args []string) ([]byte, error) {
				if args[0] != "print" {
					t.Fatalf("unowned recovery action %v", args)
				}
				if strings.HasSuffix(args[1], RenewalLabel) {
					if change == "renewal" {
						return jobOutput(c, "/foreign", true), nil
					}
					return nil, errNotLoaded
				}
				return jobOutput(old, "/foreign", false), nil
			}
			if err := a.Start(context.Background(), requested, dir); err == nil {
				t.Fatal("repaired without exact ownership")
			}
			after, _ := ReadPrivate(a.path(false))
			if !bytes.Equal(plist, after) {
				t.Fatal("overwrote unowned/inconsistent plist")
			}
		})
	}
}

func TestPrivateStateRejectsUnsafeAncestorsBeforeWriting(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink-parent=%t", alias), func(t *testing.T) {
			base := t.TempDir()
			shared := filepath.Join(base, "shared")
			if err := os.Mkdir(shared, 0777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(shared, 0777); err != nil {
				t.Fatal(err)
			}
			parent := shared
			if alias {
				parent = filepath.Join(base, "alias")
				if err := os.Symlink(shared, parent); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(parent, "new-parent", "state")
			if err := PrivateDir(dir); err == nil {
				t.Fatal("accepted writable ancestor")
			}
			if _, err := os.Stat(filepath.Join(shared, "new-parent")); !os.IsNotExist(err) {
				t.Fatal("created state before checking ancestor")
			}
			// Recheck existing state as well as the initial mkdir, including provisioning.
			dir = filepath.Join(parent, "existing")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			c := testConfig(dir)
			if err := SaveConfig(dir, c); err == nil {
				t.Fatal("saved config in unsafe tree")
			}
			if _, err := NewCertificates().Provision(context.Background(), dir, Network{Kind: "lan", IP: c.IP, Host: "my-mac.local"}, c.Name); err == nil {
				t.Fatal("wrote private certificate in unsafe tree")
			}
			if _, err := os.Stat(c.Certificate); !os.IsNotExist(err) {
				t.Fatal("left private certificate")
			}
			// Fixture writing bypasses setup checks to verify LoadConfig rejects it too.
			if err := WritePrivate(filepath.Join(dir, "config.json"), []byte("{}"), false); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(dir); err == nil {
				t.Fatal("loaded unsafe state")
			}
		})
	}
	// Safe symlink parents remain compatible with macOS /tmp and existing aliases.
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(alias, "state")
	if err := PrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	c := testConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pairing.NewStore(filepath.Join(dir, "devices.json")).CreateInvitation(time.Minute); err != nil {
		t.Fatal("setup accepted a location pairing rejects", err)
	}
}

func TestServiceActionsSerializeWithControl(t *testing.T) {
	a, r, dir, c := testAgents(t)
	unlock, err := lockControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, op := range []func() error{
		func() error { return a.Start(context.Background(), c, dir) },
		func() error { return a.Stop(context.Background(), c, dir) },
		func() error { return a.Install(c, dir) },
		func() error { return a.Uninstall(context.Background(), c, dir) },
		func() error { return a.Control(context.Background(), c, dir, true) },
	} {
		if err := op(); err == nil || !strings.Contains(err.Error(), "in progress") {
			t.Fatal("service action ignored control lock", err)
		}
	}
	if len(r.calls) != 0 {
		t.Fatal("queried or changed service during control commit")
	}
}

// No concurrent goroutines are needed: commit the intervening change after the
// caller took its snapshot, then invoke the service action with that snapshot.
func TestInstallAndStartRejectStaleConfiguration(t *testing.T) {
	for _, installed := range []bool{false, true} {
		for _, change := range []string{"disable control", "providers", "missing config", "invalid config"} {
			for _, action := range []string{"install", "start"} {
				t.Run(fmt.Sprintf("installed=%t/%s/%s", installed, change, action), func(t *testing.T) {
					dir := testDir(t)
					stale := testConfig(dir)
					stale.AllowControl = true
					if err := SaveConfig(dir, stale); err != nil {
						t.Fatal(err)
					}
					r := &fakeRunner{run: func(name string, args []string) ([]byte, error) {
						t.Fatalf("stale configuration reached service command: %s %v", name, args)
						return nil, nil
					}}
					a := Agents{Home: t.TempDir(), UID: 501, Runner: r, ListenCheck: func(string) error {
						t.Fatal("stale configuration reached listener check")
						return nil
					}}
					if installed {
						if err := a.Install(stale, dir); err != nil {
							t.Fatal(err)
						}
					}
					if change == "disable control" && !installed {
						// The reported setup race: a completed config-only disable
						// must prevent installation of the caller's enabled snapshot.
						if err := a.Control(context.Background(), stale, dir, false); err != nil {
							t.Fatal(err)
						}
					} else if change == "missing config" {
						if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
							t.Fatal(err)
						}
					} else {
						current := stale
						current.AllowControl = false
						if change == "providers" {
							current = stale
							current.Backends = "herdr"
						}
						data, err := json.Marshal(current)
						if err != nil {
							t.Fatal(err)
						}
						if change == "invalid config" {
							data = []byte("{}")
						}
						// Matching old plists model an interrupted committed change.
						if err := WritePrivate(filepath.Join(dir, "config.json"), data, true); err != nil {
							t.Fatal(err)
						}
					}
					paths := []string{filepath.Join(dir, "config.json"), stale.Certificate, filepath.Join(dir, "devices.json"), a.path(false), a.path(true)}
					for _, path := range paths[1:3] {
						if err := WritePrivate(path, []byte("preserved private state"), false); err != nil {
							t.Fatal(err)
						}
					}
					before := make([][]byte, len(paths))
					for i, path := range paths {
						var err error
						before[i], err = os.ReadFile(path)
						if err != nil && !errors.Is(err, os.ErrNotExist) {
							t.Fatal(err)
						}
					}
					var err error
					if action == "install" {
						err = a.Install(stale, dir)
					} else {
						err = a.Start(context.Background(), stale, dir)
					}
					if err == nil {
						t.Fatal("accepted stale configuration")
					}
					if (change == "disable control" || change == "providers") && !strings.Contains(err.Error(), "configuration changed") {
						t.Fatalf("missing stale configuration diagnostic: %v", err)
					}
					for i, path := range paths {
						after, err := os.ReadFile(path)
						if before[i] == nil {
							if !errors.Is(err, os.ErrNotExist) {
								t.Fatalf("created missing state %s: %v", path, err)
							}
						} else if err != nil || !bytes.Equal(before[i], after) {
							t.Fatalf("changed preserved state %s: %v", path, err)
						}
					}
				})
			}
		}
	}
}
