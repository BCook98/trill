// Package demoapp owns the explicitly selected demo sandbox. It never manages
// the user's installed companion, providers, launch agents, or tmux servers.
package demoapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const markerName = "demo-owner.json"
const lockName = "demo.lock"
const kind = "trill-demo-v1"

type owner struct {
	Kind string `json:"kind"`
	Root string `json:"root"`
	ID   string `json:"id"`
	UID  int    `json:"uid"`
}
type manifest struct {
	Kind          string   `json:"kind"`
	Running       bool     `json:"running"`
	Address       string   `json:"address"`
	Name          string   `json:"name"`
	Profile       string   `json:"profile"`
	Fingerprint   string   `json:"fingerprint"`
	PreviewPort   int      `json:"previewPort"`
	PID           int      `json:"pid"`
	State         any      `json:"state,omitempty"`
	RuntimeFaults []string `json:"runtimeFaults"`
}

func statePath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return "", errors.New("--state-dir must be a clean absolute path to a dedicated demo directory")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	root := filepath.Join(parent, filepath.Base(path))
	if len(filepath.Join(root, "control.sock")) > 100 {
		return "", errors.New("demo state path is too long for a Unix socket; choose a shorter directory")
	}
	return root, nil
}
func privateInfo(path string, directory bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || st.IsDir() != directory || (!directory && !st.Mode().IsRegular()) || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("unsafe demo path permissions/type: %s", path)
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(raw.Uid) != os.Getuid() {
		return errors.New("demo path is not owned by this user")
	}
	return nil
}
func openState(path string, create bool) (string, error) {
	root, err := statePath(path)
	if err != nil {
		return "", err
	}
	if create {
		if err = os.Mkdir(root, 0700); err == nil {
			nonce := make([]byte, 16)
			if _, err = rand.Read(nonce); err != nil {
				return "", err
			}
			if err = writeNew(filepath.Join(root, markerName), owner{kind, root, hex.EncodeToString(nonce), os.Getuid()}); err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	if err = privateInfo(root, true); err != nil {
		return "", err
	}
	var mark owner
	if err = readPrivate(filepath.Join(root, markerName), &mark); err != nil {
		return "", fmt.Errorf("not an owned demo directory: %w", err)
	}
	if mark.Kind != kind || mark.Root != root || mark.UID != os.Getuid() || len(mark.ID) != 32 {
		return "", errors.New("demo ownership marker does not match this directory")
	}
	return root, nil
}
func writeNew(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func writeAtomic(path string, v any) error {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	tmp := path + "." + hex.EncodeToString(nonce) + ".tmp"
	defer os.Remove(tmp)
	if err := writeNew(tmp, v); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func readPrivate(path string, v any) error {
	if err := privateInfo(path, false); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("demo metadata too large")
	}
	return json.Unmarshal(b, v)
}
func stateLock(root string) (*os.File, error) {
	p := filepath.Join(root, lockName)
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	if err = privateInfo(p, false); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("demo is already running or being reset")
	}
	return f, nil
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func removeSocket(root string) error {
	p := filepath.Join(root, "control.sock")
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace a non-socket control path")
	}
	return os.Remove(p)
}
func listenControl(root string) (net.Listener, error) {
	if err := removeSocket(root); err != nil {
		return nil, err
	}
	p := filepath.Join(root, "control.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(p, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
func resetState(root string) error {
	lock, err := stateLock(root)
	if err != nil {
		return err
	}
	defer unlock(lock)
	// Persist an expired tombstone before deleting anything. A failed explicit
	// reset cannot revive old uploads/credentials on the next server start.
	if err = writeAtomic(filepath.Join(root, "retention.json"), retentionState{time.Unix(1, 0)}); err != nil {
		return err
	}
	return clearData(root)
}
