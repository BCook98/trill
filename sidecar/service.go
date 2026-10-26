package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"trill/sidecar/providers"
)

var (
	errUnavailable    = errors.New("local backend unavailable")
	errNotFound       = errors.New("session not found")
	errUnsupported    = errors.New("operation unsupported by this backend")
	errNotAttachable  = errors.New("session is not attachable")
	errCapacity       = errors.New("mosh capacity reached")
	errOutcomeUnknown = errors.New("operation outcome unconfirmed")
	errClosed         = errors.New("sidecar shutting down")
	errTerminalBusy   = errors.New("terminal is already attached; disconnect its current direct client before retrying")
	idPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	tmuxPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

type Config struct {
	Backends        string
	HerdrConfigHome string
	HerdrSessions   string
	AllowControl    bool
	TmuxSocket      string
	LabFilesRoot    string // Explicit opt-in for browsing in terminal lab mode.
	MoshHost        string
	MoshBind        string // Local mosh-server bind address when it differs from the advertised MoshHost.
	UDPPorts        string
	TmuxPath        string
	MoshPath        string
	HerdrPath       string // Resolved from PATH only when attaching; metadata failures stay provider-local.
	HerdrSession    string
	HerdrLabRoot    string // Disposable probe only; confines all listed terminals.
	XirpPath        string
	LeaseTTL        time.Duration
}

type lease struct {
	deviceID  string // Authenticated paired device; empty for legacy/unowned callers.
	sessionID string
	proc      MoshProcess
	timer     *time.Timer
	stopping  bool
	stopped   chan struct{}
}

type Service struct {
	demo      *Demo // Explicit synthetic runtime; never selected by production configuration.
	sources   []*Service
	sourceKey string
	config    Config
	runner    Runner
	mu        sync.Mutex
	leases    map[string]*lease
	bootstrap chan struct{}
	closed    bool
	closeDone chan struct{}
	inflight  sync.WaitGroup
}

func NewService(config Config, runner Runner) (*Service, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	if config.HerdrSession != "" {
		config.HerdrSessions = config.HerdrSession
	}
	selection, err := providers.Normalize(config.Backends, config.HerdrPath, config.HerdrSessions, config.TmuxSocket != "")
	if err != nil {
		return nil, err
	}
	config.Backends, config.HerdrPath, config.HerdrSessions = selection.Backends, selection.HerdrPath, selection.HerdrSessions
	if config.HerdrPath != "" {
		config.HerdrConfigHome, err = providers.HerdrHome(config.HerdrConfigHome)
		if err != nil {
			return nil, err
		}
	} else if config.HerdrConfigHome != "" {
		return nil, fmt.Errorf("Herdr config home requires a Herdr backend")
	}

	if config.HerdrSession != "" {
		config.HerdrSessions = config.HerdrSession
	}
	if err := validateHerdrConfig(config); err != nil {
		return nil, err
	}
	if config.XirpPath == "" {
		config.XirpPath = "xirp"
	}
	if config.MoshPath == "" {
		config.MoshPath = "mosh-server"
	}
	if config.TmuxPath == "" {
		config.TmuxPath = "tmux"
	}
	if config.MoshHost == "" {
		config.MoshHost = "127.0.0.1"
	}
	if config.UDPPorts == "" {
		config.UDPPorts = "60000:60010"
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 12 * time.Hour
	}
	if config.LeaseTTL < time.Minute {
		return nil, fmt.Errorf("lease TTL must be at least one minute")
	}
	if config.TmuxSocket != "" && !filepath.IsAbs(config.TmuxSocket) {
		return nil, fmt.Errorf("tmux socket must be absolute")
	}
	if config.LabFilesRoot != "" {
		if config.TmuxSocket == "" {
			return nil, fmt.Errorf("--lab-files-root requires --tmux-socket")
		}
		if !filepath.IsAbs(config.LabFilesRoot) {
			return nil, fmt.Errorf("--lab-files-root must be an absolute existing directory")
		}
		info, err := os.Stat(config.LabFilesRoot)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("--lab-files-root must be an absolute existing directory")
		}
	}
	if !validPorts(config.UDPPorts) {
		return nil, fmt.Errorf("invalid UDP port range")
	}
	if strings.ContainsAny(config.MoshHost, " \t\r\n") {
		return nil, fmt.Errorf("invalid mosh host")
	}
	s := &Service{config: config, runner: runner, leases: map[string]*lease{}, bootstrap: make(chan struct{}, 1), closeDone: make(chan struct{})}
	if err := s.configureSources(); err != nil {
		return nil, err
	}
	return s, nil
}

