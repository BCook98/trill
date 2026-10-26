package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabFilesRootFlagValidationBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, socket, root string }{
		{"requires tmux socket", "", root},
		{"absolute directory", filepath.Join(root, "foo.sock"), "relative"},
		{"existing directory", filepath.Join(root, "foo.sock"), filepath.Join(root, "missing")},
		{"directory only", filepath.Join(root, "foo.sock"), file},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousFlags, previousArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
			flag.CommandLine = flag.NewFlagSet("trilld-lab-root-test", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			authPath := filepath.Join(t.TempDir(), "uncreated", "token")
			os.Args = []string{"trilld", "--insecure-loopback", "--token-file", authPath, "--lab-files-root", tc.root}
			if tc.socket != "" {
				os.Args = append(os.Args, "--tmux-socket", tc.socket)
			}
			err := run()
			if err == nil || !strings.Contains(err.Error(), "--lab-files-root") {
				t.Fatal("flag validation", err)
			}
			if _, err := os.Stat(filepath.Dir(authPath)); !os.IsNotExist(err) {
				t.Fatal("invalid flags touched authentication storage", err)
			}
		})
	}
}
