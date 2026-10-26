package pairing

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "devices.json")
	return NewStore(path), path
}
func issue(t *testing.T, s *Store) (Device, string) {
	t.Helper()
	code, _, err := s.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	d, token, err := s.Redeem(code, "My phone")
	if err != nil {
		t.Fatal(err)
	}
	return d, token
}
func TestStoreLifecycleAndSecrets(t *testing.T) {
	s, path := testStore(t)
	before := time.Now()
	code, expiry, err := s.CreateInvitation(MaxInvitationTTL)
	if err != nil || len(code) != 64 || !expiry.After(before) || expiry.After(time.Now().Add(MaxInvitationTTL)) {
		t.Fatal("invitation creation failed", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), code) {
		t.Fatal("raw invitation persisted")
	}
	d, token, err := NewStore(path).Redeem(code, "My phone 📱")
	if err != nil || len(token) != 64 || len(d.ID) != 32 || d.Name != "My phone 📱" || d.CreatedAt.Before(before) || d.RevokedAt != nil {
		t.Fatal("device creation failed", err)
	}
	if d.ID == token || token == code {
		t.Fatal("credentials reused")
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), code) {
		t.Fatal("raw credential persisted")
	}
	for _, p := range []string{path, path + ".lock"} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("insecure file mode", err)
		}
	}
	if id, err := s.Authenticate(token); err != nil || id != d.ID {
		t.Fatal("authentication failed", err)
	}
	if _, _, err := s.Redeem(code, "Replay"); !errors.Is(err, ErrInvitation) {
		t.Fatal("replay accepted", err)
	}
	devices, err := NewStore(path).Devices()
	if err != nil || len(devices) != 1 || devices[0].ID != d.ID {
		t.Fatal("devices not reloaded", err)
	}
	data, _ := json.Marshal(devices[0])
	var fields map[string]any
	json.Unmarshal(data, &fields)
	for _, key := range []string{"id", "name", "createdAt", "revokedAt"} {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing device field", key)
		}
	}
	if len(fields) != 4 {
		t.Fatal("device leaked private fields")
	}
	if err := NewStore(path).Revoke(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked token accepted", err)
	}
	devices, _ = s.Devices()
	revoked := *devices[0].RevokedAt
	if err := s.Revoke(d.ID); err != nil {
		t.Fatal(err)
	}
	devices, _ = s.Devices()
	if !devices[0].RevokedAt.Equal(revoked) {
		t.Fatal("revocation is not idempotent")
	}
	if err := s.Revoke(strings.Repeat("0", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestValidationExpiryAndDefaults(t *testing.T) {
	s, path := testStore(t)
	for _, ttl := range []time.Duration{0, -time.Second, MaxInvitationTTL + 1} {
		if _, _, err := s.CreateInvitation(ttl); !errors.Is(err, ErrFields) {
			t.Fatal("invalid TTL accepted")
		}
	}
	for _, name := range []string{"", " \t", "a\x00b", "a\nb", "a\x7fb", string([]byte{0xff}), strings.Repeat("n", 201)} {
		if _, _, err := s.Redeem(strings.Repeat("a", 64), name); !errors.Is(err, ErrFields) {
			t.Fatal("invalid name accepted")
		}
	}
	if _, _, err := s.Redeem("bad", "Phone"); !errors.Is(err, ErrFields) {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(strings.Repeat("a", 64), "Phone"); !errors.Is(err, ErrInvitation) {
		t.Fatal(err)
	}
	for _, token := range []string{"", "bad", strings.Repeat("a", 64), strings.Repeat("a", 4097)} {
		if _, err := s.Authenticate(token); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("unknown token accepted")
		}
	}
	code, expiry, err := s.CreateInvitation(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Now().Before(expiry) {
		time.Sleep(time.Millisecond)
	}
	if _, _, err := s.Redeem(code, "Phone"); !errors.Is(err, ErrInvitation) {
		t.Fatal("expired invitation accepted", err)
	}
	// Invalid fields never consume a valid invitation.
	code, _, err = s.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(code, ""); !errors.Is(err, ErrFields) {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(code, "Phone"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	def, err := DefaultPath()
	if err != nil || def != filepath.Join(home, ".config", "trill", "devices.json") {
		t.Fatal("wrong default path")
	}
	if _, _, err := NewStore("").CreateInvitation(time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentRedemptionAndRevocation(t *testing.T) {
	s, path := testStore(t)
	code, _, err := s.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := NewStore(path).Redeem(code, "Phone")
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrInvitation) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("single-use invitation won more or less than once")
	}
	d, token := issue(t, s)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := NewStore(path).Revoke(d.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := s.Authenticate(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revocation lost")
	}
}

// These subprocesses are only copies of the Go test executable. They never run
// sessions, servers, tmux, Xirp, or mosh, and never print bearer/invitation values.
func TestStoreProcessHelper(t *testing.T) {
	if os.Getenv("TRILL_STORE_HELPER") != "1" {
		return
	}
	s := NewStore(os.Getenv("TRILL_STORE_PATH"))
	var err error
	switch os.Getenv("TRILL_STORE_OP") {
	case "redeem":
		_, _, err = s.Redeem(os.Getenv("TRILL_STORE_CODE"), "Child phone")
	case "revoke":
		err = s.Revoke(os.Getenv("TRILL_STORE_ID"))
	default:
		os.Exit(4)
	}
	if errors.Is(err, ErrInvitation) {
		os.Exit(3)
	}
	if err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func childStore(path, op, value string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreProcessHelper$")
	cmd.Env = append(os.Environ(), "TRILL_STORE_HELPER=1", "TRILL_STORE_PATH="+path, "TRILL_STORE_OP="+op, "TRILL_STORE_CODE="+value, "TRILL_STORE_ID="+value)
	return cmd
}
func TestCrossProcessSingleUseAndRevocation(t *testing.T) {
	s, path := testStore(t)
	code, _, err := s.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 8)
	for j := range commands {
		commands[j] = childStore(path, "redeem", code)
		if err := commands[j].Start(); err != nil {
			t.Fatal(err)
		}
	}
	wins := 0
	for _, cmd := range commands {
		err := cmd.Wait()
		if err == nil {
			wins++
		} else {
			var e *exec.ExitError
			if !errors.As(err, &e) || e.ExitCode() != 3 {
				t.Fatal("child store operation failed")
			}
		}
	}
	if wins != 1 {
		t.Fatal("cross-process invitation consumed more or less than once")
	}
	d, token := issue(t, s)
	if err := childStore(path, "revoke", d.ID).Run(); err != nil {
		t.Fatal("child revocation failed", err)
	}
	if _, err := s.Authenticate(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-process revocation not observed")
	}
	devices, err := s.Devices()
	if err != nil || len(devices) != 2 {
		t.Fatal("cross-process state update lost")
	}
}
func TestUnsafeAndBoundedState(t *testing.T) {
	for _, name := range []string{"relative", "unclean", "path length", "state mode", "state symlink", "state hardlink", "lock mode", "lock symlink", "directory", "fifo", "oversized", "corrupt", "null", "version", "parent mode", "ancestor mode"} {
		t.Run(name, func(t *testing.T) {
			s, path := testStore(t)
			issue(t, s)
			switch name {
			case "relative":
				s = NewStore("devices.json")
			case "unclean":
				s = NewStore(filepath.Dir(path) + "/../devices.json")
			case "path length":
				s = NewStore("/" + strings.Repeat("x", 4096))
			case "state mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "lock mode":
				if err := os.Chmod(path+".lock", 0644); err != nil {
					t.Fatal(err)
				}
			case "parent mode":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			case "ancestor mode":
				if err := os.Chmod(filepath.Dir(filepath.Dir(path)), 0777); err != nil {
					t.Fatal(err)
				}
			case "state symlink", "lock symlink", "state hardlink":
				target := path
				if name == "lock symlink" {
					target += ".lock"
				}
				if err := os.Rename(target, target+".saved"); err != nil {
					t.Fatal(err)
				}
				var err error
				if name == "state hardlink" {
					err = os.Link(target+".saved", target)
				} else {
					err = os.Symlink(target+".saved", target)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "directory", "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				if name == "directory" {
					err = os.Mkdir(path, 0600)
				} else {
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			default:
				data := []byte(`{`)
				switch name {
				case "oversized":
					data = []byte(strings.Repeat("x", maxStateSize+1))
				case "null":
					data = []byte("null")
				case "version":
					data = []byte(`{"version":2}`)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Devices(); !errors.Is(err, ErrStore) {
				t.Fatal("unsafe state accepted", err)
			}
			if _, _, err := s.CreateInvitation(time.Minute); !errors.Is(err, ErrStore) {
				t.Fatal("unsafe state overwritten", err)
			}
		})
	}
}
func TestInvitationCapacityAndPruning(t *testing.T) {
	s, path := testStore(t)
	for j := 0; j < maxInvitations; j++ {
		if _, _, err := s.CreateInvitation(time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateInvitation(time.Minute); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded invitations")
	}
	// Expired invitations may be pruned without losing devices or growing state.
	v, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	for j := range v.Invitations {
		v.Invitations[j].ExpiresAt = time.Now().Add(-time.Second)
	}
	if err := replaceState(path, v); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateInvitation(time.Minute); err != nil {
		t.Fatal(err)
	}
	v, err = readState(path)
	if err != nil || len(v.Invitations) != 1 {
		t.Fatal("expired invitations not pruned")
	}
}

func TestConcurrentReadersAndAtomicWriters(t *testing.T) {
	s, path := testStore(t)
	d, token := issue(t, s)
	var wg sync.WaitGroup
	for j := 0; j < 8; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				if id, err := NewStore(path).Authenticate(token); err != nil || id != d.ID {
					t.Error("reader observed incomplete state", err)
				}
			}
		}()
	}
	for j := 0; j < 8; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := NewStore(path)
			code, _, err := other.CreateInvitation(time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			device, _, err := other.Redeem(code, "Other phone")
			if err != nil {
				t.Error(err)
				return
			}
			if err := other.Revoke(device.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	devices, err := s.Devices()
	if err != nil || len(devices) != 9 {
		t.Fatal("atomic writer lost devices", err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".devices-*"))
	if err != nil || len(temps) != 0 {
		t.Fatal("temporary credential files retained")
	}
}
