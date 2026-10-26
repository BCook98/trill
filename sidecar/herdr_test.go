package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const herdrFixture = `{"result":{"type":"session_snapshot","snapshot":{"version":"0.9.5","protocol":22,"workspaces":[{"workspace_id":"w1","label":"Prototype"}],"panes":[{"pane_id":"w1:p1","terminal_id":"term_original","workspace_id":"w1","cwd":"/tmp","agent":"claude","agent_status":"blocked"}]}}}`

type herdrRunner struct {
	fakeRunner
	preflightEnv []string
	envs         [][]string
	snapshot     string
	commands     []call
}

func (r *herdrRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, call{name, append([]string(nil), args...)})
	if name == "/tmp/herdr" && reflect.DeepEqual(args, []string{"api", "schema", "--json"}) {
		return []byte(`{"protocol":22}`), nil
	}
	if name == "/tmp/herdr" && len(args) == 6 && args[2] == "pane" && args[3] == "rename" {
		var value map[string]any
		if err := json.Unmarshal([]byte(r.snapshot), &value); err != nil {
			return nil, err
		}
		panes := value["result"].(map[string]any)["snapshot"].(map[string]any)["panes"].([]any)
		for _, entry := range panes {
			pane := entry.(map[string]any)
			if pane["pane_id"] == args[4] {
				pane["label"] = args[5]
				b, err := json.Marshal(value)
				r.snapshot = string(b)
				return []byte(`{"result":{}}`), err
			}
		}
		return nil, errors.New("pane not found")
	}
	if name != "/tmp/herdr" || !reflect.DeepEqual(args, []string{"--session", "test-server", "api", "snapshot"}) {
		return nil, errors.New("unexpected command")
	}
	return []byte(r.snapshot), nil
}
func (r *herdrRunner) CheckHerdrAttach(_ context.Context, _ string, _ []string, env []string) error {
	r.preflightEnv = append([]string(nil), env...)
	return nil
}

