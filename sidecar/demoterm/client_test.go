package demoterm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func unixServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// macOS Unix socket names are short; the default Go temp root can exceed
	// sockaddr_un's limit. This private directory is created only for this test.
	dir, err := os.MkdirTemp("/tmp", "trill-dt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "ipc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
	return path
}

func writeSnapshot(w http.ResponseWriter, state snapshot) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func TestUnixClientContractAndNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	session := "s +/雪?&=x"
	requests := make(chan string, 3)
	path := unixServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "demo.invalid" {
			t.Errorf("unexpected host %q", r.Host)
		}
		if r.Method == http.MethodGet {
			if got := r.URL.Query().Get("session"); got != session {
				t.Errorf("session = %q", got)
			}
		} else {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Error("missing JSON content type")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["session"] != session {
				t.Errorf("body: %#v", body)
			}
			if r.URL.Path == "/terminal" && body["line"] != "demo heading 雪\nnext" {
				t.Error(body)
			}
			if r.URL.Path == "/resize" && (body["columns"] != float64(120) || body["rows"] != float64(40)) {
				t.Error(body)
			}
		}
		requests <- r.Method + " " + r.URL.Path
		writeSnapshot(w, snapshot{"persisted text", 42, true, 120, 40})
	})
	c := newClient(path, session)
	defer c.transport.CloseIdleConnections()
	for _, req := range []request{{kind: "poll"}, {kind: "submit", line: "demo heading 雪\nnext"}, {kind: "resize", columns: 120, rows: 40}} {
		state, err := c.do(context.Background(), req)
		if err != nil || state.Text != "persisted text" || state.Revision != 42 {
			t.Fatalf("%+v, %v", state, err)
		}
	}
	for _, expected := range []string{"GET /terminal", "POST /terminal", "POST /resize"} {
		if got := <-requests; got != expected {
			t.Fatal(got)
		}
	}
}

func TestClientRejectsUnsafeOrUnboundedResponses(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"redirect", "", http.StatusTemporaryRedirect},
		{"missing fields", `{"text":"ok"}`, 200},
		{"null", "null", 200},
		{"two JSON objects", `{} {}`, 200},
		{"invalid dimensions", `{"text":"ok","revision":1,"live":true,"columns":0,"rows":24}`, 200},
		{"too much text", fmt.Sprintf(`{"text":%q,"revision":1,"live":true,"columns":80,"rows":24}`, strings.Repeat("x", maxTextBytes+1)), 200},
		{"too much response", strings.Repeat(" ", maxResponseBytes+1), 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			path := unixServer(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Location", "http://example.invalid/stolen")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			c := newClient(path, "owned")
			defer c.transport.CloseIdleConnections()
			if _, err := c.do(context.Background(), request{kind: "poll"}); err == nil {
				t.Fatal("accepted invalid response")
			}
			if count.Load() != 1 {
				t.Fatal("followed redirect or retried")
			}
		})
	}
}

func TestClientDoesNotReplayLostMutation(t *testing.T) {
	var count atomic.Int32
	path := unixServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		// Simulate a committed mutation whose response disappears.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	c := newClient(path, "owned")
	defer c.transport.CloseIdleConnections()
	if _, err := c.do(context.Background(), request{kind: "submit", line: "demo task"}); err == nil {
		t.Fatal("expected uncertain result")
	}
	if count.Load() != 1 {
		t.Fatalf("mutation was replayed %d times", count.Load())
	}
}

func TestClientCancellationInterruptsStalledResponse(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	path := unixServer(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-release })
	defer close(release)
	c := newClient(path, "owned")
	defer c.transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.do(ctx, request{kind: "poll"}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("IPC failed to cancel")
	}
}
