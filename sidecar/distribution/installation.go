package distribution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"trill/sidecar/setup"
)

const ownershipMarker = "trill-signed-companion-v1\n"

func InstallationRoot(home string) string {
	return filepath.Join(home, ".local", "share", "trill", "companion")
}

// OpenInstallation refuses to adopt preexisting directories without our marker.
func OpenInstallation(home string, create bool) (string, error) {
	root := InstallationRoot(home)
	_, err := os.Lstat(root)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return "", err
	}
	if missing && !create {
		return "", errors.New("this is not a managed signed installation; use the official installer or keep using scripts/setup-companion.sh")
	}
	if err := setup.PrivateDir(root); err != nil {
		return "", err
	}
	markerPath := filepath.Join(root, ".trill-distribution")
	if missing {
		if err := setup.WritePrivate(markerPath, []byte(ownershipMarker), false); err != nil {
			return "", err
		}
	}
	marker, err := setup.ReadPrivate(markerPath)
	if err != nil || string(marker) != ownershipMarker {
		return "", errors.New("installation directory has no valid Trill distribution ownership marker")
	}
	for _, directory := range []string{"bin", "releases"} {
		if err := setup.PrivateDir(filepath.Join(root, directory)); err != nil {
			return "", err
		}
	}
	return root, nil
}

func StableCLI(home, executable string) string {
	if prefix := HomebrewPrefix(executable); prefix != "" {
		return filepath.Join(prefix, "bin", "trill")
	}
	root := InstallationRoot(home)
	marker, err := setup.ReadPrivate(filepath.Join(root, ".trill-distribution"))
	if err != nil || string(marker) != ownershipMarker {
		return executable
	}
	stable := filepath.Join(root, "bin", "trill")
	resolved, err := filepath.EvalSymlinks(stable)
	if err == nil && resolved == executable {
		return stable
	}
	return executable
}

// HomebrewPrefix recognizes our cask only when Homebrew's stable CLI link
// resolves to this exact executable. Never infer ownership from PATH alone.
func HomebrewPrefix(executable string) string {
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return ""
	}
	caskDirectory := filepath.Dir(filepath.Dir(resolvedExecutable))
	if filepath.Base(caskDirectory) != "trill" || filepath.Base(filepath.Dir(caskDirectory)) != "Caskroom" {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(caskDirectory))
	stableExecutable, err := filepath.EvalSymlinks(filepath.Join(prefix, "bin", "trill"))
	if err != nil || stableExecutable != resolvedExecutable {
		return ""
	}
	return prefix
}

func PreflightLinks(home, root string) error {
	for _, binary := range []string{"trill", "trillctl", "trilld"} {
		if err := checkManagedLink(filepath.Join(root, "bin", binary), "../current/"+binary); err != nil {
			return err
		}
	}
	return checkManagedLink(filepath.Join(home, ".local", "bin", "trill"), filepath.Join(root, "bin", "trill"))
}

func checkManagedLink(path, expected string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	target, err := os.Readlink(path)
	if err != nil || target != expected {
		return fmt.Errorf("refusing to replace foreign/source installation at %s; keep using its installer or explicitly migrate it", path)
	}
	return nil
}

func InstallLinks(home, root string) error {
	if err := PreflightLinks(home, root); err != nil {
		return err
	}
	publicDirectory := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(publicDirectory, 0755); err != nil {
		return err
	}
	info, err := os.Lstat(publicDirectory)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || owner.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("public command directory must be a real user-owned directory without group/world write permission")
	}
	for _, binary := range []string{"trill", "trillctl", "trilld"} {
		if err := createManagedLink(filepath.Join(root, "bin", binary), "../current/"+binary); err != nil {
			return err
		}
	}
	return createManagedLink(filepath.Join(publicDirectory, "trill"), filepath.Join(root, "bin", "trill"))
}

func createManagedLink(path, target string) error {
	if err := checkManagedLink(path, target); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(target, path)
}
