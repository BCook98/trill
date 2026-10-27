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

func TestHomebrewUsesStablePathsAcrossCaskVersions(t *testing.T) {
	prefix, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(prefix, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(prefix, "bin", "trill")
	for _, version := range []string{"2026.10.25,first", "2026.10.25,second"} {
		directory := filepath.Join(prefix, "Caskroom", "trill", version)
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(directory, "trill")
		if err := os.WriteFile(executable, []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(executable, stable); err != nil {
			t.Fatal(err)
		}
		if HomebrewPrefix(executable) != prefix || StableCLI("/unrelated/home", executable) != stable {
			t.Fatal("recorded a version-specific Homebrew path")
		}
		if err := os.Remove(stable); err != nil {
			t.Fatal(err)
		}
		if HomebrewPrefix(executable) != "" {
			t.Fatal("adopted an executable without Homebrew's stable link")
		}
	}
}
