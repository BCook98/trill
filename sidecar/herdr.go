package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var herdrTerminalPattern = regexp.MustCompile(`^term_[A-Za-z0-9_-]{1,100}$`)

// Herdr 0.9.5 workspace.rs uses this alphabet for public workspace and pane
// numbers (10 -> A, 32 -> 0, 33 -> 11), not decimal integers.
const herdrPublicIDAlphabet = "123456789ABCDEFGHJKMNPQRSTVWXYZ0"

var herdrWorkspacePattern = regexp.MustCompile(`^w[` + herdrPublicIDAlphabet + `]+$`)
var herdrPanePattern = regexp.MustCompile(`^w[` + herdrPublicIDAlphabet + `]+:p[` + herdrPublicIDAlphabet + `]+$`)

func validateHerdrConfig(c Config) error {
	if c.HerdrLabRoot != "" {
		if c.Backends != "herdr" || len(strings.Split(c.HerdrSessions, ",")) != 1 {
			return fmt.Errorf("Herdr lab requires exactly one Herdr server and no other provider")
		}
		info, err := os.Stat(c.HerdrLabRoot)
		if !filepath.IsAbs(c.HerdrLabRoot) || err != nil || !info.IsDir() {
			return fmt.Errorf("Herdr lab root must be an absolute existing directory")
		}
	}
	return nil
}

type herdrWorktreeProvenance struct {
	CheckoutPath string `json:"checkout_path"`
	RepoKey      string `json:"repo_key"`
	IsLinked     bool   `json:"is_linked_worktree"`
}

type herdrSnapshot struct {
	Version    string `json:"version"`
	Protocol   int    `json:"protocol"`
	Workspaces []struct {
		ID       string                   `json:"workspace_id"`
		Label    string                   `json:"label"`
		Focused  bool                     `json:"focused"`
		Worktree *herdrWorktreeProvenance `json:"worktree"`
	} `json:"workspaces"`
	Tabs []struct {
		ID        string `json:"tab_id"`
		Workspace string `json:"workspace_id"`
		Label     string `json:"label"`
		Number    int    `json:"number"`
	} `json:"tabs"`
	Panes []struct {
		ID            string `json:"pane_id"`
		Tab           string `json:"tab_id"`
		TerminalTitle string `json:"terminal_title_stripped"`
		Terminal      string `json:"terminal_id"`
		Workspace     string `json:"workspace_id"`
		Cwd           string `json:"cwd"`
		Label         string `json:"label"`
		Agent         string `json:"agent"`
		Title         string `json:"title"`
		State         string `json:"agent_status"`
		RestoreError  string `json:"restore_error"`
	} `json:"panes"`
}

// Namespace by local server location and name. Terminal identity survives pane moves;
// a restarted server produces new terminal IDs rather than inheriting old drafts.
func (s *Service) herdrID(kind, id string) string {
	configDir := s.config.HerdrConfigHome
	sum := sha256.Sum256([]byte(configDir + "\x00" + s.config.HerdrSession + "\x00" + kind + "\x00" + id))
	return "herdr-" + hex.EncodeToString(sum[:16])
}

