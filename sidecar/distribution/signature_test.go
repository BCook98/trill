package distribution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodesignReceivesLiteralRequirementSource(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign regression requires macOS")
	}
	fixture := filepath.Join(t.TempDir(), "signed-fixture")
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, data, 0700); err != nil {
		t.Fatal(err)
	}
	// Ad hoc signing needs no Apple account or private signing key.
	if output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "io.bcook.trill.fixture", fixture).CombinedOutput(); err != nil {
		t.Fatalf("sign fixture: %v: %s", err, output)
	}
	if err := verifyRequirement(context.Background(), fixture, `identifier "io.bcook.trill.fixture"`); err != nil {
		t.Fatalf("literal requirement did not verify: %v", err)
	}
	if err := verifyRequirement(context.Background(), fixture, `identifier "io.bcook.trill.wrong"`); err == nil {
		t.Fatal("accepted incorrect code identity")
	}
}
