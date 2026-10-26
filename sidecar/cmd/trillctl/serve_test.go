package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServeResolvesSiblingAndPreservesArguments(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private tools")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(private, "trill")
	if err := os.WriteFile(cli, nil, 0700); err != nil {
		t.Fatal(err)
	}
	private, err := filepath.EvalSymlinks(private)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "trill")
	if err := os.Symlink(cli, link); err != nil {
		t.Fatal(err)
	}
	args := []string{"--cert", "/a path/cert.pem", "--name=$(touch unwanted)", "", "--", "literal;argument"}
	original := append([]string(nil), args...)
	failure := errors.New("fake exec failure")
	calls := 0
	err = serve(args, func() (string, error) { return link, nil }, func(path string, argv, env []string) error {
		calls++
		if path != filepath.Join(private, "trilld") {
			t.Fatalf("did not use private sibling: %s", path)
		}
		if !reflect.DeepEqual(argv, append([]string{"trill serve"}, original...)) {
			t.Fatalf("arguments changed: %q", argv)
		}
		if !reflect.DeepEqual(env, os.Environ()) {
			t.Fatal("environment changed")
		}
		return failure
	})
	if calls != 1 || !errors.Is(err, failure) || !strings.Contains(err.Error(), "setup-companion.sh --help") {
		t.Fatalf("exec error not actionable: %v (calls %d)", err, calls)
	}
	if !reflect.DeepEqual(args, original) {
		t.Fatal("modified caller arguments")
	}
}

func TestServeExecutableErrorsDoNotExec(t *testing.T) {
	failure := errors.New("unavailable executable")
	for _, executable := range []func() (string, error){
		func() (string, error) { return "", failure },
		func() (string, error) { return filepath.Join(t.TempDir(), "missing"), nil },
	} {
		err := serve(nil, executable, func(string, []string, []string) error {
			t.Fatal("executed without resolving executable")
			return nil
		})
		if err == nil {
			t.Fatal("accepted unavailable executable")
		}
	}
}
