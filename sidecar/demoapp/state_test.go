package demoapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ownedTestRoot(t *testing.T) string {
	t.Helper()
	parent, err := os.MkdirTemp("", "td-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	root, err := openState(filepath.Join(parent, "s"), true)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func TestOwnershipRejectsExistingAndSymlink(t *testing.T) {
	root := ownedTestRoot(t)
	if _, err := openState(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := openState(filepath.Dir(root), true); err == nil {
		t.Fatal("accepted an unmarked existing directory")
	}
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := openState(alias, false); err == nil {
		t.Fatal("accepted root symlink")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openState(root, false); err == nil {
		t.Fatal("accepted public state")
	}
}
func TestLockAndResetPreserveUnrelatedData(t *testing.T) {
	root := ownedTestRoot(t)
	outside := filepath.Join(filepath.Dir(root), "personal")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Symlink(outside, filepath.Join(root, "link"))
	os.WriteFile(filepath.Join(root, "tls.pem"), []byte("identity"), 0600)
	os.WriteFile(filepath.Join(root, "devices.json"), []byte("private"), 0600)
	lock, err := stateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = resetState(root); err == nil {
		t.Fatal("reset accepted live lock")
	}
	unlock(lock)
	if err = resetState(root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatal("changed unrelated file")
	}
	if _, err = os.Stat(filepath.Join(root, "devices.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pairing not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tls.pem")); string(b) != "identity" {
		t.Fatal("lost TLS identity")
	}
	if _, err = openState(root, false); err != nil {
		t.Fatal(err)
	}
}
func TestRetentionOnlyClearsExpiredOwnedState(t *testing.T) {
	root := ownedTestRoot(t)
	file := filepath.Join(root, "photo.jpg")
	os.WriteFile(file, []byte("sample"), 0600)
	writeAtomic(filepath.Join(root, "retention.json"), retentionState{time.Now()})
	if err := expireStored(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("deleted recent data")
	}
	writeAtomic(filepath.Join(root, "retention.json"), retentionState{time.Now().Add(-2 * time.Hour)})
	if err := expireStored(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("kept expired data")
	}
}
func TestMarkerCannotBeMoved(t *testing.T) {
	root := ownedTestRoot(t)
	var mark owner
	if err := readPrivate(filepath.Join(root, markerName), &mark); err != nil {
		t.Fatal(err)
	}
	mark.Root = "/not/this/directory"
	b, _ := json.Marshal(mark)
	os.WriteFile(filepath.Join(root, markerName), b, 0600)
	if _, err := openState(root, false); err == nil {
		t.Fatal("accepted moved marker")
	}
}

func TestRetentionSurvivesRestartAndFailedCleanup(t *testing.T) {
	root := ownedTestRoot(t)
	old := time.Now().Add(-23 * time.Hour)
	if err := writeAtomic(filepath.Join(root, "retention.json"), retentionState{old}); err != nil {
		t.Fatal(err)
	}
	if err := expireStored(root, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	restored, err := retainedActivity(root)
	if err != nil || !restored.Equal(old) {
		t.Fatal("startup renewed activity", err)
	}
	blocked := filepath.Join(root, "zz-blocked")
	os.Mkdir(blocked, 0700)
	os.WriteFile(filepath.Join(blocked, "photo"), []byte("sample"), 0600)
	os.Chmod(blocked, 0500)
	t.Cleanup(func() { os.Chmod(blocked, 0700) })
	if os.Getuid() != 0 {
		if err = clearData(root); err == nil {
			t.Fatal("expected failed deletion")
		}
		if _, err = os.Stat(filepath.Join(root, "retention.json")); err != nil {
			t.Fatal("lost marker before deletion completed")
		}
	}
	os.Chmod(blocked, 0700)
	if err = clearData(root); err != nil {
		t.Fatal(err)
	}
}
func TestMissingRetentionFailsClosedForExistingFixture(t *testing.T) {
	root := ownedTestRoot(t)
	os.WriteFile(filepath.Join(root, "demo-state.json"), []byte("{}"), 0600)
	if _, err := retainedActivity(root); err == nil {
		t.Fatal("missing marker renewed existing fixture")
	}
}

func TestFailedResetCannotReviveLeftoverUploads(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-denial test requires unprivileged user")
	}
	root := ownedTestRoot(t)
	blocked := filepath.Join(root, "uploads")
	os.Mkdir(blocked, 0700)
	os.WriteFile(filepath.Join(blocked, "photo"), []byte("sample"), 0600)
	os.WriteFile(filepath.Join(root, "demo-state.json"), []byte("{}"), 0600)
	writeAtomic(filepath.Join(root, "retention.json"), retentionState{time.Now()})
	os.Chmod(blocked, 0500)
	t.Cleanup(func() { os.Chmod(blocked, 0700) })
	if err := resetState(root); err == nil {
		t.Fatal("expected failed reset")
	}
	if err := expireStored(root, 24*time.Hour); err == nil {
		t.Fatal("failed reset revived leftover upload")
	}
	os.Chmod(blocked, 0700)
	if err := expireStored(root, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blocked); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired upload remained")
	}
}
