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
	"time"
)

type unloadCase struct {
	name, action string
	allow        bool
}

var unloadCases = []unloadCase{
	{name: "stop", action: "stop"},
	{name: "uninstall", action: "uninstall"},
	{name: "control enable", action: "control", allow: true},
	{name: "control disable", action: "control"},
	{name: "repair enable", action: "repair", allow: true},
	{name: "repair disable", action: "repair"},
}

type unloadFixture struct {
	a                Agents
	r                *fakeRunner
	dir              string
	runtime, desired Config
	run              func(context.Context) error
}

func newUnloadFixture(t *testing.T, tc unloadCase) unloadFixture {
	t.Helper()
	a, r, dir, runtime := testAgents(t)
	if tc.action == "control" || tc.action == "repair" {
		runtime.AllowControl = !tc.allow
	}
	plist, err := a.expected(runtime, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(a.path(false), plist, true); err != nil {
		t.Fatal(err)
	}
	desired := runtime
	if tc.action == "control" || tc.action == "repair" {
		desired.AllowControl = tc.allow
	}
	saved := runtime
	if tc.action == "repair" {
		saved = desired
	}
	current, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(dir, "config.json"), current, true); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context) error {
		switch tc.action {
		case "stop":
			return a.Stop(ctx, runtime, dir)
		case "uninstall":
			return a.Uninstall(ctx, runtime, dir)
		case "control":
			return a.Control(ctx, runtime, dir, tc.allow)
		case "repair":
			return a.Start(ctx, desired, dir)
		default:
			t.Fatalf("unknown action %q", tc.action)
			return nil
		}
	}
	return unloadFixture{a, r, dir, runtime, desired, run}
}

func TestServiceActionsWaitForDelayedUnload(t *testing.T) {
	for _, tc := range unloadCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnloadFixture(t, tc)
			jobs := map[string]Config{Label: f.runtime, RenewalLabel: f.runtime}
			pending, polls := map[string]int{}, map[string]int{}
			stops, starts := map[string]int{}, map[string]int{}
			removed := map[string]bool{}
			f.r.run = func(name string, args []string) ([]byte, error) {
				if name != "/bin/launchctl" {
					t.Fatalf("unexpected executable %s", name)
				}
				label := strings.TrimPrefix(args[1], "gui/501/")
				switch args[0] {
				case "print":
					if remaining, ok := pending[label]; ok {
						polls[label]++
						if unlock, err := lockControl(f.dir); err == nil {
							unlock()
							t.Fatal("released service lock before verified unload")
						}
						if remaining == 0 {
							delete(jobs, label)
							delete(pending, label)
							removed[label] = true
							return nil, errNotLoaded
						}
						pending[label]--
						// A present job with no running process is still loaded.
						return bytes.Replace(jobOutput(jobs[label], f.dir, label == RenewalLabel), []byte("state = running"), []byte("state = exiting"), 1), nil
					}
					if c, ok := jobs[label]; ok {
						return jobOutput(c, f.dir, label == RenewalLabel), nil
					}
					return nil, errNotLoaded
				case "bootout":
					if label != Label && label != RenewalLabel {
						t.Fatalf("foreign bootout: %v", args)
					}
					stops[label]++
					if stops[label] != 1 {
						t.Fatalf("duplicate bootout: %v", args)
					}
					pending[label] = 2
					return nil, nil
				case "bootstrap":
					label = strings.TrimSuffix(filepath.Base(args[2]), ".plist")
					if _, present := jobs[label]; present || !removed[label] {
						t.Fatalf("bootstrap before verified removal: %v", args)
					}
					if args[1] != "gui/501" || args[2] != f.a.path(label == RenewalLabel) {
						t.Fatalf("foreign bootstrap: %v", args)
					}
					if err := f.a.verify(f.desired, f.dir, label == RenewalLabel); err != nil {
						t.Fatal("bootstrap used stale plist:", err)
					}
					starts[label]++
					jobs[label] = f.desired
					return nil, nil
				default:
					t.Fatalf("unexpected command: %v", args)
					return nil, nil
				}
			}
			if err := f.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.action == "stop" || tc.action == "uninstall" {
				if len(jobs) != 0 || stops[Label] != 1 || stops[RenewalLabel] != 1 {
					t.Fatalf("returned before both jobs unloaded: jobs=%v stops=%v", jobs, stops)
				}
				if tc.action == "stop" {
					// Reproduce stop && start: neither terminating job may suppress bootstrap.
					if err := f.a.Start(context.Background(), f.desired, f.dir); err != nil {
						t.Fatal(err)
					}
					if starts[Label] != 1 || starts[RenewalLabel] != 1 {
						t.Fatalf("start skipped bootstrap after stop: %v", starts)
					}
				} else {
					for _, renew := range []bool{false, true} {
						if _, err := os.Stat(f.a.path(renew)); !errors.Is(err, os.ErrNotExist) {
							t.Fatal("uninstall left plist:", err)
						}
					}
				}
			} else if stops[Label] != 1 || starts[Label] != 1 || stops[RenewalLabel] != 0 || starts[RenewalLabel] != 0 {
				t.Fatalf("control restart affected wrong services: stops=%v starts=%v", stops, starts)
			}
			for label := range stops {
				if polls[label] != 3 {
					t.Fatalf("did not wait through delayed unload of %s: %v", label, polls)
				}
			}
		})
	}
}

