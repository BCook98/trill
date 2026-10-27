package push

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateConfigurationRefusesSharedFilesAndLinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "configuration")
	if err := os.WriteFile(path, []byte("synthetic fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path); err == nil {
		t.Fatal("accepted readable secret")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(link); err == nil {
		t.Fatal("followed secret symlink")
	}
	if _, err := ReadPrivateFile("relative"); err == nil {
		t.Fatal("accepted relative secret")
	}
}
