package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	mu           sync.Mutex
	calls        []call
	moshArgs     []string
	moshEnv      []string
	proc         *fakeProcess
	sessions     []rawSession
	replies      map[string]string
	noApply      map[string]bool
	apiErrors    map[string]bool
	afterAPI     func(string, []rawSession)
	projectsJSON string
	moshStarted  chan struct{}
	moshRelease  <-chan struct{}
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
	f.calls = append(f.calls, call{name, append([]string(nil), args...)})
	if name == "xirp" {
		if reflect.DeepEqual(args, []string{"project", "list", "--json"}) {
			if f.projectsJSON != "" {
				return []byte(f.projectsJSON), nil
			}
			return []byte(`[{"id":"p1","name":"Demo","path":"/tmp/demo"}]`), nil
		}
		if reflect.DeepEqual(args, []string{"session", "list", "--all", "--json"}) {
			return json.Marshal(f.sessions)
		}
		if len(args) >= 5 && reflect.DeepEqual(args[:2], []string{"api", "send"}) {
			kind := args[2]
			var params map[string]any
			if json.Unmarshal([]byte(args[4]), &params) != nil {
				return nil, errors.New("bad fake API JSON")
			}
			// Xirp 0.60 validates raw JSON against the catalog before sending.
			// session:create's catalog omits requestId, unlike terminal creation.
			if kind == "session:create" {
				allowed := map[string]bool{"projectId": true, "goal": true, "customName": true, "useTerminal": true, "newBranch": true, "worktreePath": true, "tags": true}
				for key := range params {
					if !allowed[key] {
						return nil, errors.New("unknown creation parameter: " + key)
					}
				}
			}
			id, _ := params["sessionId"].(string)
			if !f.noApply[kind] {
				switch kind {
				case "session:create", "project:create-terminal":
					name, _ := params["customName"].(string)
					if name == "" {
						name = "Auto title"
					}
					goal, _ := params["goal"].(string)
					branch, _ := params["newBranch"].(string)
					path, _ := params["worktreePath"].(string)
					source := ""
					if branch != "" {
						path = "/tmp/demo-worktrees/" + branch
					}
					if kind == "project:create-terminal" {
						path, source = "/tmp/demo", "terminal"
					}
					var tags []string
					if data, err := json.Marshal(params["tags"]); err == nil {
						_ = json.Unmarshal(data, &tags)
					}
					f.sessions = append(f.sessions, rawSession{Tags: tags, ID: "created-1", Name: name, ProjectID: params["projectId"].(string), Goal: goal, Branch: branch, WorktreePath: path, Source: source, Status: "running", Mode: "terminal", TerminalLive: true, TmuxSession: "xirp-created-1"})
				case "session:update", "session:stop", "session:resume", "session-sidebar:set":
					for i := range f.sessions {
						if f.sessions[i].ID == id {
							switch kind {
							case "session:update":
								f.sessions[i].Name = params["name"].(string)
							case "session:stop":
								f.sessions[i].Status = "completed"
								f.sessions[i].TerminalLive = false
							case "session:resume":
								f.sessions[i].Status = "running"
								f.sessions[i].TerminalLive = true
							case "session-sidebar:set":
								f.sessions[i].HiddenFromSidebar = params["hidden"].(bool)
							}
							break
						}
					}
				}
			}
			if f.afterAPI != nil {
				f.afterAPI(kind, f.sessions)
			}
			if f.apiErrors[kind] {
				return nil, errors.New("fake lost response")
			}
			if reply, ok := f.replies[kind]; ok {
				if requestID, ok := params["requestId"].(string); ok {
					reply = strings.ReplaceAll(reply, "$REQUEST_ID", requestID)
				}
				return []byte(reply), nil
			}
			if kind == "session:create" || kind == "project:create-terminal" {
				return json.Marshal(map[string]any{"response": map[string]any{"type": "session:created", "sessionId": "created-1", "requestId": params["requestId"]}})
			}
			return []byte(`{"response":{"type":"session:updated","sessionId":"` + id + `"}}`), nil
		}
	}
	if name == "/usr/bin/tmux" && reflect.DeepEqual(args, []string{"-S", "/tmp/trill-lab.sock", "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_activity}"}) {
		return []byte("$1\tlab-one\t1760000000\n"), nil
	}
	if name == "/usr/bin/tmux" && reflect.DeepEqual(args, []string{"-S", "/tmp/trill-lab.sock", "has-session", "-t", "=lab-one"}) {
		return nil, nil
	}
	return nil, errors.New("unexpected command")
}
func (f *fakeRunner) StartMosh(ctx context.Context, name string, args []string, env []string) (MoshProcess, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{name, append([]string(nil), args...)})
	f.moshArgs = append([]string(nil), args...)
	f.moshEnv = append([]string(nil), env...)
	f.proc = &fakeProcess{done: make(chan struct{})}
	p := f.proc
	started, release := f.moshStarted, f.moshRelease
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	return p, "MOSH CONNECT 60001 abcdefghijklmnopqrstuvwxyz123456", nil
}
func setup(t *testing.T, lab, control bool) (*HTTP, *fakeRunner, *Service) {
	t.Helper()
	f := &fakeRunner{sessions: []rawSession{
		{ID: "s1", Name: "One", ProjectID: "p1", Status: "running", ActivityState: "idle", CurrentAgent: "codex", Branch: "main", UpdatedAt: "2026-01-01T00:00:00Z", Mode: "terminal", TmuxSession: "xirp-s1", TerminalLive: true, CLISessionID: "cli1"},
		{ID: "s2", Name: "Ended", ProjectID: "p1", Status: "completed", Mode: "terminal", TmuxSession: "xirp-s2", OriginalCLISessionID: "original-cli2"},
	}, replies: map[string]string{}, noApply: map[string]bool{}}
	cfg := Config{AllowControl: control, TmuxPath: "/usr/bin/tmux"}
	if lab {
		cfg.TmuxSocket = "/tmp/trill-lab.sock"
		cfg.TmuxPath = "/usr/bin/tmux"
	}
	s, err := NewService(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return NewHTTP(s, "secret"), f, s
}
func lastAPICall(f *fakeRunner) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if len(f.calls[i].args) >= 3 && f.calls[i].args[0] == "api" {
			return f.calls[i]
		}
	}
	return call{}
}
func countAPICalls(f *fakeRunner) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c.args) >= 3 && c.args[0] == "api" {
			n++
		}
	}
	return n
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
func TestAuthenticationAndReadOnly(t *testing.T) {
	h, f, _ := setup(t, false, false)
	for _, tok := range []string{"", "wrong"} {
		if got := request(h, "GET", "/v1/sessions", tok, "").Code; got != 401 {
			t.Fatalf("auth: %d", got)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("unauthorized request executed command")
	}
	if got := request(h, "GET", "/v1/health", "secret", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"readOnly":true`) {
		t.Fatal(got)
	}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/v1/sessions", `{"projectId":"p1","name":"n","goal":"g"}`}, {"PATCH", "/v1/sessions/s1", `{"name":"x"}`}, {"POST", "/v1/sessions/s1/stop", ""}, {"POST", "/v1/sessions/s2/resume", ""}, {"POST", "/v1/sessions/s1/mosh", `{"cols":80,"rows":24}`}, {"DELETE", "/v1/leases/" + strings.Repeat("a", 48), ""}} {
		if got := request(h, tc.method, tc.path, "secret", tc.body).Code; got != 403 {
			t.Errorf("%s %s: %d", tc.method, tc.path, got)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("read-only request executed command")
	}
}
func TestMetadataAndControlAllowlist(t *testing.T) {
	h, f, _ := setup(t, false, true)
	w := request(h, "GET", "/v1/sessions", "secret", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var data struct {
		Sessions []Session `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 2 || !data.Sessions[0].Attachable || data.Sessions[0].ProjectName != "Demo" || !data.Sessions[1].Resumable {
		t.Fatalf("sessions: %+v", data.Sessions)
	}
	if got := request(h, "POST", "/v1/sessions/s2/resume", "secret", "").Code; got != 200 {
		t.Fatal(got)
	}
	last := lastAPICall(f)
	if last.name != "xirp" || !reflect.DeepEqual(last.args[:4], []string{"api", "send", "session:resume", "--json"}) {
		t.Fatal(last)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(last.args[4]), &params); err != nil || params["cliSessionId"] != "original-cli2" || len(params) != 2 {
		t.Fatalf("resume params: %v %v", params, err)
	}
	if got := request(h, "PATCH", "/v1/sessions/s1", "secret", `{"name":"safe;$(touch /tmp/bad)"}`).Code; got != 200 {
		t.Fatal(got)
	}
	last = lastAPICall(f)
	if last.name != "xirp" || last.args[2] != "session:update" {
		t.Fatal(last)
	}
	if got := request(h, "POST", "/v1/sessions/pwn%2Fetc/stop", "secret", "").Code; got == 200 {
		t.Fatal("injected ID accepted")
	}
	if got := request(h, "POST", "/v1/sessions/missing/stop", "secret", "").Code; got != 404 {
		t.Fatal(got)
	}
}

func TestCreateAndStopUseFixedAPI(t *testing.T) {
	h, f, _ := setup(t, false, true)
	if got := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","name":"Created","goal":"Do work"}`).Code; got != 200 {
		t.Fatal(got)
	}
	last := lastAPICall(f)
	if !reflect.DeepEqual(last.args[:4], []string{"api", "send", "session:create", "--json"}) {
		t.Fatal(last)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(last.args[4]), &params); err != nil || params["projectId"] != "p1" || params["useTerminal"] != true || params["customName"] != "Created" || !strings.HasPrefix(params["newBranch"].(string), "session/trill-") || len(params) != 6 {
		t.Fatalf("create params: %v %v", params, err)
	}
	if got := request(h, "POST", "/v1/sessions/s1/stop", "secret", "").Code; got != 200 {
		t.Fatal(got)
	}
	last = lastAPICall(f)
	if !reflect.DeepEqual(last.args[:4], []string{"api", "send", "session:stop", "--json"}) {
		t.Fatal(last)
	}
	params = nil
	if err := json.Unmarshal([]byte(last.args[4]), &params); err != nil || params["sessionId"] != "s1" || len(params) != 1 {
		t.Fatalf("stop params: %v %v", params, err)
	}
}

func TestOptionalNameAndMultilineGoal(t *testing.T) {
	h, f, _ := setup(t, false, true)
	w := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","goal":"First line\nSecond line"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	last := lastAPICall(f)
	var params map[string]any
	if err := json.Unmarshal([]byte(last.args[4]), &params); err != nil {
		t.Fatal(err)
	}
	if _, ok := params["name"]; ok {
		t.Fatal("optional name was sent as empty")
	}
	for _, field := range []string{"background", "worktreeId", "worktreePath", "parentSessionId", "stackParentSessionId", "agent"} {
		if _, ok := params[field]; ok {
			t.Fatalf("unrequested create field: %s", field)
		}
	}
	if params["goal"] != "First line\nSecond line" || params["useTerminal"] != true {
		t.Fatal(params)
	}
}

func TestEndedStatusesAndResumeFallback(t *testing.T) {
	h, f, _ := setup(t, false, true)
	f.mu.Lock()
	f.sessions = append(f.sessions, rawSession{ID: "s3", Name: "Failed", ProjectID: "p1", Status: "failed", Mode: "terminal", OriginalCLISessionID: "old-cli3", CLISessionID: "different-cli3", TerminalLive: true, TmuxSession: "xirp-s3"})
	f.mu.Unlock()
	w := request(h, "GET", "/v1/sessions", "secret", "")
	var data struct {
		Sessions []Session `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 3 || !data.Sessions[1].Resumable || !data.Sessions[2].Resumable || data.Sessions[2].Attachable {
		t.Fatal(data.Sessions)
	}
	if got := request(h, "POST", "/v1/sessions/s3/resume", "secret", "").Code; got != 200 {
		t.Fatal(got)
	}
	var params map[string]string
	last := lastAPICall(f)
	if err := json.Unmarshal([]byte(last.args[4]), &params); err != nil || params["cliSessionId"] != "old-cli3" {
		t.Fatal(params, err)
	}
}

func TestMutationRequiresTargetReadbackAndSendsOnce(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		apply       bool
		want        int
	}{
		{"matching reply without state change", `{"response":{"type":"session:updated","sessionId":"s1"}}`, false, 409},
		{"unrelated target despite state change", `{"response":{"type":"session:updated","sessionId":"other"}}`, true, 409},
		{"conflicting targets despite state change", `{"response":{"type":"session:updated","sessionId":"s1","session":{"id":"other"}}}`, true, 409},
		{"generic response with target readback", `{"response":{"type":"session:updated"}}`, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := setup(t, false, true)
			f.noApply["session:update"] = !tc.apply
			f.replies["session:update"] = tc.reply
			w := request(h, "PATCH", "/v1/sessions/s1", "secret", `{"name":"Changed"}`)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if countAPICalls(f) != 1 {
				t.Fatal("mutation was sent more than once")
			}
		})
	}
}

func TestCreateNeedsCorrelatedIDAndReadback(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		apply       bool
	}{
		{"created without ID", `{"response":{"type":"session:created"}}`, true},
		{"creating without readback", `{"response":{"type":"session:creating","sessionId":"created-1"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := setup(t, false, true)
			f.noApply["session:create"] = !tc.apply
			f.afterAPI = func(_ string, rows []rawSession) {
				rows[len(rows)-1].Tags = nil // No authoritative correlation.
			}
			f.replies["session:create"] = tc.reply
			w := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","goal":"Do work"}`)
			if w.Code != 409 || !strings.Contains(w.Body.String(), "refresh sessions before retrying") {
				t.Fatalf("unexpected result: %d %s", w.Code, w.Body.String())
			}
			if countAPICalls(f) != 1 {
				t.Fatal("create was sent more than once")
			}
		})
	}
}

func TestCloseDrainsBootstrapAndRejectsNewLease(t *testing.T) {
	_, f, s := setup(t, true, true)
	release := make(chan struct{})
	f.moshStarted = make(chan struct{})
	f.moshRelease = release
	result := make(chan error, 1)
	go func() { _, err := s.Bootstrap(context.Background(), "lab-1", 80, 24); result <- err }()
	select {
	case <-f.moshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("mosh startup did not begin")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		isClosed := s.closed
		s.mu.Unlock()
		if isClosed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("close did not mark service closed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-closed:
		t.Fatal("close returned before in-flight bootstrap drained")
	default:
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, errClosed) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bootstrap did not drain")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not complete")
	}
	f.mu.Lock()
	p := f.proc
	f.mu.Unlock()
	if p == nil || p.stopped() != 1 {
		t.Fatal("spawned mosh was not stopped once")
	}
	s.mu.Lock()
	leases := len(s.leases)
	s.mu.Unlock()
	if leases != 0 {
		t.Fatal("closed service retained a lease")
	}
	if _, err := s.Bootstrap(context.Background(), "lab-1", 80, 24); !errors.Is(err, errClosed) {
		t.Fatal("bootstrap after close:", err)
	}
}

