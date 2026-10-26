package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Fake builds and a temporary HOME keep installer verification away from real
// user binaries, configuration, credentials and services.
type installerFixture struct {
	home, bin, tools, script, log string
}

func newInstallerFixture(t *testing.T) installerFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS installer")
	}
	_, source, _, _ := runtime.Caller(0)
	dir := t.TempDir()
	f := installerFixture{
		home: filepath.Join(dir, "home"), bin: filepath.Join(dir, "custom private tools"),
		tools: filepath.Join(dir, "fake-tools"), log: filepath.Join(dir, "invocations"),
		script: filepath.Join(filepath.Dir(source), "../../../scripts/setup-companion.sh"),
	}
	for _, path := range []string{f.home, f.tools} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fakeGo := `#!/bin/bash
set -eu
printf 'build\n' >> "$TRILL_TEST_LOG"
if [[ "${TRILL_TEST_FAIL:-}" == "$5" ]]; then exit 31; fi
[[ "$1" == build && "$2" == -trimpath && "$3" == -o ]]
cat > "$4" <<'BINARY'
#!/bin/bash
set -eu
[[ "$1" == setup && "$2" == --help && "$#" == 2 ]]
printf 'setup --help\n' >> "$TRILL_TEST_LOG"
printf 'Usage: trill setup [options]\n'
BINARY
`
	if err := os.WriteFile(filepath.Join(f.tools, "go"), []byte(fakeGo), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f installerFixture) run(t *testing.T, onPath bool, failPackage string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", f.script, "--help")
	path := f.tools + ":/usr/bin:/bin"
	if onPath {
		path += ":" + filepath.Join(f.home, ".local/bin")
	}
	cmd.Env = []string{"HOME=" + f.home, "PATH=" + path, "TRILL_BIN_DIR=" + f.bin,
		"TRILL_TEST_LOG=" + f.log, "TRILL_TEST_FAIL=" + failPackage}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallerCustomDirectoryAndManagedLink(t *testing.T) {
	f := newInstallerFixture(t)
	for i := 0; i < 2; i++ {
		out, err := f.run(t, true, "")
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if strings.Contains(out, "export PATH=") {
			t.Fatal("unnecessary PATH change")
		}
	}
	public := filepath.Join(f.home, ".local/bin")
	entries, err := os.ReadDir(public)
	if err != nil || len(entries) != 1 || entries[0].Name() != "trill" {
		t.Fatalf("unexpected public commands: %v %v", entries, err)
	}
	target, err := os.Readlink(filepath.Join(public, "trill"))
	if err != nil || target != filepath.Join(f.bin, "trill") {
		t.Fatalf("wrong public link: %s %v", target, err)
	}
	for _, name := range []string{"trill", "trillctl", "trilld"} {
		info, err := os.Stat(filepath.Join(f.bin, name))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("missing private executable %s: %v", name, err)
		}
	}
	log, err := os.ReadFile(f.log)
	if err != nil || string(log) != strings.Repeat("build\nbuild\nsetup --help\n", 2) {
		t.Fatalf("unexpected installer actions: %q %v", log, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".config")); !os.IsNotExist(err) {
		t.Fatal("installer touched configuration")
	}
}

func TestInstallerMissingPathAndDanglingManagedLink(t *testing.T) {
	f := newInstallerFixture(t)
	public := filepath.Join(f.home, ".local/bin")
	if err := os.MkdirAll(public, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.bin, "trill"), filepath.Join(public, "trill")); err != nil {
		t.Fatal(err)
	}
	out, err := f.run(t, false, "")
	if err != nil || !strings.Contains(out, "export PATH=") || !strings.Contains(out, f.bin+"/trill") {
		t.Fatalf("missing PATH fallback: %v\n%s", err, out)
	}
}

func TestInstallerRefusesForeignPublicDestinations(t *testing.T) {
	for _, kind := range []string{"file", "directory", "link"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstallerFixture(t)
			public := filepath.Join(f.home, ".local/bin")
			if err := os.MkdirAll(public, 0700); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(public, "trill")
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(dest, []byte("foreign"), 0600)
			case "directory":
				err = os.Mkdir(dest, 0700)
			case "link":
				err = os.Symlink("/missing/foreign/trill", dest)
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.run(t, true, "")
			if err == nil || !strings.Contains(out, "Refusing to overwrite") {
				t.Fatalf("accepted foreign destination: %v\n%s", err, out)
			}
			if _, err := os.Stat(f.log); !os.IsNotExist(err) {
				t.Fatal("built despite destination collision")
			}
		})
	}
}

func TestInstallerBuildFailureKeepsAllInstalledBinaries(t *testing.T) {
	for _, pkg := range []string{"./cmd/trilld", "./cmd/trillctl"} {
		t.Run(filepath.Base(pkg), func(t *testing.T) {
			f := newInstallerFixture(t)
			if out, err := f.run(t, true, ""); err != nil {
				t.Fatalf("initial install: %v\n%s", err, out)
			}
			for _, name := range []string{"trill", "trillctl", "trilld"} {
				if err := os.WriteFile(filepath.Join(f.bin, name), []byte("old "+name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := f.run(t, true, pkg); err == nil {
				t.Fatalf("accepted failed build: %s", out)
			}
			for _, name := range []string{"trill", "trillctl", "trilld"} {
				data, err := os.ReadFile(filepath.Join(f.bin, name))
				if err != nil || string(data) != "old "+name {
					t.Fatalf("published incomplete build for %s: %v", name, err)
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(f.bin, ".build.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("build staging not cleaned: %v %v", leftovers, err)
			}
		})
	}
}
