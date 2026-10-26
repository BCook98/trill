//go:build linux || darwin

package demoterm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func await(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	mu             sync.Mutex
	state          snapshot
	commands       []string
	polls, resizes int
	loseNext       bool
}

func newFixture(t *testing.T) (*fixture, string) {
	f := &fixture{state: snapshot{"Parent transcript survives reconnect", 1, true, 80, 24}}
	path := unixServer(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/terminal":
			f.polls++
		case r.Method == "POST" && r.URL.Path == "/terminal":
			var body struct{ Session, Line string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			f.commands = append(f.commands, body.Line)
			f.state.Text += "\n> " + body.Line
			f.state.Revision++
			if f.loseNext {
				f.loseNext = false
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
		case r.Method == "POST" && r.URL.Path == "/resize":
			var body struct{ Columns, Rows int }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			f.state.Columns, f.state.Rows = body.Columns, body.Rows
			f.resizes++
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeSnapshot(w, f.state)
	})
	return f, path
}

func (f *fixture) counts() (commands, polls, resizes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commands), f.polls, f.resizes
}

func startPipeRun(t *testing.T, socket string) (*os.File, *lockedBuffer, context.CancelFunc, <-chan error) {
	t.Helper()
	in, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, socket, "owned", in, output, nil, 80, 24) }()
	t.Cleanup(func() {
		cancel()
		in.Close()
		input.Close()
	})
	return input, output, cancel, done
}

func waitDone(t *testing.T, done <-chan error, canceled bool) {
	t.Helper()
	select {
	case err := <-done:
		if canceled && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		if !canceled && err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal leaked a worker or blocked on input")
	}
}

func TestRunLiteralPasteSubmitReconnectAndIdleUpdates(t *testing.T) {
	f, socket := newFixture(t)
	input, output, _, done := startPipeRun(t, socket)
	await(t, "initial snapshot and resize", func() bool {
		_, _, resize := f.counts()
		return resize == 1 && strings.Contains(output.String(), "Parent transcript")
	})
	io.WriteString(input, "\x1b[200~demo heading 雪\r\nsecond line\x1b[201~")
	await(t, "paste editor", func() bool { return strings.Contains(output.String(), "Paste ready") })
	if commands, _, _ := f.counts(); commands != 0 {
		t.Fatal("paste executed before Return")
	}
	io.WriteString(input, "\r")
	await(t, "submitted paste", func() bool { commands, _, _ := f.counts(); return commands == 1 })
	f.mu.Lock()
	if f.commands[0] != "demo heading 雪\nsecond line" {
		t.Error(f.commands[0])
	}
	f.state.Text += "\nTask finished while idle"
	f.state.Revision++
	f.mu.Unlock()
	await(t, "idle parent update", func() bool { return strings.Contains(output.String(), "Task finished while idle") })
	io.WriteString(input, "\x04")
	waitDone(t, done, false)
	input2, output2, _, done2 := startPipeRun(t, socket)
	await(t, "reconnected transcript", func() bool { return strings.Contains(output2.String(), "Task finished while idle") })
	if commands, _, _ := f.counts(); commands != 1 {
		t.Fatal("reconnect resubmitted a command")
	}
	io.WriteString(input2, "\x04")
	waitDone(t, done2, false)
}

func TestRunLostResponseIsNotRetried(t *testing.T) {
	f, socket := newFixture(t)
	input, output, _, done := startPipeRun(t, socket)
	await(t, "ready", func() bool { _, _, resize := f.counts(); return resize == 1 })
	f.mu.Lock()
	f.loseNext = true
	f.mu.Unlock()
	io.WriteString(input, "demo task\r")
	await(t, "uncertain command notice", func() bool { return strings.Contains(output.String(), "Command not confirmed") })
	_, pollsBefore, _ := f.counts()
	await(t, "subsequent state polls", func() bool { _, polls, _ := f.counts(); return polls >= pollsBefore+2 })
	if commands, _, _ := f.counts(); commands != 1 {
		t.Fatalf("command replayed %d times", commands)
	}
	if !strings.Contains(output.String(), "> demo task") {
		t.Fatal("committed text did not recover")
	}
	io.WriteString(input, "\x04")
	waitDone(t, done, false)
}

func TestRunDiagnosticsAndCancellation(t *testing.T) {
	f, socket := newFixture(t)
	input, output, cancel, done := startPipeRun(t, socket)
	await(t, "ready", func() bool { _, _, resize := f.counts(); return resize == 1 })
	io.WriteString(input, "\x1dhelp\r\t\x1b[D")
	await(t, "key diagnostic", func() bool { return strings.Contains(output.String(), "Key: left | 80 columns") })
	if commands, _, _ := f.counts(); commands != 0 {
		t.Fatal("diagnostic mode submitted input")
	}
	io.WriteString(input, "\x1dabc\x03")
	await(t, "Ctrl-C notice", func() bool { return strings.Contains(output.String(), "Input cleared") })
	cancel()
	waitDone(t, done, true)
}

func TestRunRestoresPTYOnExitAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "Ctrl-D", true: "cancel"}[canceled], func(t *testing.T) {
			_, socket := newFixture(t)
			master, slave := openPTY(t)
			var saved, after termiosForTest
			if err := ioctl(slave.Fd(), getTermios, unsafe.Pointer(&saved)); err != nil {
				t.Fatal(err)
			}
			output := &lockedBuffer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, socket, "owned", slave, output) }()
			await(t, "raw mode initialized", func() bool { return strings.Contains(output.String(), "Parent transcript") })
			if canceled {
				cancel()
			} else {
				io.WriteString(master, "\x04")
			}
			waitDone(t, done, canceled)
			if err := ioctl(slave.Fd(), getTermios, unsafe.Pointer(&after)); err != nil {
				t.Fatal(err)
			}
			if saved != after {
				t.Fatal("raw terminal mode was not restored")
			}
			if !strings.Contains(output.String(), "\x1b[?2004h") || !strings.Contains(output.String(), "\x1b[?2004l") {
				t.Fatal("bracketed paste mode not restored")
			}
		})
	}
}

func TestRunOversizedPasteIsNeverSubmitted(t *testing.T) {
	f, socket := newFixture(t)
	input, output, _, done := startPipeRun(t, socket)
	await(t, "ready", func() bool { _, _, resize := f.counts(); return resize == 1 })
	io.WriteString(input, "\x1b[200~"+strings.Repeat("x", maxLineBytes+1)+"\x1b[201~\r")
	await(t, "rejected oversized input", func() bool { return strings.Contains(output.String(), "Input exceeded") })
	if commands, _, _ := f.counts(); commands != 0 {
		t.Fatal("truncated paste was submitted")
	}
	io.WriteString(input, "\x03help\r")
	await(t, "cleared input can submit", func() bool { commands, _, _ := f.counts(); return commands == 1 })
	f.mu.Lock()
	if f.commands[0] != "help" {
		t.Error(f.commands[0])
	}
	f.mu.Unlock()
	io.WriteString(input, "\x04")
	waitDone(t, done, false)
}

func TestRunDoesNotRedrawUnchangedIdleSnapshots(t *testing.T) {
	f, socket := newFixture(t)
	input, output, _, done := startPipeRun(t, socket)
	await(t, "ready", func() bool { _, polls, resize := f.counts(); return resize == 1 && polls >= 2 })
	before := output.String()
	_, pollsBefore, _ := f.counts()
	await(t, "idle polls", func() bool { _, polls, _ := f.counts(); return polls >= pollsBefore+2 })
	if output.String() != before {
		t.Fatal("unchanged idle snapshots redrew the terminal")
	}
	io.WriteString(input, "\x04")
	waitDone(t, done, false)
}

func TestRunSIGWINCHPostsActualPTYDimensions(t *testing.T) {
	f, socket := newFixture(t)
	_, slave := openPTY(t)
	output := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, socket, "owned", slave, output) }()
	await(t, "ready", func() bool { _, _, resize := f.counts(); return resize >= 1 })
	size := struct{ Rows, Columns, X, Y uint16 }{Rows: 31, Columns: 92}
	if err := ioctl(slave.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	await(t, "new PTY dimensions", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.state.Columns == 92 && f.state.Rows == 31
	})
	cancel()
	waitDone(t, done, true)
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunRestoresPTYOnOutputAndInitialIPCError(t *testing.T) {
	for _, outputFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "IPC", true: "output"}[outputFails], func(t *testing.T) {
			master, slave := openPTY(t)
			_ = master
			var saved, after termiosForTest
			if err := ioctl(slave.Fd(), getTermios, unsafe.Pointer(&saved)); err != nil {
				t.Fatal(err)
			}
			var output io.Writer = &lockedBuffer{}
			if outputFails {
				output = failedWriter{}
			}
			err := Run(context.Background(), "/tmp/trill-demo-terminal-missing-"+randomOwnedSuffix(t), "owned", slave, output)
			if err == nil {
				t.Fatal("expected startup error")
			}
			if err := ioctl(slave.Fd(), getTermios, unsafe.Pointer(&after)); err != nil {
				t.Fatal(err)
			}
			if saved != after {
				t.Fatal("PTY was not restored after error")
			}
		})
	}
}

func randomOwnedSuffix(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "trill-dt-missing-")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	// Use a unique nonexistent Unix-socket path, not an existing service.
	return strings.TrimPrefix(name, "/tmp/")
}

func TestRunSubmitsDelayedUTF8Intact(t *testing.T) {
	f, socket := newFixture(t)
	input, output, _, done := startPipeRun(t, socket)
	await(t, "ready", func() bool { _, _, resize := f.counts(); return resize == 1 })
	// Deliberately split 雪 (e9 9b aa) across reads with an idle period longer
	// than the Escape-key ambiguity timeout. The submitted command must retain
	// the original character, not replacement characters or an escape warning.
	if _, err := input.Write(append([]byte("demo heading "), 0xe9)); err != nil {
		t.Fatal(err)
	}
	await(t, "partial command rendered", func() bool { return strings.Contains(output.String(), "> demo heading ") })
	time.Sleep(2 * escapeDelay)
	if _, err := input.Write([]byte{0x9b, 0xaa, '\r'}); err != nil {
		t.Fatal(err)
	}
	await(t, "delayed Unicode command submitted", func() bool { commands, _, _ := f.counts(); return commands == 1 })
	f.mu.Lock()
	command := f.commands[0]
	f.mu.Unlock()
	if command != "demo heading 雪" {
		t.Fatalf("submitted %q; want intact UTF-8", command)
	}
	if strings.Contains(output.String(), "Incomplete key") {
		t.Fatal("UTF-8 was reported as an expired key sequence")
	}
	if _, err := input.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done, false)
}
