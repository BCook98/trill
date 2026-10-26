package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type mixedRunner struct {
	fakeRunner
	catalogMu     sync.Mutex
	snapshots     map[string]string
	offlineXirp   bool
	busy          bool
	herdrCommands []call
}

func (r *mixedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "/tmp/herdr" {
		if r.offlineXirp {
			return nil, errors.New("offline")
		}
		return r.fakeRunner.Run(ctx, name, args...)
	}
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	r.herdrCommands = append(r.herdrCommands, call{name, append([]string(nil), args...)})
	if reflect.DeepEqual(args, []string{"api", "schema", "--json"}) {
		return []byte(`{"protocol":22}`), nil
	}
	if len(args) < 4 || args[0] != "--session" {
		return nil, errors.New("unexpected command")
	}
	snapshot, ok := r.snapshots[args[1]]
	if !ok {
		return nil, errors.New("server unavailable")
	}
	if reflect.DeepEqual(args[2:], []string{"api", "snapshot"}) {
		return []byte(snapshot), nil
	}
	if len(args) == 6 && args[2] == "pane" && args[3] == "rename" {
		var value map[string]any
		_ = json.Unmarshal([]byte(snapshot), &value)
		panes := value["result"].(map[string]any)["snapshot"].(map[string]any)["panes"].([]any)
		panes[0].(map[string]any)["label"] = args[5]
		b, _ := json.Marshal(value)
		r.snapshots[args[1]] = string(b)
		return []byte(`{"result":{}}`), nil
	}
	return nil, errors.New("unexpected mutation")
}
func (r *mixedRunner) CheckHerdrAttach(context.Context, string, []string, []string) error {
	if r.busy {
		return errTerminalBusy
	}
	return nil
}
func mixedService(t *testing.T, r *mixedRunner) *Service {
	t.Helper()
	r.projectsJSON = `[{"id":"p1","name":"Same name","path":"/tmp"}]`
	r.sessions = []rawSession{{ID: "x1", ProjectID: "p1", Name: "Xirp terminal", Mode: "terminal", TmuxSession: "xirp-one", Status: "running", TerminalLive: true}}
	s, err := NewService(Config{Backends: "xirp,herdr", HerdrPath: "/tmp/herdr", HerdrSessions: "default,work", AllowControl: true}, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func TestMixedProvidersKeepIdentitiesCapabilitiesAndRoutesSeparate(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	ctx := context.Background()
	c, err := s.catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := c.sessions()
	if len(rows) != 3 || len(c.Projects) != 3 || len(c.Providers) != 3 {
		t.Fatalf("catalog size: %+v", c)
	}
	if rows[0].ID != "x1" || !rows[0].Capabilities.Stop || rows[1].Capabilities.Stop || !rows[1].Capabilities.Rename || rows[1].ID == rows[2].ID {
		t.Fatalf("identities/capabilities: %+v", rows)
	}
	if err = s.Rename(ctx, rows[2].ID, "Work only"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.snapshots["default"], "Work only") || !strings.Contains(r.snapshots["work"], "Work only") {
		t.Fatal("rename crossed servers")
	}
	if err = s.Stop(ctx, rows[1].ID); !errors.Is(err, errUnsupported) {
		t.Fatal(err)
	}
	if err = s.Rename(ctx, "x1", "Xirp only"); err != nil {
		t.Fatal(err)
	}
	if r.sessions[0].Name != "Xirp only" {
		t.Fatal("Xirp route lost")
	}
}
func TestMixedCatalogSurvivesOneOrMoreOfflineProviders(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"work": herdrFixture}}
	s := mixedService(t, r)
	ctx := context.Background()
	c, err := s.catalog(ctx)
	if err != nil || len(c.Raw) != 2 || c.Providers[1].Available || c.Providers[1].Message == "" {
		t.Fatalf("partial failure: %+v %v", c, err)
	}
	r.offlineXirp = true
	c, err = s.catalog(ctx)
	if err != nil || len(c.Raw) != 1 || c.Providers[0].Available {
		t.Fatalf("Herdr-only recovery: %+v %v", c, err)
	}
	r.snapshots = nil
	if _, err = s.catalog(ctx); err == nil {
		t.Fatal("all providers offline appeared healthy")
	}
}
func TestHerdrBusyPreflightDoesNotAllocateMosh(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}, busy: true}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	if _, err := s.Bootstrap(context.Background(), rows[1].ID, 80, 24); !errors.Is(err, errTerminalBusy) {
		t.Fatal(err)
	}
	if len(r.moshArgs) != 0 || len(s.leases) != 0 {
		t.Fatal("busy terminal allocated a lease")
	}
}
func TestSecondHerdrLeaseIsRejectedUntilOwnerDisconnects(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	id := rows[1].ID
	lease, err := s.Bootstrap(context.Background(), id, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bootstrap(context.Background(), id, 80, 24); !errors.Is(err, errTerminalBusy) {
		t.Fatal(err)
	}
	s.DeleteLease(lease.LeaseID)
	if _, err = s.Bootstrap(context.Background(), id, 80, 24); err != nil {
		t.Fatal(err)
	}
}

func (r *mixedRunner) RunWithEnv(ctx context.Context, name string, args, env []string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
