package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar/pairing"
)

func TestHerdrReconnectReplacesOnlySamePairedDeviceAndTerminal(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	store := pairing.NewStore(filepath.Join(t.TempDir(), "devices.json"))
	h := NewHTTPWithPairing(s, "legacy", store)
	t.Cleanup(h.Close)
	deviceA, tokenA := pairPhone(t, h, store, "Phone A")
	_, tokenB := pairPhone(t, h, store, "Phone B")
	rows, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attach := func(id, token string) (MoshInfo, *fakeProcess) {
		t.Helper()
		w := request(h, "POST", "/v1/sessions/"+id+"/mosh", token, `{"cols":80,"rows":24}`)
		var info MoshInfo
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil {
			t.Fatalf("attach status %d: %s", w.Code, w.Body.String())
		}
		r.mu.Lock()
		p := r.proc
		r.mu.Unlock()
		return info, p
	}
	old, oldProcess := attach(rows[1].ID, tokenA)
	_, otherProcess := attach(rows[2].ID, tokenA)
	for _, token := range []string{tokenB, "legacy"} {
		w := request(h, "POST", "/v1/sessions/"+rows[1].ID+"/mosh", token, `{"cols":80,"rows":24}`)
		if w.Code != 409 || oldProcess.stopped() != 0 {
			t.Fatal("foreign request replaced a live controller")
		}
	}
	// A lost bootstrap response or app termination leaves the phone without its
	// lease ID. Authentication alone must recover its own tracked connection.
	fresh, freshProcess := attach(rows[1].ID, tokenA)
	if old.LeaseID == fresh.LeaseID || oldProcess.stopped() != 1 || otherProcess.stopped() != 0 || freshProcess.stopped() != 0 {
		t.Fatal("reconnect did not isolate the previous connection")
	}
	s.mu.Lock()
	owner := s.leases[fresh.LeaseID].deviceID
	s.mu.Unlock()
	if owner != deviceA {
		t.Fatal("device identity was not recorded during bootstrap")
	}
	// A delayed DELETE from the previous connection must not close the new one.
	if w := request(h, "DELETE", "/v1/leases/"+old.LeaseID, tokenA, ""); w.Code != 404 || freshProcess.stopped() != 0 {
		t.Fatal("late release interfered with replacement")
	}
	if err := store.Revoke(deviceA); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "POST", "/v1/sessions/"+rows[1].ID+"/mosh", tokenA, `{"cols":80,"rows":24}`); w.Code != 401 {
		t.Fatal("revoked device recovered a lease")
	}
	r.mu.Lock()
	args := strings.Join(r.moshArgs, " ")
	r.mu.Unlock()
	if strings.Contains(args, "takeover") {
		t.Fatal("reconnect stole external ownership")
	}
}

func TestHerdrRecoveryStillHonorsExternalController(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	first, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	old := r.proc
	r.mu.Unlock()
	// Herdr remains authoritative if an external client acquires the terminal
	// between cleanup of the old phone connection and the new preflight.
	r.busy = true
	if _, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone"); err != errTerminalBusy {
		t.Fatal(err)
	}
	if old.stopped() != 1 || s.DeleteLease(first.LeaseID) {
		t.Fatal("old lease was not cleaned up")
	}
	r.mu.Lock()
	current := r.proc
	r.mu.Unlock()
	if current != old {
		t.Fatal("busy external terminal spawned another Mosh server")
	}
}

func TestHerdrPairedRecoveryCannotReplaceUnownedLease(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	if _, err := s.Bootstrap(context.Background(), rows[1].ID, 80, 24); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	old := r.proc
	r.mu.Unlock()
	if _, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone"); err != errTerminalBusy {
		t.Fatal(err)
	}
	if old.stopped() != 0 {
		t.Fatal("unowned connection was stopped")
	}
}

// A transport can report Done before Stop has finished its cleanup. Both paths
// must retain the barrier until reconnect can safely request a new controller.
type retiringHerdrProcess struct {
	entered chan struct{}
	finish  chan struct{}
	done    chan struct{}
}

func (p *retiringHerdrProcess) Stop()                 { close(p.entered); close(p.done); <-p.finish }
func (p *retiringHerdrProcess) Done() <-chan struct{} { return p.done }

func TestHerdrReconnectWaitsForConcurrentLeaseCleanup(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	p := &retiringHerdrProcess{entered: make(chan struct{}), finish: make(chan struct{}), done: make(chan struct{})}
	s.mu.Lock()
	s.leases["retiring"] = &lease{sessionID: rows[1].ID, deviceID: "phone", proc: p}
	s.mu.Unlock()
	deleted := make(chan bool, 1)
	go func() { deleted <- s.DeleteLease("retiring") }()
	<-p.entered
	recovered := make(chan error, 1)
	go func() {
		_, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone")
		recovered <- err
	}()
	select {
	case err := <-recovered:
		close(p.finish)
		t.Fatalf("recovery raced pending cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(p.finish)
	if !<-deleted {
		t.Fatal("lease not released")
	}
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
}

func TestHerdrRecoveryAtCapacityPreservesOtherLeases(t *testing.T) {
	r := &mixedRunner{snapshots: map[string]string{"default": herdrFixture, "work": herdrFixture}}
	s := mixedService(t, r)
	rows, _ := s.Sessions(context.Background())
	if _, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone"); err != nil {
		t.Fatal(err)
	}
	others := make([]*fakeProcess, 31)
	s.mu.Lock()
	for i := range others {
		others[i] = &fakeProcess{done: make(chan struct{})}
		s.leases[fmt.Sprint("other-", i)] = &lease{sessionID: "other", deviceID: "phone", proc: others[i]}
	}
	s.mu.Unlock()
	if _, err := s.bootstrapDevice(context.Background(), rows[1].ID, 80, 24, "phone"); err != nil {
		t.Fatal(err)
	}
	for _, p := range others {
		if p.stopped() != 0 {
			t.Fatal("unrelated connection stopped")
		}
	}
}
