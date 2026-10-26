// Package pairing stores single-use invitations and revocable device credentials.
// Only hashes of the random credentials are persisted. Store instances and other
// processes coordinate through a stable flock file, never the replaced JSON inode.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxInvitationTTL = 10 * time.Minute
	maxStateSize     = 1 << 20
	maxDevices       = 1024
	maxInvitations   = 128
)

var (
	ErrInvitation   = errors.New("invalid or expired invitation")
	ErrUnauthorized = errors.New("invalid device credential")
	ErrFields       = errors.New("invalid pairing fields")
	ErrNotFound     = errors.New("device not found")
	ErrStore        = errors.New("pairing state unavailable or unsafe")
	ErrCapacity     = errors.New("pairing state capacity reached")
)

type Device struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt"`
}

type credential struct {
	Device
	TokenHash string `json:"tokenHash"`
}
type invitation struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type state struct {
	Version     int          `json:"version"`
	Devices     []credential `json:"devices"`
	Invitations []invitation `json:"invitations"`
}

type Store struct{ path string }

// DefaultPath is independent of XDG_CONFIG_HOME so setup and daemon agree.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", ErrStore
	}
	return filepath.Join(home, ".config", "trill", "devices.json"), nil
}

// NewStore defers path validation and I/O errors to the requested operation.
// An empty path selects ~/.config/trill/devices.json.
func NewStore(path string) *Store {
	if path == "" {
		path, _ = DefaultPath()
	}
	return &Store{path: path}
}

func ValidCode(code string) bool { return validHex(code, 64) }
func ValidName(name string) bool {
	if len(name) > 200 || !utf8.ValidString(name) || strings.TrimSpace(name) == "" {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func validHex(v string, n int) bool {
	if len(v) != n {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", ErrStore
	}
	return hex.EncodeToString(b), nil
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

func (s *Store) CreateInvitation(ttl time.Duration) (code string, expiresAt time.Time, err error) {
	if ttl <= 0 || ttl > MaxInvitationTTL {
		return "", time.Time{}, ErrFields
	}
	code, err = randomHex(32)
	if err != nil {
		return "", time.Time{}, err
	}
	err = s.access(true, func(v *state) error {
		now := time.Now().UTC()
		live := v.Invitations[:0]
		for _, i := range v.Invitations {
			if now.Before(i.ExpiresAt) {
				live = append(live, i)
			}
		}
		v.Invitations = live
		if len(live) >= maxInvitations {
			return ErrCapacity
		}
		expiresAt = now.Add(ttl)
		v.Invitations = append(v.Invitations, invitation{digest(code), expiresAt})
		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

func (s *Store) Redeem(code, name string) (device Device, token string, err error) {
	if !ValidCode(code) || !ValidName(name) {
		return Device{}, "", ErrFields
	}
	err = s.access(true, func(v *state) error {
		hash := digest(code)
		index := -1
		now := time.Now().UTC()
		for j, i := range v.Invitations {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(i.Hash)) == 1 && now.Before(i.ExpiresAt) {
				index = j
			}
		}
		if index < 0 {
			return ErrInvitation
		}
		if len(v.Devices) >= maxDevices {
			return ErrCapacity
		}
		id, e := randomHex(16)
		if e != nil {
			return e
		}
		token, e = randomHex(32)
		if e != nil {
			return e
		}
		device = Device{ID: id, Name: name, CreatedAt: now}
		v.Devices = append(v.Devices, credential{device, digest(token)})
		v.Invitations = append(v.Invitations[:index], v.Invitations[index+1:]...)
		return nil
	})
	if err != nil {
		return Device{}, "", err
	}
	return device, token, nil
}

func (s *Store) Authenticate(token string) (deviceID string, err error) {
	if !validHex(token, 64) {
		return "", ErrUnauthorized
	}
	err = s.access(false, func(v *state) error {
		hash := digest(token)
		for _, d := range v.Devices {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(d.TokenHash)) == 1 && d.RevokedAt == nil {
				deviceID = d.ID
			}
		}
		if deviceID == "" {
			return ErrUnauthorized
		}
		return nil
	})
	return deviceID, err
}
func (s *Store) Devices() (devices []Device, err error) {
	devices = []Device{}
	err = s.access(false, func(v *state) error {
		for _, d := range v.Devices {
			devices = append(devices, d.Device)
		}
		return nil
	})
	return devices, err
}
func (s *Store) Revoke(id string) error {
	if !validHex(id, 32) {
		return ErrNotFound
	}
	return s.access(true, func(v *state) error {
		for j := range v.Devices {
			if v.Devices[j].ID == id {
				if v.Devices[j].RevokedAt == nil {
					now := time.Now().UTC()
					if now.Before(v.Devices[j].CreatedAt) {
						now = v.Devices[j].CreatedAt
					}
					v.Devices[j].RevokedAt = &now
				}
				return nil
			}
		}
		return ErrNotFound
	})
}

// The parent is canonicalized and must belong to this user without group/other
// write access. Files are opened without following links and must have one link.
// A malicious process running as the same user is outside this trust boundary.
func (s *Store) directory() (string, error) {
	if s == nil || len(s.path) > 4096 || !filepath.IsAbs(s.path) || strings.ContainsRune(s.path, 0) || filepath.Clean(s.path) != s.path || filepath.Base(s.path) == "." || s.path == "/" {
		return "", ErrStore
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", ErrStore
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", ErrStore
	}
	// A writable ancestor would let another user replace our private parent.
	for ancestor := filepath.Dir(dir); ; ancestor = filepath.Dir(ancestor) {
		info, e := os.Stat(ancestor)
		if e != nil || !info.IsDir() {
			return "", ErrStore
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Getuid())) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return "", ErrStore
		}
		if ancestor == "/" {
			break
		}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return "", ErrStore
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return "", ErrStore
	}
	return filepath.Join(dir, filepath.Base(s.path)), nil
}

func secureOpen(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
			err = ErrStore
		}
	}
	if err != nil {
		f.Close()
		return nil, ErrStore
	}
	return f, nil
}

