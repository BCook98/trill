package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func apiParams(t *testing.T, f *fakeRunner) map[string]any {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal([]byte(lastAPICall(f).args[4]), &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestCreationChoices(t *testing.T) {
	for _, tc := range []struct {
		name, body, api string
		worktree        bool
		branch          string
	}{
		{"legacy defaults", `{"projectId":"p1","goal":"g"}`, "session:create", true, ""},
		{"explicit worktree", `{"projectId":"p1","kind":"agent","name":"n","goal":"g","useWorktree":true}`, "session:create", true, ""},
		{"named branch", `{"projectId":"p1","goal":"g","useWorktree":true,"branch":"bcook/topic"}`, "session:create", true, "bcook/topic"},
		{"project root", `{"projectId":"p1","kind":"agent","name":"n","goal":"g","useWorktree":false}`, "session:create", false, ""},
		{"standalone shell", `{"projectId":"p1","kind":"terminal","useWorktree":false}`, "project:create-terminal", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := setup(t, false, true)
			t.Setenv("CHIRP_SESSION_ID", "ambient-parent-must-not-be-used")
			t.Setenv("CHIRP_PROJECT_ID", "other-project")
			w := request(h, "POST", "/v1/sessions", "secret", tc.body)
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			c := lastAPICall(f)
			if !reflect.DeepEqual(c.args[:4], []string{"api", "send", tc.api, "--json"}) || !reflect.DeepEqual(c.args[5:], []string{"--expect", "session:created"}) {
				t.Fatalf("argv: %+v", c)
			}
			p := apiParams(t, f)
			if p["projectId"] != "p1" || countAPICalls(f) != 1 {
				t.Fatal(p, f.calls)
			}
			if tc.api == "project:create-terminal" {
				if id, _ := p["requestId"].(string); !strings.HasPrefix(id, "trill-") {
					t.Fatal("missing terminal creation correlation", p)
				}
				if len(p) != 2 {
					t.Fatal("shell schema accepts only projectId and requestId", p)
				}
				return
			}
			tags, _ := p["tags"].([]any)
			if len(tags) != 1 || !strings.HasPrefix(tags[0].(string), "trill-") || p["requestId"] != nil {
				t.Fatal("agent creation must use supported tag correlation", p)
			}
			if p["goal"] != "g" || p["useTerminal"] != true {
				t.Fatal(p)
			}
			if tc.worktree {
				branch, _ := p["newBranch"].(string)
				if (tc.branch != "" && branch != tc.branch) || (tc.branch == "" && !strings.HasPrefix(branch, "session/trill-")) || p["worktreePath"] != nil {
					t.Fatal(p)
				}
			} else if p["worktreePath"] != "/tmp/demo" || p["newBranch"] != nil {
				t.Fatal(p)
			}
			for _, field := range []string{"kind", "useWorktree", "noWorktree", "worktreeId", "parentSessionId", "stackParentSessionId", "agent", "background"} {
				if _, ok := p[field]; ok {
					t.Fatalf("unrequested/unsupported parameter %s", field)
				}
			}
		})
	}
}

func TestCreationRejectsIncompatibleOptions(t *testing.T) {
	for _, body := range []string{
		`{"projectId":"p1","kind":"shell","goal":"g"}`,
		`{"projectId":"p1","kind":"terminal"}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":true}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false,"name":""}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false,"goal":""}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false,"branch":"topic"}`,
		`{"projectId":"p1","kind":"agent","useWorktree":false,"goal":"g","branch":"topic"}`,
		`{"projectId":"p1","goal":"g","branch":""}`,
		`{"projectId":"p1","goal":"g","branch":"../bad"}`,
		`{"projectId":"p1","goal":"g","branch":"topic.lock"}`,
		`{"projectId":"p1","goal":"g","branch":"topic//bad"}`,
		`{"projectId":"p1","goal":"g","useWorktree":null}`,
		`{"projectId":"p1","goal":"g","useWorktree":"false"}`,
		`{"projectId":"p1","goal":"g","useWorktree":0}`,
		`{"projectId":"p1","goal":"g","kind":null}`,
		`{"projectId":"p1","goal":"g","kind":""}`,
		`{"projectId":"p1","goal":"g","branch":null}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false,"name":null}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false,"goal":null}`,
		`{"projectId":"p1","kind":"agent","useWorktree":false}`,
		`{"projectId":"p1","goal":"g","worktreePath":"/untrusted"}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, f, _ := setup(t, false, true)
			if w := request(h, "POST", "/v1/sessions", "secret", body); w.Code != 400 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if len(f.calls) != 0 {
				t.Fatal("invalid options reached runner", f.calls)
			}
		})
	}
}

func TestCreationRequiresFullReadback(t *testing.T) {
	for _, body := range []string{
		`{"projectId":"p1","name":"n","goal":"g","useWorktree":true}`,
		`{"projectId":"p1","name":"n","goal":"g","useWorktree":false}`,
		`{"projectId":"p1","kind":"terminal","useWorktree":false}`,
	} {
		for _, field := range []string{"id", "project", "mode", "live", "status", "tmux", "hidden", "path", "branch", "name", "goal", "source", "tags"} {
			terminal := strings.Contains(body, `"terminal"`)
			if terminal && (field == "branch" || field == "name" || field == "goal" || field == "tags") {
				continue
			}
			if field == "branch" && strings.Contains(body, `false`) {
				continue
			}
			t.Run(body+"/"+field, func(t *testing.T) {
				h, f, _ := setup(t, false, true)
				f.afterAPI = func(_ string, rows []rawSession) {
					v := &rows[len(rows)-1]
					switch field {
					case "id":
						v.ID = "../invalid"
					case "tags":
						v.Tags = []string{"trill-unrelated"}
					case "project":
						v.ProjectID = "wrong"
					case "mode":
						v.Mode = "sdk"
					case "live":
						v.TerminalLive = false
					case "status":
						v.Status = "failed"
					case "tmux":
						v.TmuxSession = ""
					case "hidden":
						v.HiddenFromSidebar = true
					case "path":
						if strings.Contains(body, `true`) {
							v.WorktreePath = "/tmp/demo"
						} else {
							v.WorktreePath = "/wrong/path"
						}
					case "branch":
						v.Branch = "wrong"
					case "name":
						v.Name = "wrong"
					case "goal":
						v.Goal = "wrong"
					case "source":
						if terminal {
							v.Source = ""
						} else {
							v.Source = "terminal"
						}
					}
				}
				if w := request(h, "POST", "/v1/sessions", "secret", body); w.Code != 409 {
					t.Fatalf("%d: %s", w.Code, w.Body.String())
				}
				if countAPICalls(f) != 1 {
					t.Fatal("create retried")
				}
			})
		}
	}
}

func TestCreationReplyAndProjectFailures(t *testing.T) {
	for _, api := range []string{"session:create", "project:create-terminal"} {
		for _, reply := range []string{
			`{"response":{"type":"session:created","requestId":"$REQUEST_ID"}}`,
			`{"response":{"type":"session:created","sessionId":"created-1"}}`,
			`{"response":{"type":"session:created","sessionId":"created-1","requestId":"unrelated"}}`,
			`{"response":{"type":"session:created","sessionId":"created-1","session":{"id":"other"},"requestId":"$REQUEST_ID"}}`,
			`{"response":{"type":"session:created","sessionId":"s1","requestId":"$REQUEST_ID"}}`,
			`{"response":{"type":"session:creating","sessionId":"created-1","requestId":"$REQUEST_ID"}}`,
			`invalid JSON`,
		} {
			t.Run(api+reply, func(t *testing.T) {
				h, f, _ := setup(t, false, true)
				f.replies[api] = reply
				if api == "session:create" {
					f.noApply[api] = true // A reply alone cannot confirm agent creation.
				}
				body := `{"projectId":"p1","goal":"g"}`
				if api == "project:create-terminal" {
					body = `{"projectId":"p1","kind":"terminal","useWorktree":false}`
				}
				if w := request(h, "POST", "/v1/sessions", "secret", body); w.Code != 409 || countAPICalls(f) != 1 {
					t.Fatal(w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
	for _, path := range []string{"", "relative/path"} {
		h, f, _ := setup(t, false, true)
		b, _ := json.Marshal([]Project{{ID: "p1", Path: path}})
		f.projectsJSON = string(b)
		if w := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","goal":"g","useWorktree":false}`); w.Code != 502 || countAPICalls(f) != 0 {
			t.Fatal(w.Code, f.calls)
		}
	}
}