func validPorts(s string) bool {
	p := strings.Split(s, ":")
	if len(p) != 2 {
		return false
	}
	a, e1 := strconv.Atoi(p[0])
	b, e2 := strconv.Atoi(p[1])
	return e1 == nil && e2 == nil && a >= 1024 && b <= 65535 && b >= a && b-a <= 1000
}

func (s *Service) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	b, err := s.runner.Run(ctx, name, args...)
	if err != nil {
		return nil, errUnavailable
	}
	return b, nil
}

func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	if s.demo != nil {
		c, err := s.demo.catalog()
		return c.Projects, err
	}
	if len(s.sources) > 0 {
		return collectSourceValues(ctx, s.sources, func(p *Service) ([]Project, error) { return p.Projects(ctx) })
	}
	if s.config.HerdrPath != "" {
		projects, _, err := s.herdrMetadata(ctx)
		return projects, err
	}
	if s.config.TmuxSocket != "" {
		return []Project{{ID: "terminal-lab", Name: "Terminal lab", Path: s.config.LabFilesRoot}}, nil
	}
	return s.xirpProjects(ctx)
}

func (s *Service) xirpProjects(ctx context.Context) ([]Project, error) {
	b, err := s.command(ctx, s.config.XirpPath, "project", "list", "--json")
	if err != nil {
		return nil, err
	}
	var p []Project
	if json.Unmarshal(b, &p) != nil || p == nil {
		return nil, errUnavailable
	}
	return p, nil
}

func (s *Service) rawSessions(ctx context.Context) ([]rawSession, error) {
	b, err := s.command(ctx, s.config.XirpPath, "session", "list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var sessions []rawSession
	if json.Unmarshal(b, &sessions) != nil || sessions == nil {
		return nil, errUnavailable
	}
	return sessions, nil
}

func (s *Service) labSessions(ctx context.Context) ([]rawSession, error) {
	b, err := s.command(ctx, s.config.TmuxPath, "-S", s.config.TmuxSocket, "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_activity}")
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return []rawSession{}, nil
	}
	var result []rawSession
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || !strings.HasPrefix(parts[0], "$") || !tmuxPattern.MatchString(parts[1]) {
			return nil, errUnavailable
		}
		id := "lab-" + strings.TrimPrefix(parts[0], "$")
		if !idPattern.MatchString(id) {
			return nil, errUnavailable
		}
		seconds, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, errUnavailable
		}
		result = append(result, rawSession{ID: id, Name: parts[1], ProjectID: "terminal-lab", Status: "running", ActivityState: "idle", CurrentAgent: "Shell", Mode: "terminal", TmuxSession: parts[1], UpdatedAt: time.Unix(seconds, 0).UTC().Format(time.RFC3339), TerminalLive: true})
	}
	return result, nil
}

func (s *Service) sessions(ctx context.Context) ([]rawSession, error) {
	if s.demo != nil {
		c, err := s.demo.catalog()
		return c.Raw, err
	}
	if len(s.sources) > 0 {
		return collectSourceValues(ctx, s.sources, func(p *Service) ([]rawSession, error) { return p.sessions(ctx) })
	}
	var rows []rawSession
	var err error
	switch {
	case s.config.HerdrPath != "":
		_, rows, err = s.herdrMetadata(ctx)
	case s.config.TmuxSocket != "":
		rows, err = s.labSessions(ctx)
	default:
		rows, err = s.rawSessions(ctx)
	}
	for i := range rows {
		rows[i].ProviderKey = s.sourceKey
	}
	return rows, err
}

func (s *Service) Sessions(ctx context.Context) ([]Session, error) {
	c, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	return c.sessions(), nil
}

func endedStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "stopped"
}
func activeStatus(status string) bool {
	return status == "running" || status == "waiting" || status == "idle" || status == "finished"
}
func resumeID(v rawSession) string {
	if v.OriginalCLISessionID != "" {
		return v.OriginalCLISessionID
	}
	return v.CLISessionID
}

func (s *Service) find(ctx context.Context, id string) (rawSession, error) {
	if !idPattern.MatchString(id) {
		return rawSession{}, errNotFound
	}
	all, err := s.sessions(ctx)
	if err != nil {
		return rawSession{}, err
	}
	var found rawSession
	matched := false
	for _, v := range all {
		if v.ID != id {
			continue
		}
		if matched {
			return rawSession{}, errUnavailable
		}
		found = v
		matched = true
	}
	if matched {
		return found, nil
	}
	return rawSession{}, errNotFound
}

type apiResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Session   struct {
		ID string `json:"id"`
	} `json:"session"`
	Params struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
		Session   struct {
			ID string `json:"id"`
		} `json:"session"`
	} `json:"params"`
}

func (r apiResponse) target() (string, bool, bool) {
	ids := []string{r.ID, r.SessionID, r.Session.ID, r.Params.ID, r.Params.SessionID, r.Params.Session.ID}
	var target string
	for _, id := range ids {
		if id != "" {
			if target != "" && target != id {
				return "", true, false
			}
			target = id
		}
	}
	return target, target != "", true
}

func (s *Service) apiSend(ctx context.Context, kind string, params any) (apiResponse, error) {
	b, _ := json.Marshal(params)
	args := []string{"api", "send", kind, "--json", string(b)}
	if kind == "session:create" || kind == "project:create-terminal" {
		// Xirp emits an ID-less session:creating progress event first.
		// Wait for the final event so a normal create is not reported uncertain.
		args = append(args, "--expect", "session:created")
	}
	out, err := s.command(ctx, s.config.XirpPath, args...)
	if err != nil {
		return apiResponse{}, err
	}
	var envelope struct {
		Response apiResponse `json:"response"`
	}
	if json.Unmarshal(out, &envelope) != nil {
		return apiResponse{}, errUnavailable
	}
	return envelope.Response, nil
}

// Readback retries only metadata reads. The mutation itself is sent once.
func (s *Service) confirm(ctx context.Context, matches func([]rawSession) bool) bool {
	for _, delay := range []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond} {
		if delay != 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return false
			}
		}
		rows, err := s.sessions(ctx)
		if err == nil && matches(rows) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

func targetRow(rows []rawSession, id string) (rawSession, bool) {
	for _, v := range rows {
		if v.ID == id {
			return v, true
		}
	}
	return rawSession{}, false
}

func updatedReplyFor(r apiResponse, id string) bool {
	if r.Type != "session:updated" {
		return false
	}
	target, hasTarget, consistent := r.target()
	return consistent && (!hasTarget || target == id)
}

