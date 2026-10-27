package sidecar

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

var errCommand = errors.New("local command failed")

// Runner is the only path from HTTP requests to local commands. Arguments never
// pass through a shell. Output and errors from commands are never returned to HTTP.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type TerminalProcess interface {
	Stop()
	Done() <-chan struct{}
}
type ExecRunner struct{}

func withEnv(base, overrides []string) []string {
	keys := make(map[string]bool, len(overrides))
	for _, v := range overrides {
		if key, _, ok := strings.Cut(v, "="); ok {
			keys[key] = true
		}
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, v := range base {
		key, _, ok := strings.Cut(v, "=")
		if !ok || !keys[key] {
			result = append(result, v)
		}
	}
	return append(result, overrides...)
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.RunWithEnv(ctx, name, args, nil)
}
func (ExecRunner) RunWithEnv(ctx context.Context, name string, args, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = withEnv(os.Environ(), env)
	var out limitWriter
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.exceeded {
		return nil, errCommand
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	bytes.Buffer
	exceeded bool
}

const maxCommandOutput = 4 << 20

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > maxCommandOutput {
		w.exceeded = true
		return 0, errCommand
	}
	return w.Buffer.Write(p)
}