func (s *Service) herdrMetadata(ctx context.Context) ([]Project, []rawSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	schemaData, err := s.herdrCommand(ctx, "api", "schema", "--json")
	if err != nil {
		return nil, nil, err
	}
	var schema struct {
		Protocol int `json:"protocol"`
	}
	if json.Unmarshal(schemaData, &schema) != nil || schema.Protocol < 22 {
		return nil, nil, errUnavailable
	}
	b, err := s.herdrCommand(ctx, "--session", s.config.HerdrSession, "api", "snapshot")
	if err != nil {
		return nil, nil, err
	}
	var response struct {
		Result struct {
			Type     string        `json:"type"`
			Snapshot herdrSnapshot `json:"snapshot"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &response) != nil || len(response.Error) != 0 || response.Result.Type != "session_snapshot" {
		return nil, nil, errUnavailable
	}
	snapshot := response.Result.Snapshot
	// Direct attach uses the installed binary’s internal protocol. Patch releases
	// can differ, but the running server and local CLI must agree on that protocol.
	if snapshot.Protocol != schema.Protocol || snapshot.Workspaces == nil || snapshot.Panes == nil {
		return nil, nil, errUnavailable
	}
	branches := s.enrichHerdrWorktrees(ctx, &snapshot)
	projects := make([]Project, 0, len(snapshot.Workspaces))
	projectByWorkspace := map[string]int{}
	for _, w := range snapshot.Workspaces {
		if !herdrWorkspacePattern.MatchString(w.ID) {
			return nil, nil, errUnavailable
		}
		if _, duplicate := projectByWorkspace[w.ID]; duplicate {
			return nil, nil, errUnavailable
		}
		projectByWorkspace[w.ID] = len(projects)
		path := ""
		if w.Worktree != nil {
			path = w.Worktree.CheckoutPath
			if path != "" && !filepath.IsAbs(path) {
				return nil, nil, errUnavailable
			}
		}
		projects = append(projects, Project{ID: s.herdrID("workspace", w.ID), Name: w.Label, Path: path, Branch: branches[w.ID], HerdrWorkspace: w.ID, Focused: w.Focused, WorkspaceOrder: len(projects) + 1, IsWorktree: w.Worktree != nil && w.Worktree.IsLinked})
	}
	// Group only by Herdr's repository provenance, never by a folder name or cwd.
	primaryByRepository := map[string]string{}
	for _, workspace := range snapshot.Workspaces {
		if workspace.Worktree != nil && !workspace.Worktree.IsLinked && workspace.Worktree.RepoKey != "" {
			if primaryByRepository[workspace.Worktree.RepoKey] == "" {
				primaryByRepository[workspace.Worktree.RepoKey] = s.herdrID("workspace", workspace.ID)
			}
		}
	}
	for index, workspace := range snapshot.Workspaces {
		if workspace.Worktree != nil && workspace.Worktree.IsLinked {
			projects[index].ParentID = primaryByRepository[workspace.Worktree.RepoKey]
		}
	}
	tabNames := map[string]string{}
	for _, tab := range snapshot.Tabs {
		// Numeric labels are Herdr's defaults, not useful session names.
		if tab.Label != "" && tab.Label != fmt.Sprint(tab.Number) {
			tabNames[tab.Workspace+"\x00"+tab.ID] = tab.Label
		}
	}
	sessions := make([]rawSession, 0, len(snapshot.Panes))
	seen := map[string]bool{}
	for _, p := range snapshot.Panes {
		index, ok := projectByWorkspace[p.Workspace]
		if !ok || !herdrPanePattern.MatchString(p.ID) || !herdrTerminalPattern.MatchString(p.Terminal) || seen[p.Terminal] {
			return nil, nil, errUnavailable
		}
		seen[p.Terminal] = true
		if p.Cwd != "" && !filepath.IsAbs(p.Cwd) {
			return nil, nil, errUnavailable
		}
		if s.config.HerdrLabRoot != "" && !samePath(p.Cwd, s.config.HerdrLabRoot) {
			return nil, nil, errUnavailable
		}
		// Pane cwd remains scoped to this terminal. Only native Git provenance
		// supplies a project root; never infer it from whichever pane is first.
		name := p.Label
		if name == "" {
			name = tabNames[p.Workspace+"\x00"+p.Tab]
		}
		if name == "" {
			name = p.Title
		}
		if name == "" {
			name = p.TerminalTitle
		}
		if name == "" {
			name = p.ID
		}
		activity := p.State
		if activity == "blocked" {
			activity = "waiting_for_input"
		}
		if activity == "done" {
			activity = "idle"
		}
		agent := p.Agent
		if agent == "" {
			agent = "Shell"
		}
		projectID := projects[index].ID
		if s.config.HerdrLabRoot != "" {
			projectID = "herdr-lab"
		}
		sessions = append(sessions, rawSession{ID: s.herdrID("terminal", p.Terminal), Name: name, ProjectID: projectID,
			HerdrAgentStatus: p.State, Status: "running", ActivityState: activity, CurrentAgent: agent, Mode: "terminal", HerdrTerminal: p.Terminal, HerdrPane: p.ID,
			Branch: projects[index].Branch, WorktreePath: p.Cwd, TerminalLive: strings.TrimSpace(p.RestoreError) == ""})
	}
	if s.config.HerdrLabRoot != "" {
		projects = []Project{{ID: "herdr-lab", Name: "Herdr lab", Path: s.config.HerdrLabRoot}}
	}
	return projects, sessions, nil
}

// Fresh spaces may not yet carry Git provenance in session.snapshot. The native
// worktree listing resolves it without guessing from a pane's current directory.
// Git/trust failures leave the original snapshot usable and do not grant trust.
func (s *Service) enrichHerdrWorktrees(ctx context.Context, snapshot *herdrSnapshot) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	branches := map[string]string{}
	workspaceIndices := map[string]int{}
	for index, workspace := range snapshot.Workspaces {
		workspaceIndices[workspace.ID] = index
	}
	queriedRepositories := map[string]bool{}
	for _, workspace := range snapshot.Workspaces {
		if ctx.Err() != nil {
			break
		}
		if !herdrWorkspacePattern.MatchString(workspace.ID) {
			continue
		}
		if workspace.Worktree != nil && workspace.Worktree.RepoKey != "" && queriedRepositories[workspace.Worktree.RepoKey] {
			continue
		}
		data, err := s.herdrCommand(ctx, "--session", s.config.HerdrSession, "worktree", "list", "--workspace", workspace.ID)
		var response struct {
			Result struct {
				Type   string `json:"type"`
				Source struct {
					RepoKey   string `json:"repo_key"`
					Workspace string `json:"source_workspace_id"`
				} `json:"source"`
				Worktrees []struct {
					Workspace string `json:"open_workspace_id"`
					Path      string `json:"path"`
					Branch    string `json:"branch"`
					IsLinked  bool   `json:"is_linked_worktree"`
				} `json:"worktrees"`
			} `json:"result"`
		}
		if err != nil || json.Unmarshal(data, &response) != nil || response.Result.Type != "worktree_list" {
			continue
		}
		source := response.Result.Source
		if source.RepoKey != "" && source.Workspace == workspace.ID {
			queriedRepositories[source.RepoKey] = true
		}
		for _, checkout := range response.Result.Worktrees {
			index, ok := workspaceIndices[checkout.Workspace]
			if !ok || !filepath.IsAbs(checkout.Path) {
				continue
			}
			target := &snapshot.Workspaces[index]
			if target.Worktree == nil && source.RepoKey != "" && source.Workspace == workspace.ID {
				target.Worktree = &herdrWorktreeProvenance{CheckoutPath: checkout.Path, RepoKey: source.RepoKey, IsLinked: checkout.IsLinked}
			}
			if target.Worktree != nil && samePath(target.Worktree.CheckoutPath, checkout.Path) {
				branches[checkout.Workspace] = checkout.Branch
			}
		}
	}
	return branches
}

// Resolve the current pane for the stable terminal ID immediately before sending
// one mutation. Verify readback; never replay a rename after an ambiguous reply.
func (s *Service) renameHerdr(ctx context.Context, id, name string) error {
	if name == "--clear" {
		return errUnsupported
	} // This CLI spelling clears a label.
	before, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if before.Name == name {
		return nil
	}
	_, _ = s.herdrCommand(ctx, "--session", s.config.HerdrSession, "pane", "rename", before.HerdrPane, name)
	if s.confirm(ctx, func(rows []rawSession) bool { v, ok := targetRow(rows, id); return ok && v.Name == name }) {
		return nil
	}
	return errOutcomeUnknown
}

// An optional existing directory becomes the new tab’s cwd. No launch command
// is accepted. Creation is sent once and confirmed by terminal ID and cwd.
func (s *Service) createHerdrTerminal(ctx context.Context, projectID string, options CreateOptions) error {
	if options.Kind != "terminal" || options.UseWorktree || options.Branch != "" {
		return errUnsupported
	}
	if err := s.validateTerminalDirectory(options.Directory); err != nil {
		return err
	}
	projects, err := s.Projects(ctx)
	if err != nil {
		return err
	}
	workspace := ""
	for _, p := range projects {
		if p.ID == projectID {
			workspace = p.HerdrWorkspace
			break
		}
	}
	if workspace == "" {
		return errNotFound
	}
	arguments := []string{"--session", s.config.HerdrSession, "tab", "create", "--workspace", workspace, "--no-focus"}
	if options.Directory != "" {
		arguments = append(arguments, "--cwd", options.Directory)
	}
	b, err := s.herdrCommand(ctx, arguments...)
	if err != nil {
		return errOutcomeUnknown
	}
	var response struct {
		Result struct {
			RootPane struct {
				Terminal  string `json:"terminal_id"`
				Workspace string `json:"workspace_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &response) != nil || response.Result.RootPane.Workspace != workspace || !herdrTerminalPattern.MatchString(response.Result.RootPane.Terminal) {
		return errOutcomeUnknown
	}
	id := s.herdrID("terminal", response.Result.RootPane.Terminal)
	if s.confirm(ctx, func(rows []rawSession) bool {
		v, ok := targetRow(rows, id)
		return ok && v.ProjectID == projectID && v.TerminalLive && (options.Directory == "" || samePath(v.WorktreePath, options.Directory))
	}) {
		return nil
	}
	return errOutcomeUnknown
}

func (s *Service) herdrEnv() []string {
	return []string{"XDG_CONFIG_HOME=" + s.config.HerdrConfigHome, "HERDR_CONFIG_PATH=" + filepath.Join(s.config.HerdrConfigHome, "herdr", "config.toml")}
}
func (s *Service) herdrCommand(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 4 && args[0] == "--session" && args[2] == "api" && args[3] == "snapshot" {
		if reader, ok := s.runner.(interface {
			HerdrSnapshot(context.Context, string, string) ([]byte, error)
		}); ok {
			return reader.HerdrSnapshot(ctx, s.config.HerdrConfigHome, args[1])
		}
	}
	runner, ok := s.runner.(interface {
		RunWithEnv(context.Context, string, []string, []string) ([]byte, error)
	})
	if !ok {
		return nil, errUnavailable
	}
	b, err := runner.RunWithEnv(ctx, s.config.HerdrPath, args, s.herdrEnv())
	if err != nil {
		return nil, errUnavailable
	}
	return b, nil
}

// CreateSpace targets a configured, healthy server, including servers with no spaces.
func (s *Service) CreateSpace(ctx context.Context, providerID, name, directory string) error {
	if !validText(name, 200) || name == "--clear" || !filepath.IsAbs(directory) || !validText(directory, 4096) {
		return errUnsupported
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return errNotFound
	}
	catalog, err := s.catalog(ctx)
	if err != nil {
		return err
	}
	for _, provider := range catalog.Providers {
		if provider.ID != providerID || !provider.CreateSpace {
			continue
		}
		owner := s.source(providerID)
		if owner == nil {
			return errNotFound
		}
		return owner.createHerdrWorkspace(ctx, "workspace_created", []string{"workspace", "create", "--cwd", directory, "--label", name, "--no-focus"})
	}
	return errUnsupported
}

func (s *Service) createHerdrWorktree(ctx context.Context, projectID, name string, options CreateOptions) error {
	if options.Kind != "worktree" || !options.UseWorktree || !validBranch(options.Branch) || (name != "" && !validText(name, 200)) {
		return errUnsupported
	}
	projects, err := s.Projects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project.ID != projectID {
			continue
		}
		if project.Path == "" || project.IsWorktree || project.ParentID != "" || s.config.HerdrLabRoot != "" {
			return errUnsupported
		}
		arguments := []string{"worktree", "create", "--workspace", project.HerdrWorkspace, "--branch", options.Branch, "--no-focus"}
		if name != "" {
			arguments = append(arguments, "--label", name)
		}
		return s.createHerdrWorkspace(ctx, "worktree_created", arguments)
	}
	return errNotFound
}

