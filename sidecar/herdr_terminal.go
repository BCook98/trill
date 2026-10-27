package sidecar

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const maximumTerminalFrame = 4 << 20

// The controller is a child owned by a normal Trill lease. Closing the socket,
// revoking the device, expiry and shutdown all release the same controller.
type herdrTerminalProcess struct {
	input    io.WriteCloser
	frames   chan []byte
	done     chan struct{}
	cancel   context.CancelFunc
	stopOnce sync.Once
	writeMu  sync.Mutex
	claimMu  sync.Mutex
	claimed  bool
}

func (process *herdrTerminalProcess) Done() <-chan struct{} { return process.done }
func (process *herdrTerminalProcess) Stop() {
	process.stopOnce.Do(func() {
		// EOF releases ownership, even if the child is blocked writing output.
		_ = process.input.Close()
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
			process.cancel()
			<-process.done
		}
	})
}
func (process *herdrTerminalProcess) write(command []byte) error {
	process.writeMu.Lock()
	defer process.writeMu.Unlock()
	_, err := process.input.Write(append(command, '\n'))
	return err
}

func (ExecRunner) StartHerdrTerminal(ctx context.Context, path string, arguments, environment []string) (*herdrTerminalProcess, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(lifetime, path, arguments...)
	command.Env = withEnv(os.Environ(), environment)
	command.Stderr = io.Discard
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, errUnavailable
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, errUnavailable
	}
	process := &herdrTerminalProcess{input: input, frames: make(chan []byte, 8), done: make(chan struct{}), cancel: cancel}
	if command.Start() != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, errUnavailable
	}
	ready := make(chan error, 1)
	go func() {
		defer close(process.done)
		defer close(process.frames)
		defer cancel()
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), maximumTerminalFrame)
		first := true
		for scanner.Scan() {
			frame := append([]byte(nil), scanner.Bytes()...)
			var record struct {
				Type   string `json:"type"`
				Bytes  string `json:"bytes"`
				Reason string `json:"reason"`
			}
			if json.Unmarshal(frame, &record) != nil {
				break
			}
			if record.Type != "terminal.frame" {
				if first {
					if strings.Contains(record.Reason, "already has an attached client") {
						ready <- errTerminalBusy
					} else {
						ready <- errNotAttachable
					}
					first = false
				}
				break
			}
			if _, err := base64.StdEncoding.DecodeString(record.Bytes); err != nil {
				break
			}
			if first {
				ready <- nil
				first = false
			}
			// Bound retained output. A stalled or never-connected phone must not keep
			// a controller forever or accumulate an unbounded transcript in memory.
			timer := time.NewTimer(10 * time.Second)
			select {
			case process.frames <- frame:
				timer.Stop()
			case <-lifetime.Done():
				timer.Stop()
				input.Close()
				_ = command.Wait()
				return
			case <-timer.C:
				cancel()
				input.Close()
				_ = command.Wait()
				return
			}
		}
		if first {
			ready <- errNotAttachable
		}
		input.Close()
		cancel()
		_ = command.Wait()
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err != nil {
			process.Stop()
			return nil, err
		}
		time.AfterFunc(15*time.Second, func() {
			process.claimMu.Lock()
			unclaimed := !process.claimed
			if unclaimed {
				process.claimed = true
			}
			process.claimMu.Unlock()
			if unclaimed {
				process.Stop()
			}
		})
		return process, nil
	case <-ctx.Done():
		process.Stop()
		return nil, ctx.Err()
	case <-timer.C:
		process.Stop()
		return nil, errUnavailable
	}
}

func (h *HTTP) herdrTerminal(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	id := r.PathValue("id")
	if !leasePattern.MatchString(id) {
		writeError(w, 404, "not found")
		return
	}
	h.mu.Lock()
	if h.closed || (requestDevice(r) != "" && h.owners[id] != requestDevice(r)) {
		h.mu.Unlock()
		writeError(w, 404, "not found")
		return
	}
	h.service.mu.Lock()
	lease := h.service.leases[id]
	var process *herdrTerminalProcess
	if lease != nil && !lease.stopping {
		process, _ = lease.proc.(*herdrTerminalProcess)
	}
	h.service.mu.Unlock()
	h.mu.Unlock()
	if process == nil {
		writeError(w, 404, "not found")
		return
	}
	process.claimMu.Lock()
	if process.claimed {
		process.claimMu.Unlock()
		writeError(w, 409, "stream already connected")
		return
	}
	process.claimed = true
	process.claimMu.Unlock()
	defer h.service.DeleteLease(id)
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(maxBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			kind, data, err := connection.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText || validateTerminalCommand(data) != nil {
				return
			}
			if process.write(data) != nil {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case frame, ok := <-process.frames:
			if !ok {
				return
			}
			deadline, release := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Write(deadline, websocket.MessageText, frame)
			release()
			if err != nil {
				return
			}
		case <-heartbeat.C:
			deadline, release := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Ping(deadline)
			release()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		case <-process.done:
			return
		}
	}
}

func validateTerminalCommand(data []byte) error {
	var command struct {
		Type      string `json:"type"`
		Bytes     string `json:"bytes,omitempty"`
		Cols      int    `json:"cols,omitempty"`
		Rows      int    `json:"rows,omitempty"`
		Direction string `json:"direction,omitempty"`
		Lines     int    `json:"lines,omitempty"`
		Column    *int   `json:"column,omitempty"`
		Row       *int   `json:"row,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil {
		return errUnsupported
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errUnsupported
	}
	if command.Column != nil || command.Row != nil {
		if command.Type != "terminal.scroll" || command.Column == nil || command.Row == nil || *command.Column < 0 || *command.Column >= 500 || *command.Row < 0 || *command.Row >= 200 {
			return errUnsupported
		}
	}
	switch command.Type {
	case "terminal.input":
		if command.Cols != 0 || command.Rows != 0 || command.Direction != "" || command.Lines != 0 {
			break
		}
		bytes, err := base64.StdEncoding.DecodeString(command.Bytes)
		if err == nil && len(bytes) > 0 && len(bytes) <= 8192 {
			return nil
		}
	case "terminal.resize":
		if command.Bytes == "" && command.Direction == "" && command.Lines == 0 && command.Cols >= 20 && command.Cols <= 500 && command.Rows >= 5 && command.Rows <= 200 {
			return nil
		}
	case "terminal.scroll":
		if command.Bytes == "" && command.Cols == 0 && command.Rows == 0 && (command.Direction == "up" || command.Direction == "down") && command.Lines > 0 && command.Lines <= 200 {
			return nil
		}
	case "terminal.release":
		if command.Bytes == "" && command.Cols == 0 && command.Rows == 0 && command.Direction == "" && command.Lines == 0 {
			return nil
		}
	}
	return errors.New("invalid terminal command")
}
