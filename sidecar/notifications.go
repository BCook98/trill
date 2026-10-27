package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
	"trill/sidecar/push"
)

type agentNotifications struct {
	sender push.Sender
	cancel context.CancelFunc
	done   chan struct{}
}

// EnableAgentNotifications must be called before serving HTTP. No local/iOS
// polling is involved. The Mac must remain awake, connected, and running Herdr.
func (h *HTTP) EnableAgentNotifications(parent context.Context, sender push.Sender) error {
	if h.store == nil || sender == nil || h.notifications != nil || h.service.demo != nil {
		return errors.New("notifications require device pairing and an explicit relay")
	}
	ctx, cancel := context.WithCancel(parent)
	h.notifications = &agentNotifications{sender: sender, cancel: cancel, done: make(chan struct{})}
	sources := h.service.sources
	if len(sources) == 0 {
		sources = []*Service{h.service}
	}
	go func() {
		defer close(h.notifications.done)
		var workers sync.WaitGroup
		for _, source := range sources {
			if source.config.HerdrPath == "" {
				continue
			}
			workers.Add(1)
			go func() { defer workers.Done(); h.watchAgentNotifications(ctx, source) }()
		}
		workers.Wait()
	}()
	return nil
}
func (h *HTTP) notificationStatus(w http.ResponseWriter, r *http.Request) {
	var registration any
	if h.store != nil && requestDevice(r) != "" {
		registrations, err := h.store.PushRegistrations()
		if err != nil {
			writeError(w, 503, "Notification preferences could not be read.")
			return
		}
		if value, found := registrations[requestDevice(r)]; found {
			// Device token stays private, including in API responses.
			registration = map[string]any{"attention": value.Attention, "completion": value.Completion, "computerId": value.ComputerID, "expiresAt": value.ExpiresAt}
		}
	}
	writeJSON(w, 200, map[string]any{"available": h.notifications != nil && requestDevice(r) != "", "registration": registration})
}
func (h *HTTP) registerNotifications(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil {
		writeError(w, 503, "Background alerts are not configured on this computer.")
		return
	}
	if requestDevice(r) == "" {
		writeError(w, 409, "Pair this device using a pairing code to enable alerts.")
		return
	}
	var registration push.Registration
	if !decodeBody(w, r, &registration) {
		return
	}
	if !registration.Valid() {
		writeError(w, 400, "Invalid notification preferences.")
		return
	}
	if err := h.store.SetPush(requestDevice(r), &registration); err != nil {
		writeError(w, 503, "Notification preferences could not be saved.")
		return
	}
	ok(w)
}
func (h *HTTP) deleteNotifications(w http.ResponseWriter, r *http.Request) {
	if h.store != nil && requestDevice(r) != "" {
		if err := h.store.SetPush(requestDevice(r), nil); err != nil {
			writeError(w, 503, "Notification preferences could not be removed.")
			return
		}
	}
	ok(w)
}

// Only native blocked/done transitions alert. Unknown, idle, process exit and
// agent discovery are not completion signals. A reconnect always seeds a new
// baseline, sacrificing missed events rather than replaying an old catalog.
func agentAlertKind(previous, current string) string {
	switch previous {
	case "idle", "working", "blocked", "done":
	default:
		return ""
	}
	if previous == current {
		return ""
	}
	if current == "blocked" {
		return "attention"
	}
	if current == "done" && (previous == "working" || previous == "blocked") {
		return "completion"
	}
	return ""
}
func (h *HTTP) watchAgentNotifications(ctx context.Context, source *Service) {
	for ctx.Err() == nil {
		registrations, err := h.store.PushRegistrations()
		if err == nil && len(registrations) > 0 {
			_ = h.consumeAgentNotifications(ctx, source)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-source.closeDone:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (h *HTTP) consumeAgentNotifications(ctx context.Context, source *Service) error {
	_, sessions, err := source.herdrMetadata(ctx)
	if err != nil {
		return err
	}
	connection, err := openHerdrSocket(ctx, source.config.HerdrConfigHome, source.config.HerdrSession)
	if err != nil {
		return err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	// Topology/agent identity changes trigger a fresh subscription and baseline.
	subscriptions := []map[string]string{}
	for _, kind := range []string{"pane.created", "pane.closed", "pane.moved", "pane.exited", "pane.agent_detected"} {
		subscriptions = append(subscriptions, map[string]string{"type": kind})
	}
	byPane := map[string]rawSession{}
	for _, session := range sessions {
		byPane[session.HerdrPane] = session
		subscriptions = append(subscriptions, map[string]string{"type": "pane.agent_status_changed", "pane_id": session.HerdrPane})
	}
	if err := herdrSocketRequest(connection, "events.subscribe", map[string]any{"subscriptions": subscriptions}); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(connection, 8<<20))
	var acknowledgement struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if decoder.Decode(&acknowledgement) != nil || len(acknowledgement.Result) == 0 || len(acknowledgement.Error) > 0 {
		return errUnavailable
	}
	for {
		// Idle expiry also sweeps removed registrations and renews subscriptions.
		_ = connection.SetReadDeadline(time.Now().Add(time.Minute))
		var event struct {
			Event string `json:"event"`
			Data  struct {
				Type        string `json:"type"`
				PaneID      string `json:"pane_id"`
				Status      string `json:"agent_status"`
				FinalStatus string `json:"final_status"`
				Released    bool   `json:"released"`
			} `json:"data"`
		}
		if decoder.Decode(&event) != nil {
			return errUnavailable
		}
		released := event.Event == "pane_agent_detected" && event.Data.Type == event.Event && event.Data.Released && event.Data.FinalStatus == "done"
		if released {
			event.Data.Status = event.Data.FinalStatus
		}
		if !released && (event.Event != "pane_agent_status_changed" || event.Data.Type != event.Event) {
			return errUnavailable
		}
		session, found := byPane[event.Data.PaneID]
		if !found {
			return errUnavailable
		}
		kind := agentAlertKind(session.HerdrAgentStatus, event.Data.Status)
		session.HerdrAgentStatus = event.Data.Status
		byPane[event.Data.PaneID] = session // Advance before sending: no retry/replay on ambiguous failures.
		if kind != "" && session.TerminalLive && session.CurrentAgent != "Shell" && session.CurrentAgent != "" {
			h.sendAgentAlert(ctx, session.ID, kind)
		}
		if released {
			return nil
		}
	}
}
func (h *HTTP) sendAgentAlert(ctx context.Context, sessionID, kind string) {
	registrations, err := h.store.PushRegistrations()
	if err != nil {
		return
	}
	for deviceID, registration := range registrations {
		if kind == "attention" && !registration.Attention || kind == "completion" && !registration.Completion {
			continue
		}
		// Re-read immediately before sending; revoke/delete/token rotation must win
		// over a previously fetched catalog of recipients.
		latest, err := h.store.PushRegistrations()
		if err != nil || latest[deviceID] != registration {
			continue
		}
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return
		}
		random[6] = (random[6] & 15) | 64
		random[8] = (random[8] & 63) | 128
		eventID := fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:])
		err = h.notifications.sender.Send(ctx, push.Alert{Token: registration.Token, Environment: registration.Environment, ComputerID: registration.ComputerID, SessionID: sessionID, EventID: eventID, Kind: kind})
		if errors.Is(err, push.ErrInvalidToken) {
			_ = h.store.RemovePushToken(deviceID, registration.Token)
		}
	}
}