func (s *Service) Create(ctx context.Context, projectID, name, goal string, options CreateOptions) error {
	if s.demo != nil {
		return s.demo.create(projectID, name, goal, options)
	}
	if len(s.sources) > 0 {
		owner, err := s.projectOwner(ctx, projectID)
		if err != nil {
			return err
		}
		return owner.Create(ctx, projectID, name, goal, options)
	}
	if s.config.HerdrPath != "" {
		if options.Kind == "worktree" {
			return s.createHerdrWorktree(ctx, projectID, name, options)
		}
		return s.createHerdrTerminal(ctx, projectID, options)
	}
	if !s.sessionManagement() || (options.Kind != "agent" && options.Kind != "terminal") {
		return errUnsupported
	}

	projects, err := s.Projects(ctx)
	if err != nil {
		return err
	}
	var project Project
	for _, p := range projects {
		if p.ID == projectID {
			project = p
			break
		}
	}
	if project.ID == "" {
		return errNotFound
	}
	if !filepath.IsAbs(project.Path) {
		return errUnavailable
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errUnavailable
	}
	requestID := "trill-" + hex.EncodeToString(nonce[:])
	kind := "session:create"
	params := map[string]any{"projectId": projectID}
	branch := options.Branch
	if options.Kind == "terminal" {
		kind = "project:create-terminal"
		params["requestId"] = requestID
	} else {
		// Xirp 0.60's generic CLI rejects requestId for session:create.
		// A supported, persisted tag correlates metadata without trusting
		// the CLI's first broadcast reply.
		params["tags"] = []string{requestID}
		params["goal"] = goal
		params["useTerminal"] = true
		if name != "" {
			params["customName"] = name
		}
		if options.UseWorktree {
			if branch == "" {
				var suffix [8]byte
				if _, err := rand.Read(suffix[:]); err != nil {
					return errUnavailable
				}
				branch = "session/trill-" + hex.EncodeToString(suffix[:])
			}
			// Request a new worktree through the CLI's newBranch parameter.
			params["newBranch"] = branch
		} else {
			// Request the selected project's authoritative root explicitly.
			// Never accept a caller-supplied filesystem path or CLI ambient parent.
			params["worktreePath"] = project.Path
		}
	}
	reply, err := s.apiSend(ctx, kind, params)
	var id string
	if options.Kind == "terminal" {
		if err != nil || reply.Type != "session:created" || reply.RequestID != requestID {
			return errOutcomeUnknown
		}
		var hasID, consistent bool
		id, hasID, consistent = reply.target()
		if !consistent || !hasID || !idPattern.MatchString(id) {
			return errOutcomeUnknown
		}
	}
	// Agent creation can be confirmed even if the CLI loses its response or
	// consumes another client's broadcast. Never resend the mutation.
	if s.confirm(ctx, func(rows []rawSession) bool {
		var v rawSession
		var ok bool
		if options.Kind == "terminal" {
			v, ok = targetRow(rows, id)
		} else {
			for _, row := range rows {
				for _, tag := range row.Tags {
					if tag == requestID {
						if ok {
							return false // Ambiguous metadata must stay unconfirmed.
						}
						v, ok = row, true
						break
					}
				}
			}
		}
		if !ok || !idPattern.MatchString(v.ID) || v.ProjectID != projectID || v.HiddenFromSidebar || v.Mode != "terminal" || !v.TerminalLive || !activeStatus(v.Status) || !tmuxPattern.MatchString(v.TmuxSession) {
			return false
		}
		if options.Kind == "terminal" {
			return v.Source == "terminal" && samePath(v.WorktreePath, project.Path)
		}
		if v.Source == "terminal" || v.Goal != goal || (name != "" && v.Name != name) {
			return false
		}
		if options.UseWorktree {
			return v.Branch == branch && filepath.IsAbs(v.WorktreePath) && !samePath(v.WorktreePath, project.Path)
		}
		return samePath(v.WorktreePath, project.Path)
	}) {
		return nil
	}
	return errOutcomeUnknown
}

func samePath(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	canonical := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	return canonical(a) == canonical(b)
}

