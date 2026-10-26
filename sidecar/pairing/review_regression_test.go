package pairing

import (
	"errors"
	"testing"
	"time"
)

func TestRevokeAfterClockRollbackPreservesStoreAndOtherDevices(t *testing.T) {
	store, path := testStore(t)
	device, token := issue(t, store)
	other, otherToken := issue(t, store)

	// Model issuance before a backward wall-clock correction. Change only the
	// temporary fixture, never the system clock or a real pairing store.
	issuedAt := time.Now().UTC().Add(time.Hour)
	if err := store.access(true, func(v *state) error {
		for i := range v.Devices {
			if v.Devices[i].ID == device.ID {
				v.Devices[i].CreatedAt = issuedAt
			}
		}
		return nil
	}); err != nil {
		t.Fatal("prepare pre-correction issuance:", err)
	}
	if id, err := NewStore(path).Authenticate(token); err != nil || id != device.ID {
		t.Fatal("credential must authenticate before revocation:", err)
	}

	if err := NewStore(path).Revoke(device.ID); err != nil {
		t.Fatal("revoke after backward clock correction:", err)
	}
	devices, err := NewStore(path).Devices()
	if err != nil {
		t.Fatal("revocation made persisted state unreadable:", err)
	}
	if len(devices) != 2 {
		t.Fatalf("revocation changed device count: got %d, want 2", len(devices))
	}
	var revokedAt *time.Time
	for _, d := range devices {
		if d.ID == device.ID {
			revokedAt = d.RevokedAt
		}
		if d.ID == other.ID && d.RevokedAt != nil {
			t.Fatal("revocation affected another device")
		}
	}
	if revokedAt == nil || revokedAt.Before(issuedAt) {
		t.Fatal("persisted revocation predates issuance")
	}
	if _, err := NewStore(path).Authenticate(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked credential must fail immediately with ErrUnauthorized, got %v", err)
	}
	if id, err := NewStore(path).Authenticate(otherToken); err != nil || id != other.ID {
		t.Fatal("another device lost authentication:", err)
	}

	if err := NewStore(path).Revoke(device.ID); err != nil {
		t.Fatal("repeated revocation failed:", err)
	}
	devices, err = NewStore(path).Devices()
	if err != nil {
		t.Fatal("read after repeated revocation:", err)
	}
	for _, d := range devices {
		if d.ID == device.ID && (d.RevokedAt == nil || !d.RevokedAt.Equal(*revokedAt)) {
			t.Fatal("repeated revocation changed its timestamp")
		}
	}

	// An unaffected credential is not enough: new enrollment must remain usable.
	code, _, err := NewStore(path).CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal("new invitation blocked after revocation:", err)
	}
	enrolled, enrolledToken, err := NewStore(path).Redeem(code, "Replacement phone")
	if err != nil {
		t.Fatal("new enrollment blocked after revocation:", err)
	}
	if id, err := NewStore(path).Authenticate(enrolledToken); err != nil || id != enrolled.ID {
		t.Fatal("new credential failed authentication:", err)
	}
}