func herdrService(t *testing.T, r *herdrRunner) *Service {
	t.Helper()
	s, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSession: "test-server", AllowControl: true}, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func TestHerdrMetadataAndAttachDoNotInvokeXirpOrTmux(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	rows, err := s.Sessions(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("listing: %v %v", rows, err)
	}
	row := rows[0]
	if !row.Attachable || row.Archived || row.Resumable || !idPattern.MatchString(row.ID) || row.ActivityState != "waiting_for_input" {
		t.Fatalf("unexpected mapping: %+v", row)
	}
	info, err := s.Bootstrap(context.Background(), row.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !s.DeleteLease(info.LeaseID) {
		t.Fatal("lease missing")
	}
	joined := strings.Join(r.moshArgs, " ")
	if !strings.Contains(joined, "-- /tmp/herdr --session test-server terminal attach term_original") || strings.Contains(joined, "takeover") {
		t.Fatalf("unsafe attach: %v", r.moshArgs)
	}
	for _, c := range r.commands {
		if c.name != "/tmp/herdr" {
			t.Fatalf("cross-backend fallback: %+v", c)
		}
	}
}
func TestHerdrTerminalIdentitySurvivesPaneMoveAndNamespacesServers(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	before, _ := s.Sessions(context.Background())
	r.snapshot = strings.ReplaceAll(herdrFixture, "w1", "w2")
	after, err := s.Sessions(context.Background())
	if err != nil || before[0].ID != after[0].ID || before[0].ProjectID == after[0].ProjectID {
		t.Fatalf("pane move: %v %v", after, err)
	}
	old := s.herdrID("terminal", "term_original")
	s.config.HerdrSession = "other-server"
	if old == s.herdrID("terminal", "term_original") {
		t.Fatal("server identity collision")
	}
}
func TestHerdrRejectsUntrustedOrIncompatibleMetadata(t *testing.T) {
	cases := map[string]string{
		"error": `{"error":{"code":"unavailable"}}`,

		"protocol":          strings.Replace(herdrFixture, `"protocol":22`, `"protocol":23`, 1),
		"relative cwd":      strings.Replace(herdrFixture, "/tmp", "relative", 1),
		"option terminal":   strings.Replace(herdrFixture, "term_original", "--takeover", 1),
		"foreign workspace": strings.Replace(herdrFixture, `"workspace_id":"w1","cwd"`, `"workspace_id":"w2","cwd"`, 1),
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			r := &herdrRunner{snapshot: fixture}
			s := herdrService(t, r)
			if _, err := s.Sessions(context.Background()); err == nil {
				t.Fatal("accepted bad metadata")
			}
			if len(r.calls) != 0 {
				t.Fatal("unexpected mosh launch")
			}
		})
	}
}
func TestHerdrDoesNotMutateOrFallbackToXirp(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	ctx := context.Background()
	for _, err := range []error{s.Create(ctx, "x", "name", "goal", CreateOptions{}), s.Stop(ctx, "x"), s.Resume(ctx, "x"), s.Archive(ctx, "x")} {
		if !errors.Is(err, errUnsupported) {
			t.Fatalf("unsupported action: %v", err)
		}
	}
	if len(r.commands) != 0 || s.sessionManagement() {
		t.Fatal("backend performed unsupported action")
	}
}
func TestHerdrLabRejectsForeignRoot(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	s.config.HerdrLabRoot = t.TempDir()
	if _, err := s.Sessions(context.Background()); err == nil {
		t.Fatal("lab accepted terminal outside configured root")
	}
}
func TestHerdrConfigurationRequiresExplicitIsolation(t *testing.T) {
	for _, c := range []Config{
		{HerdrPath: "herdr", HerdrSession: "test-server"}, {HerdrPath: "/tmp/herdr"},
		{HerdrPath: "/tmp/herdr", HerdrSession: "default"}, {HerdrPath: "/tmp/herdr", HerdrSession: "../other"},
		{HerdrPath: "/tmp/herdr", HerdrSession: "test-server", TmuxSocket: "/tmp/lab.sock"},
		{HerdrSession: "test-server"}, {HerdrLabRoot: "/tmp"},
	} {
		if _, err := NewService(c, nil); err == nil {
			t.Fatalf("accepted unsafe configuration: %+v", c)
		}
	}
}
func TestHerdrDoneIsLiveAndRestoreErrorsPreventAttach(t *testing.T) {
	r := &herdrRunner{snapshot: strings.Replace(herdrFixture, "blocked", "done", 1)}
	s := herdrService(t, r)
	rows, err := s.Sessions(context.Background())
	if err != nil || rows[0].Archived || !rows[0].Attachable || rows[0].ActivityState != "idle" {
		t.Fatalf("done mapping: %v %v", rows, err)
	}
	r.snapshot = strings.Replace(r.snapshot, `"agent_status":"done"`, `"agent_status":"done","restore_error":"shell unavailable"`, 1)
	rows, err = s.Sessions(context.Background())
	if err != nil || rows[0].Attachable {
		t.Fatalf("restore error: %v %v", rows, err)
	}
	if _, err = s.Bootstrap(context.Background(), rows[0].ID, 80, 24); !errors.Is(err, errNotAttachable) {
		t.Fatal(err)
	}
}
func TestHerdrHealthDisablesOnlySessionManagement(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	token := strings.Repeat("a", 64)
	h := NewHTTP(s, token)
	t.Cleanup(h.Close)
	response := request(h, "GET", "/v1/health", token, "")
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["sessionManagement"] != false || health["readOnly"] != false || health["backend"] != "herdr" {
		t.Fatalf("health: %v", health)
	}
}

