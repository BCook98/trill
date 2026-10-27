package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"trill/sidecar/pairing"
)

type call struct {
	name string
	args []string
}
type fakeRunner struct {
	mu                        sync.Mutex
	calls                     []call
	terminalArgs, terminalEnv []string
	proc                      *fakeProcess
	sessions                  []rawSession
	projectsJSON              string
}
type fakeProcess struct {
	done  chan struct{}
	mu    sync.Mutex
	stops int
}

func (p *fakeProcess) Stop() {
	p.mu.Lock()
	p.stops++
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	p.mu.Unlock()
}
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) stopped() int          { p.mu.Lock(); defer p.mu.Unlock(); return p.stops }
func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{name, append([]string{}, args...)})
	return nil, errors.New("unexpected local command")
}
func (f *fakeRunner) StartHerdrTerminal(ctx context.Context, path string, args, env []string) (*herdrTerminalProcess, error) {
	f.mu.Lock()
	f.terminalArgs = append([]string{path}, args...)
	f.terminalEnv = env
	f.mu.Unlock()
	return (ExecRunner{}).StartHerdrTerminal(ctx, "/usr/bin/python3", []string{"-u", "-c", `import sys,json,base64
print(json.dumps(dict(type='terminal.frame',seq=1,width=80,height=24,bytes=base64.b64encode(b'fixture terminal').decode())),flush=True)
for line in sys.stdin:
 if json.loads(line).get('type')=='terminal.release': break
`}, nil)
}
func setup(t *testing.T, lab, control bool) (*HTTP, *fakeRunner, *Service) {
	t.Helper()
	fixture := &fakeRunner{sessions: []rawSession{
		{ID: "s1", Name: "One", ProjectID: "p1", Status: "running", ActivityState: "idle", CurrentAgent: "codex", Branch: "main", Mode: "terminal", HerdrTerminal: "term_s1", TerminalLive: true},
		{ID: "s2", Name: "Ended", ProjectID: "p1", Status: "completed", Mode: "terminal", HerdrTerminal: "term_s2"},
	}}
	if lab {
		fixture.sessions[0].ID = "lab-1"
		fixture.sessions[0].ProjectID = "terminal-lab"
	}
	service, err := NewService(Config{AllowControl: control}, fixture)
	if err != nil {
		t.Fatal(err)
	}
	service.readMetadata = func(ctx context.Context) ([]Project, []rawSession, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if fixture.sessions == nil {
			return nil, nil, errUnavailable
		}
		projects := []Project{{ID: "p1", Name: "Demo", Path: "/tmp/demo"}}
		if fixture.projectsJSON != "" {
			if json.Unmarshal([]byte(fixture.projectsJSON), &projects) != nil {
				return nil, nil, errUnavailable
			}
		}
		return projects, append([]rawSession{}, fixture.sessions...), nil
	}
	t.Cleanup(service.Close)
	return NewHTTP(service, "secret"), fixture, service
}
func request(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func pairPhone(t *testing.T, h *HTTP, store *pairing.Store, name string) (string, string) {
	t.Helper()
	code, _, err := store.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	w := request(h, "POST", "/v1/pair", "", string(body))
	var result struct {
		DeviceID string `json:"deviceId"`
		Token    string `json:"token"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.DeviceID == "" || result.Token == "" {
		t.Fatal("pair failed", w.Code)
	}
	return result.DeviceID, result.Token
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pairedHTTP(t *testing.T, control bool, legacy string) (*HTTP, *fakeRunner, *Service, *pairing.Store, string) {
	_, fixture, service := setup(t, true, control)
	path := filepath.Join(t.TempDir(), "devices.json")
	store := pairing.NewStore(path)
	handler := NewHTTPWithPairing(service, legacy, store)
	t.Cleanup(handler.Close)
	return handler, fixture, service, store, path
}