// The generic API command selects replies by type only; its session:created
// event may belong to another client. Only our persisted nonce can correlate it.
func TestAgentCreationTagCorrelation(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		for _, outcome := range []string{"applied", "unrelated reply", "malformed reply", "lost response", "unapplied", "missing tag", "wrong tag", "ambiguous tag", "invalid ID"} {
			t.Run(fmt.Sprintf("worktree=%t/%s", worktree, outcome), func(t *testing.T) {
				h, f, _ := setup(t, false, true)
				want := 200
				switch outcome {
				case "unrelated reply":
					f.replies["session:create"] = `{"response":{"type":"session:created","session":{"id":"s1"},"requestId":"another-client"}}`
				case "malformed reply":
					f.replies["session:create"] = `invalid JSON`
				case "lost response":
					f.apiErrors = map[string]bool{"session:create": true}
				case "unapplied":
					f.noApply["session:create"] = true
					// An existing session with the same goal/name is not evidence
					// that this attempt created anything.
					f.sessions[0].Name, f.sessions[0].Goal = "n", "g"
					f.sessions[0].WorktreePath = "/tmp/demo"
					want = 409
				case "missing tag", "wrong tag", "ambiguous tag", "invalid ID":
					want = 409
					f.afterAPI = func(_ string, rows []rawSession) {
						v := &rows[len(rows)-1]
						switch outcome {
						case "missing tag":
							v.Tags = nil
						case "wrong tag":
							v.Tags = []string{"trill-another-attempt"}
						case "ambiguous tag":
							rows[0].Tags = append([]string(nil), v.Tags...)
						case "invalid ID":
							v.ID = ""
						}
					}
				}
				body := fmt.Sprintf(`{"projectId":"p1","kind":"agent","name":"n","goal":"g","useWorktree":%t}`, worktree)
				w := request(h, "POST", "/v1/sessions", "secret", body)
				if w.Code != want || countAPICalls(f) != 1 {
					t.Fatalf("status=%d want=%d mutations=%d", w.Code, want, countAPICalls(f))
				}
			})
		}
	}
}

