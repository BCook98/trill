//go:build linux || darwin

package demoterm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Run is the demo terminal's PTY child entry point. socket must name the parent's
// private Unix HTTP socket, and session is an opaque parent-owned identifier.
// Run requires exclusive access to a PTY input descriptor. It restores termios
// and disables bracketed paste before returning, including cancellation/errors.
// Ctrl-D returns nil; context cancellation returns ctx.Err(). A failed initial
// snapshot, invalid arguments, PTY failure, or output failure returns an error.
// Later IPC failures are displayed and polled through; submissions are never
// retried, even when their response is lost. No command is interpreted here.
func Run(ctx context.Context, socket, session string, in *os.File, out io.Writer) (err error) {
	if ctx == nil || in == nil || out == nil {
		return errors.New("demo terminal requires context, input, and output")
	}
	if !filepath.IsAbs(socket) || strings.ContainsRune(socket, 0) {
		return errors.New("demo terminal requires an absolute Unix socket path")
	}
	if !validSession(session) {
		return errors.New("invalid demo terminal session")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	restore, err := rawMode(in)
	if err != nil {
		return err
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore demo terminal: %w", restoreErr))
		}
		_, writeErr := io.WriteString(out, "\x1b[?2004l\x1b[?25h\r\n")
		if writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	columns, rows, err := terminalSize(in)
	if err != nil {
		return fmt.Errorf("read demo terminal dimensions: %w", err)
	}
	if _, err := io.WriteString(out, "\x1b[?2004h"); err != nil {
		return err
	}
	return run(ctx, socket, session, in, out, resize, columns, rows)
}

func run(ctx context.Context, socket, session string, in *os.File, out io.Writer, resize <-chan os.Signal, columns, rows int) error {
	ctx, cancel := context.WithCancel(ctx)
	requests, results := make(chan request, 1), make(chan result, 1)
	client := newClient(socket, session)
	var worker sync.WaitGroup
	worker.Add(1)
	go func() { defer worker.Done(); client.worker(ctx, requests, results) }()
	defer func() { cancel(); worker.Wait() }()
	active := true
	requests <- request{kind: "poll"}
	ready, resizePending, diagnostic := false, true, false
	var pending *string
	var state snapshot
	var e editor
	var decoder decoder
	var keyLog []string
	recordKey := func(description string) {
		if len(description) > 256 {
			description = description[:256]
		}
		keyLog = append(keyLog, description)
		if len(keyLog) > 8 {
			keyLog = keyLog[len(keyLog)-8:]
		}
	}
	var nextPoll, nextResize time.Time
	note, status := "", "Connecting to demo…"
	dirty := true
	var input [4096]byte

	handle := func(keys []key) bool {
		for _, k := range keys {
			dirty = true
			if k.name == "exit" {
				return false
			}
			if k.name == "diagnostics" {
				diagnostic = !diagnostic
				if diagnostic {
					note = "Key diagnostics ON; Ctrl-] leaves diagnostics."
					keyLog = nil
					recordKey(fmt.Sprintf("Terminal: %d columns × %d rows", columns, rows))
				} else {
					note = "Key diagnostics OFF."
				}
				continue
			}
			if diagnostic {
				note = fmt.Sprintf("Key: %s | %d columns × %d rows", keyDescription(k), columns, rows)
				recordKey(note)
				continue
			}
			switch k.name {
			case "enter":
				switch {
				case !ready:
					note = "Waiting for the demo connection."
				case e.overflow:
					note = "Input exceeded 8 KiB. Ctrl-C clears it; nothing was submitted."
				case !state.Live:
					note = "Session is stopped; Ctrl-D exits."
				case pending != nil || (active && status == "Submitting…"):
					note = "Waiting for command result; input was not submitted."
				case strings.TrimSpace(string(e.text)) == "":
					note = "Enter a demo command, or Ctrl-D to exit."
				default:
					line := string(e.text)
					pending = &line
					e.clear()
					note = "Command queued."
				}
			case "interrupt":
				e.clear()
				note = "Input cleared (Ctrl-C)."
			case "note":
				note = k.text
			case "paste-start":
				note = "Pasting literal text; press Return after paste to submit."
			case "paste-end":
				if e.overflow {
					note = "Paste exceeded 8 KiB. Ctrl-C clears it; nothing was submitted."
				} else {
					note = "Paste ready. Return submits; Ctrl-C clears."
				}
			default:
				if editNote := e.edit(k); editNote != "" {
					note = editNote
				}
			}
		}
		return true
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resize:
			newColumns, newRows, err := terminalSize(in)
			if err != nil {
				return fmt.Errorf("resize demo terminal: %w", err)
			}
			if newColumns != columns || newRows != rows {
				columns, rows = newColumns, newRows
				resizePending, dirty = true, true
				if diagnostic {
					note = fmt.Sprintf("Resize: %d columns × %d rows", columns, rows)
					recordKey(note)
				}
			}
		case response := <-results:
			oldNote, oldStatus := note, status
			active = false
			if response.err != nil {
				if !ready {
					return response.err
				}
				switch response.request.kind {
				case "submit":
					note = "Command not confirmed. Check output before retrying; it was not replayed."
				case "resize":
					note = "Resize not confirmed; waiting for connection."
					resizePending = true
					nextResize = time.Now().Add(time.Second)
				default:
					note = "Demo connection unavailable; reconnecting…"
				}
			} else {
				ready = true
				if state != response.snapshot {
					state, dirty = response.snapshot, true
				}
				if response.request.kind == "submit" || strings.HasPrefix(note, "Demo connection unavailable") || (response.request.kind == "resize" && strings.HasPrefix(note, "Resize not confirmed")) {
					note = ""
				}
			}
			status = ""
			nextPoll = time.Now().Add(250 * time.Millisecond)
			dirty = dirty || oldNote != note || oldStatus != status
		default:
		}
		if !active && ready {
			var next request
			switch {
			case pending != nil:
				next = request{kind: "submit", line: *pending}
				pending = nil
				status = "Submitting…"
			case resizePending && !time.Now().Before(nextResize):
				next = request{kind: "resize", columns: columns, rows: rows}
				resizePending = false
			case !time.Now().Before(nextPoll):
				next = request{kind: "poll"}
			}
			if next.kind != "" {
				requests <- next
				active = true
				if next.kind == "submit" {
					dirty = true
				}
			}
		}
		if !handle(decoder.flush(time.Now())) {
			return nil
		}
		if dirty {
			footer := "Demo · Return submits · Ctrl-D exits · Ctrl-] keys"
			if !state.Live && ready {
				footer = "Demo · Session stopped · Ctrl-D exits"
			}
			if note != "" {
				footer = note
			}
			if status != "" {
				footer = status
			}
			visible := state
			if diagnostic {
				visible.Text += "\n[Key diagnostics]\n" + strings.Join(keyLog, "\n")
			}
			if _, err := io.WriteString(out, render(visible, &e, columns, rows, footer)); err != nil {
				return err
			}
			dirty = false
		}
		available, err := readable(in)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for demo terminal input: %w", err)
		}
		if !available {
			continue
		}
		n, err := syscall.Read(int(in.Fd()), input[:])
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read demo terminal input: %w", err)
		}
		if n == 0 {
			return nil
		}
		if !handle(decoder.feed(input[:n], time.Now())) {
			return nil
		}
	}
}
