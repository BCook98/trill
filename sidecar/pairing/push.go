package pairing

import (
	"time"
	"trill/sidecar/push"
)

const PushLifetime = 7 * 24 * time.Hour

type PushRegistration struct {
	push.Registration
	ExpiresAt time.Time `json:"expiresAt"`
}

// SetPush is atomic with revocation. nil deletes even when the provider is disabled.
func (s *Store) SetPush(deviceID string, registration *push.Registration) error {
	if registration != nil && !registration.Valid() {
		return ErrFields
	}
	return s.access(true, func(value *state) error {
		for index := range value.Devices {
			device := &value.Devices[index]
			if device.ID != deviceID {
				continue
			}
			if device.RevokedAt != nil {
				return ErrUnauthorized
			}
			device.Push = nil
			if registration != nil {
				device.Push = &PushRegistration{*registration, time.Now().Add(PushLifetime)}
			}
			return nil
		}
		return ErrUnauthorized
	})
}

// PushRegistrations excludes expired registrations and fails closed on unreadable state.
func (s *Store) PushRegistrations() (map[string]PushRegistration, error) {
	result := map[string]PushRegistration{}
	// Avoid rewriting the credentials file on every event; expired values are
	// excluded immediately and removed on the next registration/revocation write.
	err := s.access(false, func(value *state) error {
		for _, device := range value.Devices {
			if device.RevokedAt == nil && device.Push != nil && time.Now().Before(device.Push.ExpiresAt) {
				result[device.ID] = *device.Push
			}
		}
		return nil
	})
	return result, err
}

// Compare before deleting so an old APNs rejection cannot erase a rotated token.
func (s *Store) RemovePushToken(deviceID, token string) error {
	return s.access(true, func(value *state) error {
		for index := range value.Devices {
			device := &value.Devices[index]
			if device.ID == deviceID && device.Push != nil && device.Push.Token == token {
				device.Push = nil
			}
		}
		return nil
	})
}
