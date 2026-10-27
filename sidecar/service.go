package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"trill/sidecar/providers"
)

var (
	errUnavailable    = errors.New("local backend unavailable")
	errNotFound       = errors.New("session not found")
	errUnsupported    = errors.New("operation unsupported by this backend")
	errNotAttachable  = errors.New("session is not attachable")
	errCapacity       = errors.New("terminal capacity reached")
	errOutcomeUnknown = errors.New("operation outcome unconfirmed")
	errClosed         = errors.New("sidecar shutting down")
	errTerminalBusy   = errors.New("terminal is already attached; disconnect its current direct client before retrying")
	idPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type Config struct {
	Backends        string
	HerdrConfigHome string
	HerdrSessions   string
	AllowControl    bool
	HerdrPath       string // Resolved from PATH only when attaching; metadata failures stay provider-local.
	HerdrSession    string
	HerdrLabRoot    string // Disposable probe only; confines all listed terminals.
	LeaseTTL        time.Duration
}

type lease struct {
	deviceID  string // Authenticated paired device; empty for legacy/unowned callers.
	sessionID string
	proc      TerminalProcess
	timer     *time.Timer
	stopping  bool
	stopped   chan struct{}
}

type Service struct {
	demo         *Demo // Explicit synthetic runtime; never selected by production configuration.
	sources      []*Service
	sourceKey    string
	config       Config
	runner       Runner
	mu           sync.Mutex
	leases       map[string]*lease
	bootstrap    chan struct{}
	closed       bool
	closeDone    chan struct{}
	readMetadata func(context.Context) ([]Project, []rawSession, error)
	inflight     sync.WaitGroup
}

func NewService(config Config, runner Runner) (*Service, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	selection, err := providers.Normalize(config.Backends, config.HerdrPath, config.HerdrSessions, false)
	if err != nil {
		return nil, err
	}
	config.Backends, config.HerdrPath, config.HerdrSessions = selection.Backends, selection.HerdrPath, selection.HerdrSessions
	if config.HerdrSession != "" {
		config.HerdrSessions = config.HerdrSession
	}
	config.HerdrConfigHome, err = providers.HerdrHome(config.HerdrConfigHome)
	if err != nil {
		return nil, err
	}
	if err = validateHerdrConfig(config); err != nil {
		return nil, err
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 12 * time.Hour
	}
	if config.LeaseTTL < time.Minute {
		return nil, fmt.Errorf("lease TTL must be at least one minute")
	}
	service := &Service{config: config, runner: runner, leases: map[string]*lease{}, bootstrap: make(chan struct{}, 1), closeDone: make(chan struct{})}
	service.readMetadata = service.herdrMetadata
	if err = service.configureSources(); err != nil {
		return nil, err
	}
	return service, nil
}
func (s *Service) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	data, err := s.runner.Run(ctx, name, args...)
	if err != nil {
		return nil, errUnavailable
	}
	return data, nil
}
func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	catalog, err := s.catalog(ctx)
	return catalog.Projects, err
}
func (s *Service) sessions(ctx context.Context) ([]rawSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.demo != nil {
		catalog, err := s.demo.catalog()
		return catalog.Raw, err
	}
	if len(s.sources) > 0 {
		return collectSourceValues(ctx, s.sources, func(source *Service) ([]rawSession, error) { return source.sessions(ctx) })
	}
	_, sessions, err := s.readMetadata(ctx)
	for index := range sessions {
		sessions[index].ProviderKey = s.sourceKey
	}
	return sessions, err
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
	if options.Kind == "worktree" {
		return s.createHerdrWorktree(ctx, projectID, name, options)
	}
	return s.createHerdrTerminal(ctx, projectID, options)
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
	return s.renameHerdr(ctx, id, name)
}

type TerminalLease struct {
	LeaseID string `json:"leaseId"`
}

func (s *Service) Bootstrap(ctx context.Context, id string, cols, rows int) (TerminalLease, error) {
	return s.bootstrapDevice(ctx, id, cols, rows, "")
}
func (s *Service) bootstrapDevice(ctx context.Context, id string, cols, rows int, deviceID string) (TerminalLease, error) {
	return s.bootstrapTerminal(ctx, id, cols, rows, deviceID)
}
func (s *Service) bootstrapTerminal(ctx context.Context, id string, cols, rows int, deviceID string) (TerminalLease, error) {
	if cols < 20 || cols > 500 || rows < 5 || rows > 200 {
		return TerminalLease{}, errUnsupported
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return TerminalLease{}, errClosed
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
		return TerminalLease{}, errCapacity
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return TerminalLease{}, errClosed
	}
	v, err := s.find(ctx, id)
	if err != nil {
		return TerminalLease{}, err
	}
	if v.Mode != "terminal" || !v.TerminalLive || !activeStatus(v.Status) {
		return TerminalLease{}, errNotAttachable
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
				return TerminalLease{}, errTerminalBusy
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
		return TerminalLease{}, errCapacity
	}
	var proc TerminalProcess
	if s.demo != nil {
		proc, err = s.startDemoTerminal(ctx, id, cols, rows)
	} else {
		owner := s.source(v.ProviderKey)
		if owner == nil || v.HerdrTerminal == "" {
			return TerminalLease{}, errNotAttachable
		}
		starter, ok := s.runner.(interface {
			StartHerdrTerminal(context.Context, string, []string, []string) (*herdrTerminalProcess, error)
		})
		if !ok {
			return TerminalLease{}, errUnavailable
		}
		proc, err = starter.StartHerdrTerminal(ctx, owner.config.HerdrPath, []string{"--session", owner.config.HerdrSession, "terminal", "session", "control", v.HerdrTerminal, "--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows)}, owner.herdrEnv())
	}
	if err != nil {
		return TerminalLease{}, err
	}
	idBytes := make([]byte, 24)
	if _, err = rand.Read(idBytes); err != nil {
		proc.Stop()
		return TerminalLease{}, errUnavailable
	}
	leaseID := hex.EncodeToString(idBytes)
	l := &lease{proc: proc, sessionID: id, deviceID: deviceID}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		proc.Stop()
		return TerminalLease{}, errClosed
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
	return TerminalLease{LeaseID: leaseID}, nil
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