// Send once, then correlate the returned workspace and stable terminal with a
// fresh snapshot. Never retry a create after a transport or decoding failure.
func (s *Service) createHerdrWorkspace(ctx context.Context, resultType string, arguments []string) error {
	projects, terminals, err := s.herdrMetadata(ctx)
	if err != nil {
		return err
	}
	existingTerminals := map[string]bool{}
	for _, terminal := range terminals {
		existingTerminals[terminal.HerdrTerminal] = true
	}
	existing := map[string]bool{}
	for _, project := range projects {
		existing[project.HerdrWorkspace] = true
	}
	arguments = append([]string{"--session", s.config.HerdrSession}, arguments...)
	data, err := s.herdrCommand(ctx, arguments...)
	if err != nil {
		return errOutcomeUnknown
	}
	var response struct {
		Result struct {
			Type      string `json:"type"`
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				Terminal  string `json:"terminal_id"`
				Workspace string `json:"workspace_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &response) != nil {
		return errOutcomeUnknown
	}
	result := response.Result
	if result.Type != resultType || !herdrWorkspacePattern.MatchString(result.Workspace.ID) || existing[result.Workspace.ID] || existingTerminals[result.RootPane.Terminal] || result.RootPane.Workspace != result.Workspace.ID || !herdrTerminalPattern.MatchString(result.RootPane.Terminal) {
		return errOutcomeUnknown
	}
	terminalID, projectID := s.herdrID("terminal", result.RootPane.Terminal), s.herdrID("workspace", result.Workspace.ID)
	if s.confirm(ctx, func(rows []rawSession) bool {
		terminal, ok := targetRow(rows, terminalID)
		return ok && terminal.ProjectID == projectID && terminal.TerminalLive
	}) {
		return nil
	}
	return errOutcomeUnknown
}
