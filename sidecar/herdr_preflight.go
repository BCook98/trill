package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CheckHerdrAttach briefly acquires and releases a direct controller, without
// takeover or terminal input. A busy external controller yields 409 before mosh
// is spawned. Herdr remains authoritative if ownership changes after preflight.
func (ExecRunner) CheckHerdrAttach(ctx context.Context, path string, args []string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = withEnv(os.Environ(), env)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return errUnavailable
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return errUnavailable
	}
	if cmd.Start() != nil {
		in.Close()
		return errUnavailable
	}
	defer func() { in.Close(); cancel(); _ = cmd.Wait() }()
	var frame struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if json.NewDecoder(io.LimitReader(out, 4<<20)).Decode(&frame) != nil {
		return errNotAttachable
	}
	if frame.Type != "terminal.frame" {
		if strings.Contains(frame.Reason, "already has an attached client") {
			return errTerminalBusy
		}
		return errNotAttachable
	}
	if _, err = io.WriteString(in, "{\"type\":\"terminal.release\"}\n"); err != nil {
		return errUnavailable
	}
	in.Close()
	// Drain until the release closes the stream so mosh cannot race our own owner.
	if _, err = io.Copy(io.Discard, io.LimitReader(out, 4<<20)); err != nil {
		return errUnavailable
	}
	if err = cmd.Wait(); err != nil {
		return errUnavailable
	}
	return nil
}
