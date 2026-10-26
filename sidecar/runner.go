package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errCommand = errors.New("local command failed")

// Runner is the only path from HTTP requests to local commands. Arguments never
// pass through a shell. Output and errors from commands are never returned to HTTP.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	StartMosh(ctx context.Context, name string, args []string, env []string) (MoshProcess, string, error)
}

type MoshProcess interface {
	Stop()
	Done() <-chan struct{}
}

type ExecRunner struct{}

var connectLine = regexp.MustCompile(`^MOSH CONNECT ([0-9]{1,5}) ([A-Za-z0-9+/=]{20,})$`)
var detachedLine = regexp.MustCompile(`^\[mosh-server detached, pid = ([0-9]+)\]$`)

type execMosh struct {
	pid  int
	pgid int
	done chan struct{}
	once sync.Once
}

func (p *execMosh) Done() <-chan struct{} { return p.done }
func (p *execMosh) owned() bool {
	pgid, err := syscall.Getpgid(p.pid)
	return err == nil && pgid == p.pgid
}
func (p *execMosh) Stop() {
	p.once.Do(func() {
		if p.owned() {
			_ = syscall.Kill(p.pid, syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				if p.owned() {
					_ = syscall.Kill(p.pid, syscall.SIGKILL)
					// Sending a signal is not confirmation of exit. Keep the
					// lease retirement barrier until the owned process is gone.
					<-p.done
				}
			}
		}
	})
}

func (ExecRunner) StartMosh(ctx context.Context, name string, args []string, env []string) (MoshProcess, string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = withEnv(os.Environ(), env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", errCommand
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", errCommand
	}
	if err := cmd.Start(); err != nil {
		return nil, "", errCommand
	}
	pgid := cmd.Process.Pid
	stopLaunch := func() (MoshProcess, string, error) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, "", errCommand
	}
	connection := make(chan string, 1)
	detached := make(chan int, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 1024), 4096)
		for s.Scan() {
			v := strings.TrimSpace(s.Text())
			if connectLine.MatchString(v) {
				connection <- v
				return
			}
		}
		connection <- ""
	}()
	go func() {
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 1024), 4096)
		for s.Scan() {
			m := detachedLine.FindStringSubmatch(strings.TrimSpace(s.Text()))
			if len(m) == 2 {
				pid, _ := strconv.Atoi(m[1])
				detached <- pid
				return
			}
		}
		detached <- 0
	}()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	var line string
	var pid int
	for line == "" || pid == 0 {
		select {
		case v := <-connection:
			if v == "" {
				return stopLaunch()
			}
			line = v
		case v := <-detached:
			if v <= 0 {
				return stopLaunch()
			}
			pid = v
		case <-ctx.Done():
			return stopLaunch()
		case <-timer.C:
			return stopLaunch()
		}
	}
	// Mosh forks after writing both lines. Reap the launcher only after both
	// pipe readers are finished; Wait closes the pipes supplied by StdoutPipe.
	if err := cmd.Wait(); err != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return nil, "", errCommand
	}
	p := &execMosh{pid: pid, pgid: pgid, done: make(chan struct{})}
	if !p.owned() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return nil, "", errCommand
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer close(p.done)
		for range ticker.C {
			if !p.owned() {
				return
			}
		}
	}()
	return p, line, nil
}

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