func (s *Store) access(write bool, fn func(*state) error) error {
	path, err := s.directory()
	if err != nil {
		return ErrStore
	}
	lock, err := secureOpen(path+".lock", syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return ErrStore
	}
	defer lock.Close()
	operation := syscall.LOCK_SH
	if write {
		operation = syscall.LOCK_EX
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err = syscall.Flock(int(lock.Fd()), operation|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR || time.Now().After(deadline) {
			return ErrStore
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	v, err := readState(path)
	if err != nil {
		return ErrStore
	}
	if err = fn(&v); err != nil {
		return err
	}
	if write {
		return replaceState(path, v)
	}
	return nil
}
func readState(path string) (state, error) {
	v := state{Version: 1, Devices: []credential{}, Invitations: []invitation{}}
	f, err := secureOpen(path, syscall.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, ErrStore
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStateSize+1))
	if err != nil || len(b) > maxStateSize {
		return v, ErrStore
	}
	v = state{}
	if json.Unmarshal(b, &v) != nil || !validState(v) {
		return v, ErrStore
	}
	return v, nil
}
func validState(v state) bool {
	if v.Version != 1 || len(v.Devices) > maxDevices || len(v.Invitations) > maxInvitations {
		return false
	}
	ids, hashes := map[string]bool{}, map[string]bool{}
	for _, d := range v.Devices {
		if !validHex(d.ID, 32) || !ValidName(d.Name) || !validHex(d.TokenHash, 64) || d.CreatedAt.IsZero() || ids[d.ID] || hashes[d.TokenHash] || d.RevokedAt != nil && d.RevokedAt.Before(d.CreatedAt) {
			return false
		}
		ids[d.ID], hashes[d.TokenHash] = true, true
	}
	for _, i := range v.Invitations {
		if !validHex(i.Hash, 64) || i.ExpiresAt.IsZero() || i.ExpiresAt.After(time.Now().Add(MaxInvitationTTL)) || hashes[i.Hash] {
			return false
		}
		hashes[i.Hash] = true
	}
	return true
}
func replaceState(path string, v state) error {
	b, err := json.Marshal(v)
	if err != nil || len(b) > maxStateSize {
		return ErrStore
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".devices-*")
	if err != nil {
		return ErrStore
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrStore
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return ErrStore
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrStore
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrStore
	}
	return nil
}
