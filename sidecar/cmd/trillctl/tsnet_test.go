package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"trill/sidecar/setup"
)

func TestEmbeddedEnrollmentRefusesRunningNode(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	nodeDirectory := filepath.Join(stateDirectory, "tsnet")
	if err := setup.PrivateDir(nodeDirectory); err != nil {
		t.Fatal(err)
	}
	unlock, err := setup.LockTSNetState(nodeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = enrollTSNet(context.Background(), stateDirectory, "trill", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "stop this companion") {
		t.Fatalf("enrollment opened an identity already in use: %v", err)
	}
}
