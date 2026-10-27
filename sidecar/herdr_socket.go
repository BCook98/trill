package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

func herdrSocketPath(configurationHome, session string) string {
	directory := filepath.Join(configurationHome, "herdr")
	if session != "default" {
		directory = filepath.Join(directory, "sessions", session)
	}
	return filepath.Join(directory, "herdr.sock")
}
func openHerdrSocket(ctx context.Context, configurationHome, session string) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", herdrSocketPath(configurationHome, session))
	if err != nil {
		return nil, errUnavailable
	}
	deadline := time.Now().Add(12 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	_ = connection.SetDeadline(deadline)
	return connection, nil
}
func herdrSocketRequest(connection net.Conn, method string, params any) error {
	return json.NewEncoder(connection).Encode(map[string]any{"id": "trill", "method": method, "params": params})
}
func (ExecRunner) HerdrSnapshot(ctx context.Context, configurationHome, session string) ([]byte, error) {
	connection, err := openHerdrSocket(ctx, configurationHome, session)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err = herdrSocketRequest(connection, "session.snapshot", map[string]any{}); err != nil {
		return nil, errUnavailable
	}
	var response json.RawMessage
	if json.NewDecoder(io.LimitReader(connection, maxCommandOutput)).Decode(&response) != nil {
		return nil, errUnavailable
	}
	return response, nil
}

// Changes are invalidation hints, never patches replayed over a newer snapshot.
// A fresh subscription on every bounded long poll includes the current panes.
func (service *Service) waitHerdrChange(ctx context.Context) error {
	connection, err := openHerdrSocket(ctx, service.config.HerdrConfigHome, service.config.HerdrSession)
	if err != nil {
		return err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	_, sessions, err := service.herdrMetadata(ctx)
	if err != nil {
		return err
	}
	subscriptions := []map[string]string{}
	for _, kind := range []string{
		"workspace.created", "workspace.updated", "workspace.metadata_updated", "workspace.renamed", "workspace.closed", "workspace.focused", "workspace.moved", "workspace.reordered",
		"worktree.created", "worktree.opened", "worktree.removed",
		"tab.created", "tab.closed", "tab.renamed", "tab.moved", "tab.focused",
		"pane.created", "pane.closed", "pane.updated", "pane.moved", "pane.focused", "pane.exited", "pane.agent_detected", "layout.updated",
	} {
		subscriptions = append(subscriptions, map[string]string{"type": kind})
	}
	for _, session := range sessions {
		subscriptions = append(subscriptions, map[string]string{"type": "pane.agent_status_changed", "pane_id": session.HerdrPane})
	}
	if herdrSocketRequest(connection, "events.subscribe", map[string]any{"subscriptions": subscriptions}) != nil {
		return errUnavailable
	}
	decoder := json.NewDecoder(io.LimitReader(connection, maxCommandOutput))
	var acknowledgement struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if decoder.Decode(&acknowledgement) != nil || len(acknowledgement.Error) > 0 || len(acknowledgement.Result) == 0 {
		return errUnavailable
	}
	var event json.RawMessage
	if decoder.Decode(&event) != nil {
		return errUnavailable
	}
	return nil
}
func (h *HTTP) herdrChanges(w http.ResponseWriter, r *http.Request) {
	select {
	case h.changeSlots <- struct{}{}:
		defer func() { <-h.changeSlots }()
	default:
		writeError(w, 429, "event capacity reached")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sources := h.service.sources
	if len(sources) == 0 {
		sources = []*Service{h.service}
	}
	changes := make(chan bool, len(sources))
	count := 0
	for _, source := range sources {
		if source.config.HerdrPath == "" {
			continue
		}
		count++
		go func() { changes <- source.waitHerdrChange(ctx) == nil }()
	}
	if count == 0 {
		h.fail(w, errUnsupported)
		return
	}
	for count > 0 {
		select {
		case changed := <-changes:
			if changed {
				writeJSON(w, 200, map[string]bool{"changed": true})
				return
			}
			count--
		case <-ctx.Done():
			count = 0
		case <-h.done:
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"changed": false})
}
