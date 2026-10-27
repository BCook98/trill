package distribution

import (
	"os"
	"path/filepath"
	"testing"

	"trill/sidecar/setup"
)

func TestInstallationOwnershipAndStablePaths(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInstallation(home, false); err == nil {
		t.Fatal("adopted missing installation")
	}
	root, err := OpenInstallation(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallLinks(home, root); err != nil {
		t.Fatal(err)
	}
	if err := InstallLinks(home, root); err != nil {
		t.Fatal(err)
	}
	if err := setup.PrivateDir(filepath.Join(root, "releases", "fixture")); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "releases", "fixture", "trill")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := setup.SetCurrentRelease(root, "releases/fixture"); err != nil {
		t.Fatal(err)
	}
	if got := StableCLI(home, executable); got != filepath.Join(root, "bin", "trill") {
		t.Fatalf("unstable configured path %s", got)
	}
	if got := StableCLI(home, "/source/trill"); got != "/source/trill" {
		t.Fatal("adopted unrelated executable")
	}
	public := filepath.Join(home, ".local", "bin", "trill")
	if err := os.Remove(public); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/source/trill", public); err != nil {
		t.Fatal(err)
	}
	if err := InstallLinks(home, root); err == nil {
		t.Fatal("overwrote source installation")
	}
}

func TestInstallationRefusesForeignDirectoryAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		home := t.TempDir()
		root := InstallationRoot(home)
		if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
			t.Fatal(err)
		}
		var err error
		if symlink {
			err = os.Symlink(t.TempDir(), root)
		} else {
			err = os.Mkdir(root, 0700)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenInstallation(home, true); err == nil {
			t.Fatal("adopted foreign installation")
		}
	}
}
