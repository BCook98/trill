package demoapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"trill/sidecar"
	"trill/sidecar/pairing"
	"trill/sidecar/setup"
)

type runtimeState struct {
	root         string
	manifest     manifest
	demo         *sidecar.Demo
	store        *pairing.Store
	cancel       context.CancelFunc
	mu           sync.Mutex
	faults       map[string]bool
	lastActivity time.Time
	retention    time.Duration
	expired      bool
}

func quietLog() *log.Logger { return log.New(io.Discard, "", 0) }
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
func bodyJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		problem(w, 415, "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, 400, "invalid request")
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		problem(w, 400, "trailing request data")
		return false
	}
	return true
}
func (s *runtimeState) touch() { s.mu.Lock(); s.lastActivity = time.Now(); s.mu.Unlock() }
func (s *runtimeState) fault(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faults[name]
}
func (s *runtimeState) takeFault(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.faults[name]
	delete(s.faults, name)
	return v
}
func (s *runtimeState) setFault(name string) error {
	s.mu.Lock()
	switch name {
	case "clear":
		s.faults = map[string]bool{}
		s.mu.Unlock()
		return s.demo.Fault("clear")
	case "api-unavailable", "preview-unavailable", "upload-rejected", "invitation-expired", "bootstrap-delayed":
		s.faults[name] = true
		s.mu.Unlock()
		return nil
	default:
		s.mu.Unlock()
		return s.demo.Fault(name)
	}
}
func (s *runtimeState) invitation() ([]byte, error) {
	ttl := pairing.MaxInvitationTTL
	if s.takeFault("invitation-expired") {
		ttl = time.Nanosecond
	}
	code, expiry, err := s.store.CreateInvitation(ttl)
	if err != nil {
		return nil, err
	}
	s.touch()
	return setup.PairPayload(setup.Config{Address: s.manifest.Address, Name: s.manifest.Name, Fingerprint: s.manifest.Fingerprint}, code, expiry)
}
func (s *runtimeState) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		m := s.manifest
		m.State = s.demo.Status()
		m.RuntimeFaults = []string{}
		s.mu.Lock()
		for name, armed := range s.faults {
			if armed {
				m.RuntimeFaults = append(m.RuntimeFaults, name)
			}
		}
		s.mu.Unlock()
		sort.Strings(m.RuntimeFaults)
		jsonResponse(w, 200, m)
	})
	mux.HandleFunc("POST /pair", func(w http.ResponseWriter, r *http.Request) {
		b, err := s.invitation()
		if err != nil {
			problem(w, 503, "cannot create invitation")
			return
		}
		jsonResponse(w, 200, json.RawMessage(b))
	})
	mux.HandleFunc("POST /fault", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Name string `json:"name"`
		}
		if !bodyJSON(w, r, &v) {
			return
		}
		if err := s.setFault(v.Name); err != nil {
			problem(w, 400, "unknown or unavailable fault")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ID string `json:"id"`
		}
		if !bodyJSON(w, r, &v) {
			return
		}
		if err := s.store.Revoke(v.ID); err != nil {
			problem(w, 400, "unknown device")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]bool{"ok": true})
		s.cancel()
	})
	mux.HandleFunc("GET /terminal", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.demo.Snapshot(r.URL.Query().Get("session"))
		if err != nil {
			problem(w, 404, "session unavailable")
			return
		}
		s.touch()
		jsonResponse(w, 200, v)
	})
	mux.HandleFunc("POST /terminal", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Session string `json:"session"`
			Line    string `json:"line"`
		}
		if !bodyJSON(w, r, &v) {
			return
		}
		snapshot, err := s.demo.Command(v.Session, v.Line)
		if err != nil {
			problem(w, 400, "command unavailable")
			return
		}
		s.touch()
		jsonResponse(w, 200, snapshot)
	})
	mux.HandleFunc("POST /resize", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Session string `json:"session"`
			Columns int    `json:"columns"`
			Rows    int    `json:"rows"`
		}
		if !bodyJSON(w, r, &v) {
			return
		}
		snapshot, err := s.demo.Resize(v.Session, v.Columns, v.Rows)
		if err != nil {
			problem(w, 400, "invalid resize")
			return
		}
		jsonResponse(w, 200, snapshot)
	})
	return mux
}
func callControl(root, method, path string, body, target any) error {
	socket := filepath.Join(root, "control.sock")
	st, err := os.Lstat(socket)
	if err != nil {
		return errors.New("owned demo control socket is unavailable; start this sandbox first")
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0077 != 0 || !ok || int(raw.Uid) != os.Getuid() {
		return errors.New("unsafe demo control socket")
	}
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(b)
	}
	request, err := http.NewRequest(method, "http://demo-control"+path, input)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", filepath.Join(root, "control.sock"))
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("owned demo control socket is unavailable; start this sandbox first")
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("invalid demo control response")
	}
	if response.StatusCode != 200 {
		return errors.New("demo control request rejected: " + http.StatusText(response.StatusCode))
	}
	return json.Unmarshal(b, target)
}
func (s *runtimeState) preview(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.fault("preview-unavailable") {
			problem(w, 503, "demo preview unavailable")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *runtimeState) authenticated(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	_, err := s.store.Authenticate(strings.TrimPrefix(values[0], "Bearer "))
	return err == nil
}

type retentionState struct {
	LastActivity time.Time `json:"lastActivity"`
}

func (s *runtimeState) saveRetention() error {
	s.mu.Lock()
	v := retentionState{s.lastActivity}
	s.mu.Unlock()
	return writeAtomic(filepath.Join(s.root, "retention.json"), v)
}
func (s *runtimeState) expireIdle(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			expired := time.Since(s.lastActivity) > s.retention
			if expired {
				s.expired = true
			}
			s.mu.Unlock()
			if expired {
				s.cancel()
				return
			}
			_ = s.saveRetention()
		}
	}
}
func retainedActivity(root string) (time.Time, error) {
	var v retentionState
	err := readPrivate(filepath.Join(root, "retention.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Lstat(filepath.Join(root, "demo-state.json")); !errors.Is(e, os.ErrNotExist) {
			return time.Time{}, errors.New("demo retention metadata missing; explicit reset required")
		}
		return time.Now(), nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if v.LastActivity.IsZero() || v.LastActivity.After(time.Now().Add(time.Minute)) {
		return time.Time{}, errors.New("invalid retention timestamp")
	}
	return v.LastActivity, nil
}
func expireStored(root string, retention time.Duration) error {
	last, err := retainedActivity(root)
	if err != nil {
		return err
	}
	if time.Since(last) <= retention {
		return nil
	}
	return clearData(root)
}

// Keep the expiry marker until every data deletion succeeds. A partial cleanup
// fails closed on next startup and cannot silently renew retained credentials.
func clearData(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == markerName || e.Name() == lockName || e.Name() == "tls.pem" || e.Name() == "retention.json" {
			continue
		}
		if err = os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return err
		}
	}
	err = os.Remove(filepath.Join(root, "retention.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