func TestHerdrAcceptsCompatiblePatchAndRenamesCurrentPane(t *testing.T) {
	r := &herdrRunner{snapshot: strings.Replace(herdrFixture, "0.9.5", "0.9.6", 1)}
	s := herdrService(t, r)
	rows, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rows[0].Capabilities.Rename || rows[0].Capabilities.Stop || rows[0].Capabilities.Archive {
		t.Fatal(rows[0].Capabilities)
	}
	if err = s.Rename(context.Background(), rows[0].ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Sessions(context.Background())
	if err != nil || rows[0].Name != "Renamed" {
		t.Fatalf("rename: %v %v", rows, err)
	}
}

type herdrCreateRunner struct {
	herdrRunner
	reply     string
	newPaneID string
	creates   int
}

func (r *herdrCreateRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) >= 4 && args[2] == "tab" && args[3] == "create" {
		r.creates++
		r.commands = append(r.commands, call{name, append([]string(nil), args...)})
		var value map[string]any
		_ = json.Unmarshal([]byte(r.snapshot), &value)
		snapshot := value["result"].(map[string]any)["snapshot"].(map[string]any)
		panes := snapshot["panes"].([]any)
		paneID := r.newPaneID
		if paneID == "" {
			paneID = args[5] + ":p2"
		}
		snapshot["panes"] = append(panes, map[string]any{"pane_id": paneID, "terminal_id": "term_new", "workspace_id": args[5], "cwd": "/tmp", "agent_status": "unknown"})
		b, _ := json.Marshal(value)
		r.snapshot = string(b)
		return []byte(r.reply), nil
	}
	return r.herdrRunner.Run(ctx, name, args...)
}
func TestHerdrCreationConfirmsOnlyTheReturnedTerminalAndNeverResends(t *testing.T) {
	for _, test := range []struct {
		name, reply string
		success     bool
	}{
		{"confirmed", `{"result":{"root_pane":{"terminal_id":"term_new","workspace_id":"w1"}}}`, true},
		{"lost response", `{}`, false},
		{"wrong workspace", `{"result":{"root_pane":{"terminal_id":"term_new","workspace_id":"w2"}}}`, false},
		{"unknown terminal", `{"result":{"root_pane":{"terminal_id":"term_other","workspace_id":"w1"}}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &herdrCreateRunner{herdrRunner: herdrRunner{snapshot: herdrFixture}, reply: test.reply}
			s, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSessions: "test-server"}, r)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			projects, err := s.Projects(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = s.Create(context.Background(), projects[0].ID, "", "", CreateOptions{Kind: "terminal"})
			if (err == nil) != test.success || (!test.success && !errors.Is(err, errOutcomeUnknown)) || r.creates != 1 {
				t.Fatalf("outcome=%v creates=%d", err, r.creates)
			}
		})
	}
}

func (r *herdrRunner) RunWithEnv(ctx context.Context, name string, args, env []string) ([]byte, error) {
	r.envs = append(r.envs, append([]string(nil), env...))
	return r.Run(ctx, name, args...)
}
func (r *herdrCreateRunner) RunWithEnv(ctx context.Context, name string, args, env []string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func TestHerdrConfigHomePinsEverySubprocessAndPublicIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/unrelated-launchd-config")
	r := &herdrRunner{snapshot: herdrFixture}
	s, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSessions: "test-server", HerdrConfigHome: "/tmp/selected-config"}, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	rows, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rename(context.Background(), rows[0].ID, "Pinned"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bootstrap(context.Background(), rows[0].ID, 80, 24); err != nil {
		t.Fatal(err)
	}
	for _, env := range append(r.envs, r.moshEnv, r.preflightEnv) {
		if !strings.Contains(strings.Join(env, "\n"), "XDG_CONFIG_HOME=/tmp/selected-config") {
			t.Fatalf("subprocess not pinned: %v", env)
		}
	}
	before := s.herdrID("terminal", "term_original")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/another-shell-config")
	if s.herdrID("terminal", "term_original") != before {
		t.Fatal("environment changed stable session identity")
	}
}

// Herdr 0.9.5 workspace.rs encodes public numbers with this alphabet, including
// 10 -> A, 32 -> 0, and 33 -> 11. I, L, O, and U are not public-ID digits.
func TestHerdrPublicIDAlphabet(t *testing.T) {
	for _, number := range strings.Split("1 9 A Z 0 11 1A 9Z 123456789ABCDEFGHJKMNPQRSTVWXYZ0", " ") {
		t.Run(number, func(t *testing.T) {
			fixture := strings.ReplaceAll(herdrFixture, "w1", "w"+number)
			fixture = strings.Replace(fixture, ":p1", ":p"+number, 1)
			s := herdrService(t, &herdrRunner{snapshot: fixture})
			c, err := s.catalog(context.Background())
			if err != nil || len(c.Projects) != 1 || len(c.Raw) != 1 || len(c.Providers) != 1 || !c.Providers[0].Available {
				t.Fatalf("valid public number %q: %+v %v", number, c, err)
			}
		})
	}
}

func TestHerdrRejectsInvalidPublicIDs(t *testing.T) {
	for _, number := range []string{"", "I", "L", "O", "U", "a", "A_", "-A", "A/B", "A;B", "A B", "A:p1"} {
		t.Run("workspace/"+number, func(t *testing.T) {
			// An empty workspace must also be validated before its ID can be
			// used as a tab-create argument; no pane validation can catch it.
			fixture := strings.Replace(herdrFixture, `"workspace_id":"w1"`, `"workspace_id":"w`+number+`"`, 1)
			var value map[string]any
			if err := json.Unmarshal([]byte(fixture), &value); err != nil {
				t.Fatal(err)
			}
			value["result"].(map[string]any)["snapshot"].(map[string]any)["panes"] = []any{}
			b, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			s := herdrService(t, &herdrRunner{snapshot: string(b)})
			if _, err = s.Projects(context.Background()); !errors.Is(err, errUnavailable) {
				t.Fatalf("accepted invalid workspace ID: %v", err)
			}
		})
		t.Run("pane/"+number, func(t *testing.T) {
			fixture := strings.Replace(herdrFixture, "w1:p1", "w1:p"+number, 1)
			s := herdrService(t, &herdrRunner{snapshot: fixture})
			if _, err := s.Sessions(context.Background()); !errors.Is(err, errUnavailable) {
				t.Fatalf("accepted invalid pane ID: %v", err)
			}
		})
	}
}

func TestHerdrLetterIDsListingRenameAndCreateReadback(t *testing.T) {
	fixture := strings.ReplaceAll(herdrFixture, "w1", "w1A")
	fixture = strings.Replace(fixture, ":p1", ":p9", 1)
	r := &herdrCreateRunner{
		herdrRunner: herdrRunner{snapshot: fixture},
		newPaneID:   "w1A:pA",
		reply:       `{"result":{"root_pane":{"terminal_id":"term_new","workspace_id":"w1A"}}}`,
	}
	s, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSessions: "test-server"}, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].HerdrWorkspace != "w1A" {
		t.Fatalf("projects: %+v %v", projects, err)
	}
	rows, err := s.Sessions(ctx)
	if err != nil || len(rows) != 1 || !rows[0].Attachable || rows[0].ProjectID != projects[0].ID {
		t.Fatalf("sessions: %+v %v", rows, err)
	}
	originalID := rows[0].ID
	if err = s.Rename(ctx, originalID, "Letter workspace"); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(ctx, projects[0].ID, "", "", CreateOptions{Kind: "terminal"}); err != nil {
		t.Fatalf("tenth pane creation/readback: %v", err)
	}
	rows, err = s.Sessions(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("listing after creation: %+v %v", rows, err)
	}
	createdID := s.herdrID("terminal", "term_new")
	if rows[0].ID != originalID || rows[0].Name != "Letter workspace" || rows[1].ID != createdID || rows[1].ProjectID != projects[0].ID || !rows[1].Attachable {
		t.Fatalf("identity/readback: %+v", rows)
	}
	if err = s.Rename(ctx, createdID, "Tenth pane"); err != nil {
		t.Fatalf("alphabetic pane rename/readback: %v", err)
	}
	rows, err = s.Sessions(ctx)
	if err != nil || len(rows) != 2 || rows[0].Name != "Letter workspace" || rows[1].Name != "Tenth pane" {
		t.Fatalf("rename targeted wrong pane: %+v %v", rows, err)
	}
	var mutations [][]string
	for _, command := range r.commands {
		if len(command.args) > 3 && (command.args[2] == "pane" || command.args[2] == "tab") {
			mutations = append(mutations, command.args)
		}
	}
	want := [][]string{
		{"--session", "test-server", "pane", "rename", "w1A:p9", "Letter workspace"},
		{"--session", "test-server", "tab", "create", "--workspace", "w1A", "--no-focus"},
		{"--session", "test-server", "pane", "rename", "w1A:pA", "Tenth pane"},
	}
	if !reflect.DeepEqual(mutations, want) || r.creates != 1 {
		t.Fatalf("mutations: %v (creates=%d), want %v", mutations, r.creates, want)
	}
}

const herdrHierarchyFixture = `{"result":{"type":"session_snapshot","snapshot":{"protocol":22,"workspaces":[{"workspace_id":"w1","label":"trill","worktree":{"repo_key":"/repo/.git","checkout_path":"/repo","is_linked_worktree":false}},{"workspace_id":"w2","label":"calm-meadow","focused":true,"worktree":{"repo_key":"/repo/.git","checkout_path":"/worktree","is_linked_worktree":true}},{"workspace_id":"w3","label":"trill","worktree":{"repo_key":"/different/.git","checkout_path":"/different","is_linked_worktree":true}}],"tabs":[{"tab_id":"w1:t1","workspace_id":"w1","label":"Rebase","number":1},{"tab_id":"w2:t1","workspace_id":"w2","label":"1","number":1}],"panes":[{"pane_id":"w1:p1","terminal_id":"term_original","workspace_id":"w1","tab_id":"w1:t1","cwd":"/repo","terminal_title_stripped":"Terminal title"},{"pane_id":"w2:p1","terminal_id":"term_child","workspace_id":"w2","tab_id":"w2:t1","cwd":"/worktree","terminal_title_stripped":"Improve Herdr support"}]}}}`

type herdrHierarchyRunner struct {
	herdrRunner
	mutationReply string
	omitReadback  bool
	mutations     int
}

func (r *herdrHierarchyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) >= 4 && args[2] == "worktree" && args[3] == "list" {
		return []byte(`{"result":{"type":"worktree_list","source":{"repo_key":"/repo/.git","source_workspace_id":"w1"},"worktrees":[{"open_workspace_id":"w1","path":"/repo","branch":"main","is_linked_worktree":false},{"open_workspace_id":"w2","path":"/worktree","branch":"worktree/calm-meadow","is_linked_worktree":true}]}}`), nil
	}
	if len(args) >= 4 && (args[2] == "workspace" || args[2] == "worktree") && args[3] == "create" {
		r.commands = append(r.commands, call{name, append([]string(nil), args...)})
		r.mutations++
		if !r.omitReadback {
			var value map[string]any
			_ = json.Unmarshal([]byte(r.snapshot), &value)
			snapshot := value["result"].(map[string]any)["snapshot"].(map[string]any)
			snapshot["workspaces"] = append(snapshot["workspaces"].([]any), map[string]any{"workspace_id": "w4", "label": "New space"})
			snapshot["panes"] = append(snapshot["panes"].([]any), map[string]any{"pane_id": "w4:p1", "terminal_id": "term_created", "workspace_id": "w4", "cwd": "/tmp"})
			data, _ := json.Marshal(value)
			r.snapshot = string(data)
		}
		return []byte(r.mutationReply), nil
	}
	return r.herdrRunner.Run(ctx, name, args...)
}
func (r *herdrHierarchyRunner) RunWithEnv(ctx context.Context, name string, args, env []string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func hierarchyService(t *testing.T, runner *herdrHierarchyRunner) *Service {
	t.Helper()
	service, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSessions: "test-server", AllowControl: true}, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestHerdrHierarchyUsesProvenanceTabLabelsAndNativeBranches(t *testing.T) {
	runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: herdrHierarchyFixture}}
	service := hierarchyService(t, runner)
	catalog, err := service.catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projects := catalog.Projects
	if projects[1].ParentID != projects[0].ID || projects[2].ParentID != "" || !projects[1].IsWorktree || !projects[1].Focused || projects[1].WorkspaceOrder != 2 {
		t.Fatalf("hierarchy: %+v", projects)
	}
	if projects[0].Branch != "main" || projects[1].Branch != "worktree/calm-meadow" || !projects[0].Capabilities.Worktree {
		t.Fatalf("branches/capabilities: %+v", projects)
	}
	if projects[1].Capabilities.Worktree || projects[2].Capabilities.Worktree || !projects[1].Capabilities.CreateTerminal || !projects[1].Capabilities.Files {
		t.Fatalf("linked worktree capabilities: %+v %+v", projects[1].Capabilities, projects[2].Capabilities)
	}
	rows := catalog.sessions()
	if rows[0].Name != "Rebase" || rows[1].Name != "Improve Herdr support" || rows[1].Branch != projects[1].Branch {
		t.Fatalf("names: %+v", rows)
	}
	if !catalog.Providers[0].CreateSpace {
		t.Fatal("missing space creation capability")
	}
}

func TestHerdrCreationRoutesNativeWorkspacesAndWorktreesOnce(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		t.Run(fmt.Sprint(worktree), func(t *testing.T) {
			resultType := "workspace_created"
			if worktree {
				resultType = "worktree_created"
			}
			runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: herdrHierarchyFixture}, mutationReply: `{"result":{"type":"` + resultType + `","workspace":{"workspace_id":"w4"},"root_pane":{"workspace_id":"w4","terminal_id":"term_created"}}}`}
			service := hierarchyService(t, runner)
			var err error
			if worktree {
				err = service.Create(context.Background(), service.herdrID("workspace", "w1"), "Phone work", "", CreateOptions{Kind: "worktree", UseWorktree: true, Branch: "feature/phone"})
			} else {
				err = service.CreateSpace(context.Background(), service.sourceKey, "Phone space", t.TempDir())
			}
			if err != nil || runner.mutations != 1 {
				t.Fatalf("creation: %v, mutations=%d", err, runner.mutations)
			}
			for _, command := range runner.commands {
				joined := strings.Join(command.args, " ")
				if strings.Contains(joined, " create ") && (!strings.Contains(joined, "--no-focus") || strings.Contains(joined, "--trust-repository")) {
					t.Fatal(joined)
				}
			}
		})
	}
}

func TestHerdrSpaceCreationUncertainRepliesNeverReplay(t *testing.T) {
	for _, reply := range []string{`{}`, `{"result":{"type":"workspace_created","workspace":{"workspace_id":"w1"},"root_pane":{"workspace_id":"w1","terminal_id":"term_original"}}}`, `{"result":{"type":"workspace_created","workspace":{"workspace_id":"w4"},"root_pane":{"workspace_id":"w4","terminal_id":"term_created"}}}`} {
		runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: herdrHierarchyFixture}, mutationReply: reply, omitReadback: true}
		service := hierarchyService(t, runner)
		err := service.CreateSpace(context.Background(), service.sourceKey, "Phone space", t.TempDir())
		if !errors.Is(err, errOutcomeUnknown) || runner.mutations != 1 {
			t.Fatalf("%s: %v, mutations=%d", reply, err, runner.mutations)
		}
	}
}

func TestHerdrSpaceHTTPRequiresControlAndValidDirectory(t *testing.T) {
	runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: herdrHierarchyFixture}}
	service := hierarchyService(t, runner)
	handler := NewHTTP(service, "test-token")
	defer handler.Close()
	for _, body := range []string{`{"providerId":"herdr:test-server","name":"Test","directory":"relative"}`, `{"providerId":"herdr:test-server","name":"","directory":"/tmp"}`} {
		response := request(handler, "POST", "/v1/spaces", "test-token", body)
		if response.Code != 400 {
			t.Fatalf("status %d", response.Code)
		}
	}
	service.config.AllowControl = false
	response := request(handler, "POST", "/v1/spaces", "test-token", `{"providerId":"herdr:test-server","name":"Test","directory":"/tmp"}`)
	if response.Code != 403 || runner.mutations != 0 {
		t.Fatal(response.Code, runner.mutations)
	}
}

func TestHerdrNativeWorktreeListEnrichesFreshSpaceWithoutInferringPaneCwd(t *testing.T) {
	var value map[string]any
	if err := json.Unmarshal([]byte(herdrHierarchyFixture), &value); err != nil {
		t.Fatal(err)
	}
	snapshot := value["result"].(map[string]any)["snapshot"].(map[string]any)
	for _, rawWorkspace := range snapshot["workspaces"].([]any) {
		delete(rawWorkspace.(map[string]any), "worktree")
	}
	data, _ := json.Marshal(value)
	runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: string(data)}}
	service := hierarchyService(t, runner)
	projects, err := service.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projects[0].Path != "/repo" || projects[1].ParentID != projects[0].ID || !projects[1].IsWorktree || projects[2].Path != "" {
		t.Fatalf("native enrichment: %+v", projects)
	}
}

func TestXirpRejectsHerdrOnlyWorktreeKindWithoutStartingAnAgent(t *testing.T) {
	handler, runner, service := setup(t, false, true)
	defer handler.Close()
	err := service.Create(context.Background(), "p1", "Worktree", "", CreateOptions{Kind: "worktree", UseWorktree: true, Branch: "feature/test"})
	if !errors.Is(err, errUnsupported) {
		t.Fatal(err)
	}
	for _, command := range runner.calls {
		if strings.Contains(strings.Join(command.args, " "), "session:create") {
			t.Fatal("Herdr request started a Xirp agent")
		}
	}
}

func TestHerdrRejectsLinkedWorktreeSourcesBeforeMutation(t *testing.T) {
	for _, workspaceID := range []string{"w2", "w3"} {
		t.Run(workspaceID, func(t *testing.T) {
			runner := &herdrHierarchyRunner{herdrRunner: herdrRunner{snapshot: herdrHierarchyFixture}}
			service := hierarchyService(t, runner)
			err := service.Create(context.Background(), service.herdrID("workspace", workspaceID), "Nested work", "", CreateOptions{Kind: "worktree", UseWorktree: true, Branch: "feature/nested"})
			if !errors.Is(err, errUnsupported) || runner.mutations != 0 {
				t.Fatalf("linked source: %v, mutations=%d", err, runner.mutations)
			}
		})
	}
}