type delayedCreationReadback struct {
	Runner
	reads int
}

func (r *delayedCreationReadback) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.Runner.Run(ctx, name, args...)
	if err != nil || !reflect.DeepEqual(args, []string{"session", "list", "--all", "--json"}) {
		return out, err
	}
	r.reads++
	if r.reads <= 2 {
		var rows []rawSession
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, err
		}
		rows[len(rows)-1].TerminalLive = false
		return json.Marshal(rows)
	}
	return out, nil
}

func TestAgentCreationRetriesOnlyReadback(t *testing.T) {
	h, f, svc := setup(t, false, true)
	r := &delayedCreationReadback{Runner: f}
	svc.runner = r
	f.apiErrors = map[string]bool{"session:create": true}
	w := request(h, "POST", "/v1/sessions", "secret", `{"projectId":"p1","kind":"agent","goal":"g","useWorktree":false}`)
	if w.Code != 200 || countAPICalls(f) != 1 || r.reads != 3 {
		t.Fatalf("status=%d mutations=%d reads=%d", w.Code, countAPICalls(f), r.reads)
	}
}

func TestArchivePreservesSessionAndLease(t *testing.T) {
	h, f, svc := setup(t, false, true)
	p := &fakeProcess{done: make(chan struct{})}
	svc.leases["test-lease"] = &lease{proc: p}
	before := f.sessions[0]
	for i := 0; i < 2; i++ {
		if w := request(h, "POST", "/v1/sessions/s1/archive", "secret", ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if countAPICalls(f) != 1 || lastAPICall(f).args[2] != "session-sidebar:set" || !reflect.DeepEqual(apiParams(t, f), map[string]any{"sessionId": "s1", "hidden": true}) {
		t.Fatal(f.calls)
	}
	before.HiddenFromSidebar = true
	if !reflect.DeepEqual(f.sessions[0], before) || len(svc.leases) != 1 || p.stopped() != 0 || len(f.moshArgs) != 0 {
		t.Fatal("archive changed lifecycle/lease", f.sessions[0], svc.leases, p.stopped())
	}
	if w := request(h, "POST", "/v1/sessions/s1/resume", "secret", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	before.HiddenFromSidebar = false
	if !reflect.DeepEqual(f.sessions[0], before) || countAPICalls(f) != 2 || p.stopped() != 0 || !reflect.DeepEqual(apiParams(t, f), map[string]any{"sessionId": "s1", "hidden": false}) {
		t.Fatal("reopen restarted or changed identity/lease", f.calls, f.sessions[0])
	}
}

func TestArchiveAndReopenReadbackFailures(t *testing.T) {
	for _, action := range []string{"archive", "resume"} {
		for _, failure := range []string{"no change", "wrong reply", "conflicting reply", "wrong project", "unknown status", "lost response applied", "lost response unapplied"} {
			if action == "archive" && failure == "unknown status" {
				continue
			}
			t.Run(action+failure, func(t *testing.T) {
				h, f, _ := setup(t, false, true)
				f.sessions[0].HiddenFromSidebar = action == "resume"
				want := 409
				switch failure {
				case "no change":
					f.noApply["session-sidebar:set"] = true
				case "wrong reply":
					f.replies["session-sidebar:set"] = `{"response":{"type":"session:updated","sessionId":"s2"}}`
				case "conflicting reply":
					f.replies["session-sidebar:set"] = `{"response":{"type":"session:updated","sessionId":"s1","session":{"id":"s2"}}}`
				case "wrong project":
					f.afterAPI = func(_ string, rows []rawSession) { rows[0].ProjectID = "other" }
				case "unknown status":
					f.afterAPI = func(_ string, rows []rawSession) { rows[0].Status = "new-status" }
				case "lost response applied":
					f.apiErrors = map[string]bool{"session-sidebar:set": true}
					want = 200
				case "lost response unapplied":
					f.apiErrors = map[string]bool{"session-sidebar:set": true}
					f.noApply["session-sidebar:set"] = true
				}
				w := request(h, "POST", "/v1/sessions/s1/"+action, "secret", "")
				if w.Code != want || countAPICalls(f) != 1 {
					t.Fatal(w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
}

func TestEndedArchivedResumeOrderingAndPartialFailure(t *testing.T) {
	for _, failure := range []string{"", "resume unapplied", "resume wrong target", "resume lost response", "unhide unapplied", "unhide wrong target", "resume also unhides"} {
		t.Run(failure, func(t *testing.T) {
			h, f, _ := setup(t, false, true)
			f.sessions[1].HiddenFromSidebar = true
			want, calls := 200, 2
			switch failure {
			case "resume unapplied":
				f.noApply["session:resume"] = true
				want, calls = 409, 1
			case "resume wrong target":
				f.replies["session:resume"] = `{"response":{"type":"session:updated","sessionId":"s1"}}`
				want, calls = 409, 1
			case "resume lost response":
				f.apiErrors = map[string]bool{"session:resume": true}
			case "unhide unapplied":
				f.noApply["session-sidebar:set"] = true
				want = 409
			case "unhide wrong target":
				f.replies["session-sidebar:set"] = `{"response":{"type":"session:updated","sessionId":"s1"}}`
				want = 409
			case "resume also unhides":
				f.afterAPI = func(_ string, rows []rawSession) { rows[1].HiddenFromSidebar = false }
				calls = 1
			}
			w := request(h, "POST", "/v1/sessions/s2/resume", "secret", "")
			if w.Code != want || countAPICalls(f) != calls {
				t.Fatal(w.Code, w.Body.String(), f.calls)
			}
			var kinds []string
			for _, c := range f.calls {
				if len(c.args) > 2 && c.args[0] == "api" {
					kinds = append(kinds, c.args[2])
				}
			}
			if kinds[0] != "session:resume" || (calls == 2 && kinds[1] != "session-sidebar:set") {
				t.Fatal(kinds)
			}
			if failure == "unhide unapplied" && (!activeStatus(f.sessions[1].Status) || !f.sessions[1].HiddenFromSidebar) {
				t.Fatal("partial outcome lost", f.sessions[1])
			}
			if want == 200 && (!activeStatus(f.sessions[1].Status) || f.sessions[1].HiddenFromSidebar || f.sessions[1].ID != "s2") {
				t.Fatal(f.sessions[1])
			}
			if failure == "unhide unapplied" {
				f.noApply["session-sidebar:set"] = false
				if w := request(h, "POST", "/v1/sessions/s2/resume", "secret", ""); w.Code != 200 || countAPICalls(f) != 3 || lastAPICall(f).args[2] != "session-sidebar:set" {
					t.Fatal("manual refresh/reopen restarted", w.Code, f.calls)
				}
			}
		})
	}
}

func TestArchivedClassificationAndUnknownStatus(t *testing.T) {
	for _, status := range []string{"running", "waiting", "idle", "finished", "completed", "failed", "stopped", "waiting_on_parent", "unknown"} {
		for _, hidden := range []bool{false, true} {
			for _, history := range []bool{false, true} {
				t.Run(status+"/"+strings.Join([]string{map[bool]string{true: "hidden", false: "visible"}[hidden], map[bool]string{true: "history", false: "no-history"}[history]}, "/"), func(t *testing.T) {
					h, f, _ := setup(t, false, true)
					f.sessions = f.sessions[:1]
					v := &f.sessions[0]
					v.Status = status
					v.HiddenFromSidebar = hidden
					v.CLISessionID = ""
					if history {
						v.CLISessionID = "history"
					}
					w := request(h, "GET", "/v1/sessions", "secret", "")
					var data struct {
						Sessions []Session `json:"sessions"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 || len(data.Sessions) != 1 {
						t.Fatal(w.Code, w.Body.String(), err)
					}
					s := data.Sessions[0]
					wantResume := (hidden && activeStatus(status)) || (endedStatus(status) && history)
					if s.Status != status || s.Archived != (hidden || endedStatus(status)) || s.Resumable != wantResume {
						t.Fatal(s)
					}
					if !wantResume {
						if w := request(h, "POST", "/v1/sessions/s1/resume", "secret", ""); w.Code != 409 || countAPICalls(f) != 0 {
							t.Fatal("unsupported resume mutated", w.Code, f.calls)
						}
					}
				})
			}
		}
	}
}

func TestLifecycleAuthorizationAndLabGuards(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/sessions/s1/archive", ""},
		{"/v1/sessions/s1/resume", ""},
		{"/v1/sessions", `{"projectId":"p1","kind":"terminal","useWorktree":false}`},
		{"/v1/sessions", `{"projectId":"p1","kind":"agent","goal":"g","useWorktree":false}`},
	} {
		for _, mode := range []string{"unauthorized", "read only", "lab"} {
			t.Run(tc.path+tc.body+mode, func(t *testing.T) {
				h, f, _ := setup(t, mode == "lab", mode != "read only")
				token, want := "secret", 409
				if mode == "unauthorized" {
					token, want = "wrong", 401
				}
				if mode == "read only" {
					want = 403
				}
				w := request(h, "POST", tc.path, token, tc.body)
				if w.Code != want || len(f.calls) != 0 {
					t.Fatal(w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
	h, f, _ := setup(t, false, true)
	for _, tc := range []struct {
		id   string
		want int
	}{{"missing", 404}, {"bad.id", 400}} {
		if w := request(h, "POST", "/v1/sessions/"+tc.id+"/archive", "secret", ""); w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if countAPICalls(f) != 0 {
		t.Fatal("invalid target mutated")
	}
}
