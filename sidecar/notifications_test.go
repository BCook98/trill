package sidecar

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"trill/sidecar/pairing"
	"trill/sidecar/push"
)

type notificationRecorder struct {
	alerts  []push.Alert
	failure error
}

func (r *notificationRecorder) Send(_ context.Context, alert push.Alert) error {
	r.alerts = append(r.alerts, alert)
	return r.failure
}
func testRegistration() push.Registration {
	return push.Registration{Token: strings.Repeat("a", 64), Environment: "sandbox", ComputerID: "11111111-1111-4111-8111-111111111111", Attention: true, Completion: true}
}
func TestAgentAlertTransitions(t *testing.T) {
	for _, tc := range []struct{ previous, current, want string }{
		{"", "blocked", ""}, {"", "done", ""}, {"blocked", "blocked", ""}, {"done", "done", ""}, {"unknown", "done", ""}, {"idle", "done", ""}, {"working", "idle", ""}, {"working", "unknown", ""},
		{"working", "blocked", "attention"}, {"idle", "blocked", "attention"}, {"working", "done", "completion"}, {"blocked", "done", "completion"},
	} {
		if got := agentAlertKind(tc.previous, tc.current); got != tc.want {
			t.Fatalf("%s -> %s: %q", tc.previous, tc.current, got)
		}
	}
}
func TestNotificationHTTPPreferencesAndCleanup(t *testing.T) {
	handler, _, _, store, _ := pairedHTTP(t, false, "legacy")
	deviceID, token := pairPhone(t, handler, store, "Phone")
	body, _ := json.Marshal(testRegistration())
	if response := request(handler, "PUT", "/v1/notifications", token, string(body)); response.Code != 503 {
		t.Fatal("unconfigured provider advertised")
	}
	// Isolate endpoints without starting a real watcher or connecting any socket.
	handler.notifications = &agentNotifications{sender: &notificationRecorder{}, cancel: func() {}, done: make(chan struct{})}
	close(handler.notifications.done)
	if response := request(handler, "PUT", "/v1/notifications", "legacy", string(body)); response.Code != 409 {
		t.Fatal("legacy registration accepted")
	}
	if response := request(handler, "PUT", "/v1/notifications", token, string(body)); response.Code != 200 {
		t.Fatal(response.Code)
	}
	response := request(handler, "GET", "/v1/notifications", token, "")
	if response.Code != 200 || strings.Contains(response.Body.String(), testRegistration().Token) {
		t.Fatal("token exposed")
	}
	if response := request(handler, "DELETE", "/v1/notifications", token, ""); response.Code != 200 {
		t.Fatal("delete failed")
	}
	registrations, _ := store.PushRegistrations()
	if len(registrations) != 0 {
		t.Fatal("registration retained")
	}
	if err := store.SetPush(deviceID, func() *push.Registration { r := testRegistration(); return &r }()); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(deviceID); err != nil {
		t.Fatal(err)
	}
	if response := request(handler, "PUT", "/v1/notifications", token, string(body)); response.Code != 401 {
		t.Fatal("revoked device registered")
	}
}
func TestNativeNotificationStreamSeedsSilentlyAndDeduplicates(t *testing.T) {
	// Synthetic socket and metadata only: never attach to a personal Herdr server.
	root, err := os.MkdirTemp("/tmp", "trill-push-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	directory := filepath.Join(root, "herdr", "sessions", "test-server")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(directory, "herdr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	service := herdrService(t, &herdrRunner{snapshot: herdrFixture})
	service.config.HerdrConfigHome = root
	store := pairing.NewStore(filepath.Join(root, "devices.json"))
	handler := NewHTTPWithPairing(service, "", store)
	defer handler.Close()
	deviceID, _ := pairPhone(t, handler, store, "Phone")
	registration := testRegistration()
	if err := store.SetPush(deviceID, &registration); err != nil {
		t.Fatal(err)
	}
	recorder := &notificationRecorder{}
	handler.notifications = &agentNotifications{sender: recorder, cancel: func() {}, done: make(chan struct{})}
	close(handler.notifications.done)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		var subscription map[string]any
		if json.NewDecoder(connection).Decode(&subscription) != nil {
			return
		}
		encoder := json.NewEncoder(connection)
		_ = encoder.Encode(map[string]any{"result": map[string]string{"type": "subscribed"}})
		for _, status := range []string{"blocked", "working", "blocked", "blocked", "done", "done", "idle", "working", "done", "working"} {
			_ = encoder.Encode(map[string]any{"event": "pane_agent_status_changed", "data": map[string]string{"type": "pane_agent_status_changed", "pane_id": "w1:p1", "agent_status": status, "title": "SECRET PROMPT /private/file"}})
		}
		_ = encoder.Encode(map[string]any{"event": "pane_agent_detected", "data": map[string]any{"type": "pane_agent_detected", "pane_id": "w1:p1", "released": true, "final_status": "done"}})
	}()
	_ = handler.consumeAgentNotifications(context.Background(), service)
	<-serverDone
	if len(recorder.alerts) != 4 {
		t.Fatalf("got %d alerts, want 4", len(recorder.alerts))
	}
	seen := map[string]bool{}
	for _, alert := range recorder.alerts {
		if seen[alert.EventID] || alert.SessionID != service.herdrID("terminal", "term_original") {
			t.Fatal("duplicate/wrong route")
		}
		seen[alert.EventID] = true
		payload, _ := alert.Payload()
		if strings.Contains(string(payload), "SECRET") {
			t.Fatal("content leak")
		}
	}
	recorder.failure = push.ErrInvalidToken
	handler.sendAgentAlert(context.Background(), service.herdrID("terminal", "term_original"), "attention")
	registrations, _ := store.PushRegistrations()
	if len(registrations) != 0 {
		t.Fatal("invalid token retained")
	}
}