func (s *Service) Rename(ctx context.Context, id, name string) error {
	if s.demo != nil {
		return s.demo.mutate(id, "rename", name)
	}
	if len(s.sources) > 0 {
		owner, _, err := s.sessionOwner(ctx, id)
		if err != nil {
			return err
		}
		return owner.Rename(ctx, id, name)
	}
	if s.config.HerdrPath != "" {
		return s.renameHerdr(ctx, id, name)
	}
	if !s.sessionManagement() {
		return errUnsupported
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if v.Name == name {
		return nil
	}
	reply, sendErr := s.apiSend(ctx, "session:update", map[string]string{"sessionId": id, "name": name})
	if sendErr == nil && !updatedReplyFor(reply, id) {
		return errOutcomeUnknown
	}
	if s.confirm(ctx, func(rows []rawSession) bool { v, ok := targetRow(rows, id); return ok && v.Name == name }) {
		return nil
	}
	return errOutcomeUnknown
}

func (s *Service) Stop(ctx context.Context, id string) error {
	if s.demo != nil {
		return s.demo.mutate(id, "stop", "")
	}
	if len(s.sources) > 0 {
		owner, _, err := s.sessionOwner(ctx, id)
		if err != nil {
			return err
		}
		return owner.Stop(ctx, id)
	}
	if !s.sessionManagement() {
		return errUnsupported
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if endedStatus(v.Status) {
		return errUnsupported
	}
	reply, sendErr := s.apiSend(ctx, "session:stop", map[string]string{"sessionId": id})
	if sendErr == nil && !updatedReplyFor(reply, id) {
		return errOutcomeUnknown
	}
	if s.confirm(ctx, func(rows []rawSession) bool { v, ok := targetRow(rows, id); return ok && endedStatus(v.Status) }) {
		return nil
	}
	return errOutcomeUnknown
}

func (s *Service) Resume(ctx context.Context, id string) error {
	if s.demo != nil {
		return s.demo.mutate(id, "resume", "")
	}
	if len(s.sources) > 0 {
		owner, _, err := s.sessionOwner(ctx, id)
		if err != nil {
			return err
		}
		return owner.Resume(ctx, id)
	}
	if !s.sessionManagement() {
		return errUnsupported
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if v.HiddenFromSidebar && activeStatus(v.Status) {
		return s.setHidden(ctx, v, false, true)
	}
	cliID := resumeID(v)
	if !endedStatus(v.Status) || cliID == "" {
		return errUnsupported
	}
	reply, sendErr := s.apiSend(ctx, "session:resume", map[string]string{"sessionId": id, "cliSessionId": cliID})
	if sendErr == nil && !updatedReplyFor(reply, id) {
		return errOutcomeUnknown
	}
	var resumed rawSession
	if !s.confirm(ctx, func(rows []rawSession) bool {
		row, ok := targetRow(rows, id)
		resumed = row
		return ok && row.ProjectID == v.ProjectID && activeStatus(row.Status)
	}) {
		return errOutcomeUnknown
	}
	if !resumed.HiddenFromSidebar {
		return nil
	}
	return s.setHidden(ctx, resumed, false, true)
}

func (s *Service) Archive(ctx context.Context, id string) error {
	if s.demo != nil {
		return s.demo.mutate(id, "archive", "")
	}
	if len(s.sources) > 0 {
		owner, _, err := s.sessionOwner(ctx, id)
		if err != nil {
			return err
		}
		return owner.Archive(ctx, id)
	}
	if !s.sessionManagement() {
		return errUnsupported
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	if v.HiddenFromSidebar {
		return nil // Authoritative metadata already confirms the requested state.
	}
	return s.setHidden(ctx, v, true, false)
}

func (s *Service) setHidden(ctx context.Context, before rawSession, hidden, requireActive bool) error {
	reply, sendErr := s.apiSend(ctx, "session-sidebar:set", map[string]any{"sessionId": before.ID, "hidden": hidden})
	if sendErr == nil && !updatedReplyFor(reply, before.ID) {
		return errOutcomeUnknown
	}
	if s.confirm(ctx, func(rows []rawSession) bool {
		v, ok := targetRow(rows, before.ID)
		return ok && v.ProjectID == before.ProjectID && v.HiddenFromSidebar == hidden && (!requireActive || activeStatus(v.Status))
	}) {
		return nil
	}
	return errOutcomeUnknown
}

type MoshInfo struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Key     string `json:"key"`
	LeaseID string `json:"leaseId"`
}

func (s *Service) Bootstrap(ctx context.Context, id string, cols, rows int) (MoshInfo, error) {
	return s.bootstrapDevice(ctx, id, cols, rows, "")
}

// bootstrapDevice permits recovery of this authenticated device's own Herdr
// connection after a lost response, failed release, or app restart. Identity
// comes from HTTP authentication, never a request body or a client-supplied ID.
func (s *Service) bootstrapDevice(ctx context.Context, id string, cols, rows int, deviceID string) (MoshInfo, error) {
	if cols < 20 || cols > 500 || rows < 5 || rows > 200 {
		return MoshInfo{}, errUnsupported
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return MoshInfo{}, errClosed
	}
	s.inflight.Add(1)
	s.mu.Unlock()
	inflight := true
	defer func() {
		if inflight {
			s.inflight.Done()
		}
	}()
	select {
	case s.bootstrap <- struct{}{}:
		defer func() { <-s.bootstrap }()
	default:
		return MoshInfo{}, errCapacity
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return MoshInfo{}, errClosed
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return MoshInfo{}, err
	}
	if v.HerdrTerminal != "" || s.demo != nil {
		s.mu.Lock()
		var previous []string
		for leaseID, l := range s.leases {
			if l.sessionID != id {
				continue
			}
			if deviceID == "" || l.deviceID != deviceID {
				s.mu.Unlock()
				return MoshInfo{}, errTerminalBusy
			}
			previous = append(previous, leaseID)
		}
		s.mu.Unlock()
		// Bootstrap is serialized. Stop only tracked connections belonging to
		// this phone and this terminal before asking Herdr for a fresh controller.
		// Never use takeover: an external client may have acquired the terminal.
		for _, leaseID := range previous {
			s.DeleteLease(leaseID)
		}
	}
	s.mu.Lock()
	count := len(s.leases)
	s.mu.Unlock()
	limit := 32
	if s.demo != nil {
		limit = s.demo.maxLeases
	}
	if count >= limit {
		return MoshInfo{}, errCapacity
	}
	attach, err := s.attachCommand(ctx, v, cols, rows)
	if err != nil {
		return MoshInfo{}, err
	}
	s.mu.Lock()
	closed = s.closed
	s.mu.Unlock()
	if closed {
		return MoshInfo{}, errClosed
	}
	bindHost := s.config.MoshHost
	if s.config.MoshBind != "" {
		bindHost = s.config.MoshBind
	}
	if s.demo != nil {
		bindHost = s.demo.bindHost
	}
	args := []string{"new", "-i", bindHost, "-p", s.config.UDPPorts, "-c", "256", "-l", "LANG=en_US.UTF-8", "--"}
	args = append(args, attach...)
	env := []string{"MOSH_SERVER_NETWORK_TMOUT=600", "MOSH_SERVER_SIGNAL_TMOUT=600"}
	if v.HerdrTerminal != "" {
		owner := s.source(v.ProviderKey)
		if owner == nil {
			return MoshInfo{}, errNotFound
		}
		env = append(env, owner.herdrEnv()...)
	}
	proc, line, err := s.runner.StartMosh(ctx, s.config.MoshPath, args, env)
	if err != nil {
		return MoshInfo{}, errUnavailable
	}
	match := connectLine.FindStringSubmatch(line)
	if len(match) != 3 {
		proc.Stop()
		return MoshInfo{}, errUnavailable
	}
	port, err := strconv.Atoi(match[1])
	if err != nil || port < 1 || port > 65535 {
		proc.Stop()
		return MoshInfo{}, errUnavailable
	}
	idBytes := make([]byte, 24)
	if _, err = rand.Read(idBytes); err != nil {
		proc.Stop()
		return MoshInfo{}, errUnavailable
	}
	leaseID := hex.EncodeToString(idBytes)
	l := &lease{proc: proc, sessionID: id, deviceID: deviceID}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		proc.Stop()
		return MoshInfo{}, errClosed
	}
	l.timer = time.AfterFunc(s.config.LeaseTTL, func() { s.DeleteLease(leaseID) })
	s.leases[leaseID] = l
	s.inflight.Done()
	inflight = false
	s.mu.Unlock()
	go func() {
		<-proc.Done()
		s.mu.Lock()
		if s.leases[leaseID] == l && !l.stopping {
			delete(s.leases, leaseID)
			l.timer.Stop()
		}
		s.mu.Unlock()
	}()
	return MoshInfo{Host: s.config.MoshHost, Port: port, Key: match[2], LeaseID: leaseID}, nil
}

func (s *Service) DeleteLease(id string) bool {
	s.mu.Lock()
	l, ok := s.leases[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if l.stopping {
		done := l.stopped
		s.mu.Unlock()
		<-done
		return true
	}
	l.stopping = true
	l.stopped = make(chan struct{})
	if l.timer != nil {
		l.timer.Stop()
	}
	s.mu.Unlock()
	// Keep the retiring lease discoverable until Stop finishes. Reconnect must
	// join a concurrent DELETE/expiry instead of racing Herdr's old controller.
	l.proc.Stop()
	s.mu.Lock()
	if s.leases[id] == l {
		delete(s.leases, id)
	}
	close(l.stopped)
	s.mu.Unlock()
	return true
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.inflight.Wait()
	s.mu.Lock()
	ids := make([]string, 0, len(s.leases))
	for id := range s.leases {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.DeleteLease(id)
	}
	close(s.closeDone)
}