func TestCloseStopsPublishedLease(t *testing.T) {
	_, f, s := setup(t, true, true)
	info, err := s.Bootstrap(context.Background(), "lab-1", 80, 24)
	if err != nil || info.LeaseID == "" {
		t.Fatal("bootstrap:", err)
	}
	s.Close()
	f.mu.Lock()
	p := f.proc
	f.mu.Unlock()
	if p == nil || p.stopped() != 1 {
		t.Fatal("close did not stop owned lease")
	}
	if s.DeleteLease(info.LeaseID) {
		t.Fatal("closed lease remained registered")
	}
	s.Close()
	if p.stopped() != 1 {
		t.Fatal("repeated close stopped process again")
	}
}
func TestValidation(t *testing.T) {
	h, f, _ := setup(t, false, true)
	for _, body := range []string{`{"projectId":"p1","name":"n","goal":"g","extra":1}`, `{"projectId":"p1","name":"n","goal":"g"} true`, `{"projectId":"p1","name":"n","goal":"g\u0000"}`, strings.Repeat("x", maxBody+1)} {
		if got := request(h, "POST", "/v1/sessions", "secret", body).Code; got != 400 {
			t.Errorf("body rejected with %d", got)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid body executed command")
	}
	if got := request(h, "POST", "/v1/sessions/s1/mosh", "secret", `{"cols":0,"rows":24}`).Code; got != 400 {
		t.Fatal(got)
	}
	if got := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"missing","name":"n","goal":"g"}`).Code; got != 404 {
		t.Fatal(got)
	}
}
func TestIsolatedLabMoshAndLeaseOwnership(t *testing.T) {
	h, f, s := setup(t, true, true)
	w := request(h, "GET", "/v1/sessions", "secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"lab-1"`) || !strings.Contains(w.Body.String(), `"agent":"Shell"`) {
		t.Fatal(w.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/v1/sessions", `{"projectId":"terminal-lab","name":"n","goal":"g"}`}, {"PATCH", "/v1/sessions/lab-1", `{"name":"x"}`}, {"POST", "/v1/sessions/lab-1/stop", ""}, {"POST", "/v1/sessions/lab-1/resume", ""}} {
		if got := request(h, tc.method, tc.path, "secret", tc.body).Code; got != 409 {
			t.Errorf("lab mutation: %d", got)
		}
	}
	if got := request(h, "POST", "/v1/sessions/missing/mosh", "secret", `{"cols":80,"rows":24}`).Code; got != 404 {
		t.Fatal(got)
	}
	w = request(h, "POST", "/v1/sessions/lab-1/mosh", "secret", `{"cols":80,"rows":24}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var info MoshInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Port != 60001 || info.Host != "127.0.0.1" || len(info.LeaseID) != 48 {
		t.Fatal(info)
	}
	want := []string{"new", "-i", "127.0.0.1", "-p", "60000:60010", "-c", "256", "-l", "LANG=en_US.UTF-8", "--", "/usr/bin/tmux", "-S", "/tmp/trill-lab.sock", "attach-session", "-t", "=lab-one"}
	if !reflect.DeepEqual(f.moshArgs, want) || !reflect.DeepEqual(f.moshEnv, []string{"MOSH_SERVER_NETWORK_TMOUT=600", "MOSH_SERVER_SIGNAL_TMOUT=600"}) {
		t.Fatalf("mosh args/env: %q %q", f.moshArgs, f.moshEnv)
	}
	if got := request(h, "DELETE", "/v1/leases/"+strings.Repeat("0", 48), "secret", "").Code; got != 404 {
		t.Fatal(got)
	}
	if f.proc.stopped() != 0 {
		t.Fatal("foreign lease stopped process")
	}
	if got := request(h, "DELETE", "/v1/leases/"+info.LeaseID, "secret", "").Code; got != 200 {
		t.Fatal(got)
	}
	if f.proc.stopped() != 1 {
		t.Fatal("owned process was not stopped exactly once")
	}
	if s.DeleteLease(info.LeaseID) {
		t.Fatal("lease removed twice")
	}
	for _, c := range f.calls {
		if c.name == "xirp" {
			t.Fatal("lab accessed Xirp")
		}
	}
}
func TestCommandErrorRedacted(t *testing.T) {
	h, _, _ := setup(t, false, true)
	w := request(h, "POST", "/v1/sessions/s1/mosh", "secret", `{"cols":80,"rows":24}`)
	if w.Code != 409 && w.Code != 502 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), "unexpected command") {
		t.Fatal("command error leaked")
	}
	_, _ = io.Copy(io.Discard, w.Body)
}

func TestMoshTimeoutOverridesInheritedEnvironment(t *testing.T) {
	got := withEnv([]string{"MOSH_SERVER_NETWORK_TMOUT=0", "PATH=/bin", "MOSH_SERVER_SIGNAL_TMOUT=0"}, []string{"MOSH_SERVER_NETWORK_TMOUT=600", "MOSH_SERVER_SIGNAL_TMOUT=600"})
	want := []string{"PATH=/bin", "MOSH_SERVER_NETWORK_TMOUT=600", "MOSH_SERVER_SIGNAL_TMOUT=600"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment: %q", got)
	}
}

func TestCreateWaitsForFinalEvent(t *testing.T) {
	h, f, _ := setup(t, false, true)
	w := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","goal":"Do work"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	args := lastAPICall(f).args
	if len(args) != 7 || !reflect.DeepEqual(args[5:], []string{"--expect", "session:created"}) {
		t.Fatal(args)
	}
}

func TestFinishedSessionStillHasLiveTerminal(t *testing.T) {
	_, f, s := setup(t, false, true)
	f.sessions[0].Status = "finished"
	sessions, err := s.Sessions(context.Background())
	if err != nil || !sessions[0].Attachable {
		t.Fatal(sessions, err)
	}
}

func pairedHTTP(t *testing.T, control bool, legacy string) (*HTTP, *fakeRunner, *Service, *pairing.Store, string) {
	t.Helper()
	_, f, svc := setup(t, true, control)
	path := filepath.Join(t.TempDir(), "devices.json")
	store := pairing.NewStore(path)
	h := NewHTTPWithPairing(svc, legacy, store)
	t.Cleanup(h.Close)
	return h, f, svc, store, path
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
func bootstrapPhone(t *testing.T, h *HTTP, f *fakeRunner, token string) (MoshInfo, *fakeProcess) {
	t.Helper()
	w := request(h, "POST", "/v1/sessions/lab-1/mosh", token, `{"cols":80,"rows":24}`)
	var info MoshInfo
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil {
		t.Fatal("bootstrap failed", w.Code)
	}
	f.mu.Lock()
	proc := f.proc
	f.mu.Unlock()
	return info, proc
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
func TestPairEndpointAndReadOnly(t *testing.T) {
	h, f, _, store, _ := pairedHTTP(t, false, "")
	if w := request(h, "GET", "/v1/health", "", ""); w.Code != 401 {
		t.Fatal("empty legacy token authenticated")
	}
	code, _, err := store.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"code":"x","name":"Phone"}`, `{"code":"` + code + `","name":"  "}`, `{"code":"` + code + `","name":"Phone","extra":true}`, `{"code":"` + code + `","name":"Phone"} true`, strings.Repeat("x", maxBody+1)} {
		w := request(h, "POST", "/v1/pair", "", body)
		if w.Code != 400 {
			t.Fatal("bad fields not rejected", w.Code)
		}
		if strings.Contains(w.Body.String(), code) {
			t.Fatal("error leaked invitation")
		}
	}
	for _, bad := range []string{strings.Repeat("a", 64)} {
		if w := request(h, "POST", "/v1/pair", "", `{"code":"`+bad+`","name":"Phone"}`); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w := request(h, "POST", "/v1/pair", "wrong", `{"code":"`+code+`","name":"Phone"}`)
	var result map[string]string
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != 2 || len(result["token"]) != 64 || result["deviceId"] == "" {
		t.Fatal("invalid pair response", w.Code)
	}
	token := result["token"]
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("pair response cacheable")
	}
	if w := request(h, "POST", "/v1/pair", "", `{"code":"`+code+`","name":"Replay"}`); w.Code != 403 {
		t.Fatal("replay accepted", w.Code)
	}
	if w := request(h, "GET", "/v1/health", token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"readOnly":true`) {
		t.Fatal("paired auth failed", w.Code)
	}
	if w := request(h, "POST", "/v1/sessions/lab-1/mosh", token, `{"cols":80,"rows":24}`); w.Code != 403 {
		t.Fatal("pairing enabled control")
	}
	if w := request(h, "GET", "/v1/health", "secret", ""); w.Code != 401 {
		t.Fatal("legacy token used in pairing-only mode")
	}
	expired, _, err := store.CreateInvitation(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "POST", "/v1/pair", "", `{"code":"`+expired+`","name":"Phone"}`); w.Code != 403 {
		t.Fatal("expired invitation accepted", w.Code)
	}
	r := httptest.NewRequest("GET", "/v1/health", nil)
	r.Header.Add("Authorization", "Bearer "+token)
	r.Header.Add("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 401 {
		t.Fatal("multiple auth headers accepted")
	}
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("read-only pairing executed local commands")
	}
}
func TestPairedLeaseOwnershipAndIdleRevocation(t *testing.T) {
	h, f, _, store, path := pairedHTTP(t, true, "legacy")
	idA, tokenA := pairPhone(t, h, store, "A")
	_, tokenB := pairPhone(t, h, store, "B")
	infoA, procA := bootstrapPhone(t, h, f, tokenA)
	infoB, procB := bootstrapPhone(t, h, f, tokenB)
	infoLegacy, procLegacy := bootstrapPhone(t, h, f, "legacy")
	for _, token := range []string{tokenB, tokenA} {
		target := infoA.LeaseID
		if token == tokenA {
			target = infoLegacy.LeaseID
		}
		if w := request(h, "DELETE", "/v1/leases/"+target, token, ""); w.Code != 404 {
			t.Fatal("foreign lease deletion allowed", w.Code)
		}
	}
	if procA.stopped() != 0 || procB.stopped() != 0 || procLegacy.stopped() != 0 {
		t.Fatal("foreign process stopped")
	}
	// A different Store instance simulates setup's local CLI; no API traffic is
	// needed to tear down the revoked device's idle leases.
	if err := pairing.NewStore(path).Revoke(idA); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/v1/health", tokenA, ""); w.Code != 401 {
		t.Fatal("revoked API token accepted")
	}
	eventually(t, func() bool { return procA.stopped() == 1 })
	if procB.stopped() != 0 || procLegacy.stopped() != 0 {
		t.Fatal("revocation stopped another device or legacy lease")
	}
	if w := request(h, "GET", "/v1/health", tokenB, ""); w.Code != 200 {
		t.Fatal("other device revoked")
	}
	if w := request(h, "DELETE", "/v1/leases/"+infoB.LeaseID, tokenB, ""); w.Code != 200 {
		t.Fatal("owned delete failed", w.Code)
	}
	if procB.stopped() != 1 {
		t.Fatal("owned process not stopped")
	}
	// Legacy callers retain the original ability to delete any known lease.
	_, tokenC := pairPhone(t, h, store, "C")
	infoC, procC := bootstrapPhone(t, h, f, tokenC)
	if w := request(h, "DELETE", "/v1/leases/"+infoC.LeaseID, "legacy", ""); w.Code != 200 || procC.stopped() != 1 {
		t.Fatal("legacy behavior changed")
	}
	h.Close()
	h.Close()
	if procLegacy.stopped() != 0 {
		t.Fatal("HTTP close affected legacy lease")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		for _, arg := range c.args {
			if strings.HasPrefix(arg, "kill") {
				t.Fatal("revocation attempted terminal destruction")
			}
		}
	}
}
func TestRevokeDuringPairedBootstrap(t *testing.T) {
	h, f, svc, store, _ := pairedHTTP(t, true, "")
	id, token := pairPhone(t, h, store, "Phone")
	release := make(chan struct{})
	started := make(chan struct{})
	f.mu.Lock()
	f.moshStarted = started
	f.moshRelease = release
	f.mu.Unlock()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- request(h, "POST", "/v1/sessions/lab-1/mosh", token, `{"cols":80,"rows":24}`) }()
	<-started
	if err := store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	close(release)
	w := <-result
	if w.Code != 401 || strings.Contains(w.Body.String(), "leaseId") || strings.Contains(w.Body.String(), "key") {
		t.Fatal("revoked bootstrap published credentials", w.Code)
	}
	f.mu.Lock()
	proc := f.proc
	f.mu.Unlock()
	if proc.stopped() != 1 {
		t.Fatal("in-flight revoked lease survived")
	}
	svc.mu.Lock()
	leases := len(svc.leases)
	svc.mu.Unlock()
	if leases != 0 {
		t.Fatal("revoked lease retained")
	}
}
func TestPairedLeaseLifecycle(t *testing.T) {
	for _, mode := range []string{"exit", "expiry", "shutdown", "http close", "unreadable state"} {
		t.Run(mode, func(t *testing.T) {
			h, f, svc, store, path := pairedHTTP(t, true, "")
			_, token := pairPhone(t, h, store, "Phone")
			info, proc := bootstrapPhone(t, h, f, token)
			switch mode {
			case "exit":
				close(proc.done)
			case "expiry":
				svc.mu.Lock()
				svc.leases[info.LeaseID].timer.Reset(time.Millisecond)
				svc.mu.Unlock()
			case "shutdown":
				svc.Close()
			case "http close":
				var wg sync.WaitGroup
				for j := 0; j < 8; j++ {
					wg.Add(1)
					go func() { defer wg.Done(); h.Close() }()
				}
				wg.Wait()
			case "unreadable state":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if w := request(h, "GET", "/v1/health", token, ""); w.Code != 401 {
					t.Fatal("unsafe state authenticated")
				}
			}
			eventually(t, func() bool {
				h.mu.Lock()
				owners := len(h.owners)
				h.mu.Unlock()
				svc.mu.Lock()
				leases := len(svc.leases)
				svc.mu.Unlock()
				return owners == 0 && leases == 0
			})
			svc.mu.Lock()
			leases := len(svc.leases)
			svc.mu.Unlock()
			if leases != 0 {
				t.Fatal("dead lease retained")
			}
			if mode != "exit" && proc.stopped() != 1 {
				t.Fatal("process not stopped exactly once")
			}
			if mode == "shutdown" {
				select {
				case <-h.watchDone:
				case <-time.After(time.Second):
					t.Fatal("watcher survived service shutdown")
				}
			}
		})
	}
}

func TestPairedBootstrapRacingShutdown(t *testing.T) {
	for _, mode := range []string{"http close during launch", "service close before publication"} {
		t.Run(mode, func(t *testing.T) {
			h, f, svc, store, _ := pairedHTTP(t, true, "")
			_, token := pairPhone(t, h, store, "Phone")
			started, release := make(chan struct{}), make(chan struct{})
			f.mu.Lock()
			f.moshStarted = started
			f.moshRelease = release
			f.mu.Unlock()
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- request(h, "POST", "/v1/sessions/lab-1/mosh", token, `{"cols":80,"rows":24}`) }()
			<-started
			if mode == "http close during launch" {
				h.Close()
				close(release)
			} else {
				// Hold HTTP publication while Bootstrap publishes its Service lease. Then
				// finish Service.Close before HTTP can register device ownership.
				h.mu.Lock()
				close(release)
				deadline := time.Now().Add(3 * time.Second)
				for {
					svc.mu.Lock()
					live := len(svc.leases) > 0
					svc.mu.Unlock()
					if live {
						break
					}
					if time.Now().After(deadline) {
						h.mu.Unlock()
						t.Fatal("service lease not published")
					}
					time.Sleep(time.Millisecond)
				}
				svc.Close()
				h.mu.Unlock()
			}
			w := <-result
			if w.Code != 503 || strings.Contains(w.Body.String(), "leaseId") {
				t.Fatal("shutdown published credentials", w.Code)
			}
			f.mu.Lock()
			proc := f.proc
			f.mu.Unlock()
			if proc.stopped() != 1 {
				t.Fatal("shutdown lost spawned process")
			}
			eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.owners) == 0 })
		})
	}
}
