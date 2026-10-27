package pairing

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"trill/sidecar/push"
)

func pushPreference() push.Registration {
	return push.Registration{Token: strings.Repeat("a", 64), Environment: "sandbox", ComputerID: "11111111-1111-4111-8111-111111111111", Attention: true}
}
func TestPushPersistenceRotationRevocationAndExpiry(t *testing.T) {
	store, path := testStore(t)
	device, _ := issue(t, store)
	other, _ := issue(t, store)
	registration := pushPreference()
	if err := store.SetPush(device.ID, &registration); err != nil {
		t.Fatal(err)
	}
	registrations, err := NewStore(path).PushRegistrations()
	if err != nil || registrations[device.ID].Token != registration.Token || len(registrations) != 1 {
		t.Fatal("registration not persisted/scoped")
	}
	devices, _ := store.Devices()
	public, _ := json.Marshal(devices)
	if strings.Contains(string(public), registration.Token) {
		t.Fatal("token exposed through device listing")
	}
	rotated := registration
	rotated.Token = strings.Repeat("b", 64)
	if err := store.SetPush(device.ID, &rotated); err != nil {
		t.Fatal(err)
	}
	if err := store.RemovePushToken(device.ID, registration.Token); err != nil {
		t.Fatal(err)
	}
	registrations, _ = store.PushRegistrations()
	if registrations[device.ID].Token != rotated.Token {
		t.Fatal("stale failure removed rotated token")
	}
	if err := store.SetPush(other.ID, &registration); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(device.ID); err != nil {
		t.Fatal(err)
	}
	registrations, _ = store.PushRegistrations()
	if len(registrations) != 1 || registrations[other.ID].Token == "" {
		t.Fatal("revocation leaked or erased other registration")
	}
	if err := store.SetPush(device.ID, &registration); err != ErrUnauthorized {
		t.Fatal("revoked device registered")
	}
	if err := store.access(true, func(value *state) error {
		for index := range value.Devices {
			if value.Devices[index].Push != nil {
				value.Devices[index].Push.ExpiresAt = time.Now().Add(-time.Second)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registrations, _ = store.PushRegistrations()
	raw, _ := os.ReadFile(path)
	if len(registrations) != 0 || strings.Contains(string(raw), `"push"`) {
		t.Fatal("expired registration retained")
	}
}
func TestPushDeleteAndInvalidRegistration(t *testing.T) {
	store, _ := testStore(t)
	device, _ := issue(t, store)
	registration := pushPreference()
	registration.ComputerID = "private filename"
	if store.SetPush(device.ID, &registration) != ErrFields {
		t.Fatal("invalid routing accepted")
	}
	registration = pushPreference()
	if err := store.SetPush(device.ID, &registration); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPush(device.ID, nil); err != nil {
		t.Fatal(err)
	}
	registrations, _ := store.PushRegistrations()
	if len(registrations) != 0 {
		t.Fatal("delete failed")
	}
}
