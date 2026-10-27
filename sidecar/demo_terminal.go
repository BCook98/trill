package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
	"trill/sidecar/demoterm"
)

func (service *Service) startDemoTerminal(ctx context.Context, id string, columns, rows int) (*herdrTerminalProcess, error) {
	if err := service.demo.prepareTerminal(id, columns, rows); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	process := &herdrTerminalProcess{input: writer, frames: make(chan []byte, 8), done: make(chan struct{}), cancel: cancel}
	type command struct {
		Type      string `json:"type"`
		Bytes     []byte `json:"bytes"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
		Direction string `json:"direction"`
		Lines     int    `json:"lines"`
	}
	commands := make(chan command, 8)
	go func() {
		defer cancel()
		defer reader.Close()
		decoder := json.NewDecoder(reader)
		for {
			var next command
			if decoder.Decode(&next) != nil {
				return
			}
			select {
			case commands <- next:
			case <-lifetime.Done():
				return
			}
		}
	}()
	go func() {
		defer close(process.done)
		defer close(process.frames)
		defer cancel()
		defer reader.Close()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var editor demoterm.StreamEditor
		var sequence uint64
		var revision uint64
		offset := 0
		dirty := true
		for {
			snapshot, err := service.demo.Snapshot(id)
			if err != nil {
				return
			}
			if dirty || snapshot.Revision != revision {
				text := snapshot.Text
				if offset > 0 {
					lines := strings.Split(text, "\n")
					maximum := len(lines) - 1
					if offset > maximum {
						offset = maximum
					}
					text = strings.Join(lines[:len(lines)-offset], "\n")
				}
				sequence++
				frame, _ := json.Marshal(map[string]any{"type": "terminal.frame", "seq": sequence, "width": columns, "height": rows, "full": true, "bytes": base64.StdEncoding.EncodeToString([]byte(editor.Render(text, snapshot.Live, columns, rows)))})
				select {
				case process.frames <- frame:
				case <-lifetime.Done():
					return
				case <-time.After(10 * time.Second):
					return
				}
				dirty = false
				revision = snapshot.Revision
			}
			select {
			case <-lifetime.Done():
				return
			case <-ticker.C:
			case next := <-commands:
				dirty = true
				switch next.Type {
				case "terminal.release":
					return
				case "terminal.input":
					lines, closed := editor.Input(next.Bytes)
					if closed {
						return
					}
					for _, line := range lines {
						if _, err := service.demo.Command(id, line); err != nil {
							return
						}
					}
					offset = 0
				case "terminal.resize":
					columns, rows = next.Cols, next.Rows
					if _, err := service.demo.Resize(id, columns, rows); err != nil {
						return
					}
				case "terminal.scroll":
					if next.Direction == "up" {
						offset += next.Lines
					} else {
						offset -= next.Lines
					}
					if offset < 0 {
						offset = 0
					}
				}
			}
		}
	}()
	return process, nil
}