func TestUnloadFailureLeavesServiceActionsClosed(t *testing.T) {
	for _, tc := range unloadCases {
		labels := []string{Label}
		if tc.action == "stop" || tc.action == "uninstall" {
			labels = append(labels, RenewalLabel)
		}
		for _, label := range labels {
			for _, failure := range []string{"bootout", "query", "malformed", "foreign", "canceled", "canceled absence", "timeout"} {
				t.Run(tc.name+"/"+label+"/"+failure, func(t *testing.T) {
					f := newUnloadFixture(t, tc)
					paths := []string{f.a.path(false), f.a.path(true), filepath.Join(f.dir, "config.json")}
					before := make([][]byte, len(paths))
					for i, path := range paths {
						var err error
						before[i], err = ReadPrivate(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if failure == "timeout" {
						var deadlineCancel context.CancelFunc
						ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
						defer deadlineCancel()
					}
					stopped, polls := false, 0
					f.r.run = func(name string, args []string) ([]byte, error) {
						if name != "/bin/launchctl" || args[1] != "gui/501/"+label {
							// Only the target is loaded; the other job is absent at preflight.
							if name == "/bin/launchctl" && args[0] == "print" && !stopped {
								return nil, errNotLoaded
							}
							t.Fatalf("unexpected/foreign action: %s %v", name, args)
						}
						switch args[0] {
						case "print":
							if !stopped {
								return jobOutput(f.runtime, f.dir, label == RenewalLabel), nil
							}
							polls++
							switch failure {
							case "query":
								return nil, errors.New("permission denied")
							case "malformed":
								return []byte("state = exited"), nil
							case "foreign":
								return jobOutput(f.runtime, "/foreign/state", label == RenewalLabel), nil
							case "canceled", "canceled absence":
								cancel()
								if failure == "canceled absence" {
									return nil, errNotLoaded
								}
							}
							return jobOutput(f.runtime, f.dir, label == RenewalLabel), nil
						case "bootout":
							if stopped {
								t.Fatal("duplicate bootout")
							}
							stopped = true
							if failure == "bootout" {
								return nil, errors.New("cannot stop")
							}
							return nil, nil
						default:
							t.Fatalf("mutated service after unload failure: %v", args)
							return nil, nil
						}
					}
					err := f.run(ctx)
					if err == nil || !stopped {
						t.Fatalf("unconfirmed unload succeeded or missed bootout: %v", err)
					}
					if failure != "bootout" && (!strings.Contains(err.Error(), "unload not confirmed") || !strings.Contains(err.Error(), "--state-dir")) {
						t.Fatalf("missing actionable unload error: %v", err)
					}
					if strings.HasPrefix(failure, "canceled") && !errors.Is(err, context.Canceled) {
						t.Fatalf("lost cancellation: %v", err)
					}
					if failure == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("lost deadline: %v", err)
					}
					if failure != "bootout" && polls == 0 {
						t.Fatal("did not verify unload")
					}
					for i, path := range paths {
						after, readErr := ReadPrivate(path)
						if readErr != nil || !bytes.Equal(before[i], after) {
							t.Fatalf("unconfirmed unload changed %s: %v", path, readErr)
						}
					}
				})
			}
		}
	}
}

type unloadRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func TestStopRechecksDaemonOwnershipAfterRenewalUnload(t *testing.T) {
	for _, replacement := range []string{"foreign", "unknown", "absent"} {
		t.Run(replacement, func(t *testing.T) {
			a, r, dir, c := testAgents(t)
			renewalStopped, daemonRechecked := false, false
			r.run = func(_ string, args []string) ([]byte, error) {
				switch args[0] {
				case "print":
					if args[1] == "gui/501/"+RenewalLabel {
						if renewalStopped {
							return nil, errNotLoaded
						}
						return jobOutput(c, dir, true), nil
					}
					if renewalStopped {
						daemonRechecked = true
						switch replacement {
						case "foreign":
							return jobOutput(c, "/foreign/state", false), nil
						case "unknown":
							return nil, errors.New("permission denied")
						case "absent":
							return nil, errNotLoaded
						}
					}
					return jobOutput(c, dir, false), nil
				case "bootout":
					if args[1] != "gui/501/"+RenewalLabel || renewalStopped {
						t.Fatalf("acted on stale ownership or repeated bootout: %v", args)
					}
					renewalStopped = true
					return nil, nil
				default:
					t.Fatalf("unexpected action: %v", args)
					return nil, nil
				}
			}
			err := a.Stop(context.Background(), c, dir)
			if !daemonRechecked || (err == nil) != (replacement == "absent") {
				t.Fatalf("did not handle changed ownership safely: rechecked=%t err=%v", daemonRechecked, err)
			}
		})
	}
}

func (f unloadRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestUnloadWaitBoundsPrintContext(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "background", true: "caller deadline"}[callerDeadline], func(t *testing.T) {
			a, _, dir, c := testAgents(t)
			ctx := context.Background()
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			queried := false
			a.Runner = unloadRunnerFunc(func(queryCtx context.Context, name string, args ...string) ([]byte, error) {
				queried = true
				deadline, ok := queryCtx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > unloadTimeout {
					t.Fatal("print did not inherit bounded unload deadline")
				}
				if want, ok := ctx.Deadline(); ok && !deadline.Equal(want) {
					t.Fatal("extended caller deadline")
				}
				if name != "/bin/launchctl" || len(args) != 2 || args[0] != "print" || args[1] != "gui/501/"+Label {
					t.Fatalf("unload wait issued non-read-only query: %s %v", name, args)
				}
				return nil, errNotLoaded
			})
			if err := a.waitUnloaded(ctx, c, dir, false); err != nil || !queried {
				t.Fatalf("absence was not confirmed: %v", err)
			}
		})
	}
}
