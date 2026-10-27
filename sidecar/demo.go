package sidecar

// This provider is selected only by NewDemoService. It has no CLI or shell,
// or Herdr dependency. Production handlers and transport continue to own auth,
// validation, leases, files, uploads, and browser grants.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"trill/sidecar/pairing"
)

const (
	demoMaxSessions   = 32
	demoMaxTranscript = 16 << 10
	demoMaxInput      = 8 << 10
	demoMaxState      = 1 << 20
)

type DemoOptions struct {
	Root, Profile, Executable, Socket string
	BindHost                          string // Legacy fixture bind option; terminal traffic uses HTTPS.
	MaxLeases                         int
}
type DemoSnapshot struct {
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
	Live     bool   `json:"live"`
	Columns  int    `json:"columns"`
	Rows     int    `json:"rows"`
}
type demoProject struct{ ID, Name, Provider string }
type demoSession struct {
	Session   rawSession   `json:"session"`
	Directory string       `json:"directory"`
	Screen    DemoSnapshot `json:"screen"`
	TaskDue   time.Time    `json:"taskDue,omitempty"`
	TaskPhase int          `json:"taskPhase,omitempty"`
}
type demoState struct {
	Version  int               `json:"version"`
	Profile  string            `json:"profile"`
	Next     uint64            `json:"next"`
	Projects []demoProject     `json:"projects"`
	Sessions []demoSession     `json:"sessions"`
	Headings map[string]string `json:"headings"`
	Revision uint64            `json:"revision"`
}

// Demo's state is private to one parent process. The caller must hold its state
// directory lifecycle lock for the lifetime of this value. Terminal children use
// the parent's private IPC and never open this state themselves.
type Demo struct {
	mu                                              sync.Mutex
	root                                            *os.Root
	rootPath, profile, executable, socket, bindHost string
	maxLeases                                       int
	previewPort                                     int
	allowControl                                    bool
	state                                           demoState
	faults                                          map[string]bool
	closed                                          bool
	wsSlots                                         chan struct{}
	done, stopped                                   chan struct{}
	closeDone                                       chan struct{}
	closeErr                                        error
	service                                         *Service
	httpHandlers                                    []*HTTP
}

func NewDemoService(config Config, options DemoOptions) (*Service, *Demo, error) {
	if options.Profile == "" {
		options.Profile = "herdr"
	}
	if options.Profile != "herdr" {
		return nil, nil, errors.New("demo supports Herdr sessions only")
	}
	if !filepath.IsAbs(options.Root) || !filepath.IsAbs(options.Executable) || !filepath.IsAbs(options.Socket) {
		return nil, nil, errors.New("demo paths must be absolute")
	}
	info, err := os.Lstat(options.Root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, nil, errors.New("demo root must be an existing private directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, nil, errors.New("demo root must belong to this user")
	}
	if options.MaxLeases == 0 {
		options.MaxLeases = 2
	}
	if options.MaxLeases < 1 || options.MaxLeases > 8 {
		return nil, nil, errors.New("demo max leases must be between one and eight")
	}
	// Deliberately discard provider config: there is no path to a real provider.
	config.Backends, config.HerdrPath, config.HerdrConfigHome, config.HerdrSessions, config.HerdrSession, config.HerdrLabRoot = "herdr", "", "", "", "", ""
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 15 * time.Minute
	}
	s, err := NewService(config, ExecRunner{})
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(options.Root)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	d := &Demo{root: root, rootPath: options.Root, profile: options.Profile, executable: options.Executable, socket: options.Socket, maxLeases: options.MaxLeases, allowControl: config.AllowControl, faults: map[string]bool{}, wsSlots: make(chan struct{}, 8), done: make(chan struct{}), stopped: make(chan struct{}), closeDone: make(chan struct{}), service: s}
	if err = d.load(); err == nil {
		err = d.ensureFixtures()
	}
	if err != nil {
		root.Close()
		s.Close()
		return nil, nil, err
	}
	s.demo = d
	go d.tasks()
	return s, d, nil
}

// NewDemoHTTP retains the complete production auth/router/proxy surface. Only
// the demo's owned attachment directory and exact loopback preview target differ.
func NewDemoHTTP(s *Service, d *Demo, store *pairing.Store, previewPort int) (*HTTP, error) {
	if s == nil || d == nil || s.demo != d || previewPort < 1024 || previewPort > 65535 {
		return nil, errors.New("invalid demo HTTP configuration")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errClosed
	}
	if err := d.privateDir("attachments"); err != nil {
		return nil, err
	}
	d.previewPort = previewPort
	h := NewHTTPWithPairing(s, "", store)
	h.attachmentRoot = filepath.Join(d.rootPath, "attachments")
	h.attachmentLimit = 64 << 20
	h.photoPixelLimit = 8_000_000
	h.previewPort = previewPort
	d.httpHandlers = append(d.httpHandlers, h)
	return h, nil
}

func demoSafeText(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, v)
}
func (d *Demo) privateDir(name string) error {
	if !validFilePath(filepath.ToSlash(name)) {
		return errors.New("invalid demo directory")
	}
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(name), "/") {
		current = filepath.Join(current, component)
		if err := d.root.Mkdir(current, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := d.root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return errors.New("unsafe demo directory")
		}
	}
	return nil
}
func (d *Demo) atomicWrite(name string, data []byte) error {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(name), ".demo-"+hex.EncodeToString(b[:]))
	file, err := d.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer d.root.Remove(temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		// Go 1.24's Root has no Rename. Both names are generated by this
		// provider, with temp created through Root in the destination directory.
		// The parent holds the exclusive lifecycle flock over this private root.
		err = os.Rename(filepath.Join(d.rootPath, temp), filepath.Join(d.rootPath, name))
	}
	if err == nil {
		dir, e := d.root.Open(filepath.Dir(name))
		if e != nil {
			return e
		}
		err = dir.Sync()
		dir.Close()
	}
	return err
}
func (d *Demo) load() error {
	info, err := d.root.Lstat("demo-state.json")
	if errors.Is(err, fs.ErrNotExist) {
		d.seed()
		return d.persist()
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > demoMaxState || info.Mode().Perm() != 0600 {
		return errors.New("unsafe demo state")
	}
	file, err := d.root.Open("demo-state.json")
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, demoMaxState+1))
	if err != nil || len(data) > demoMaxState {
		return errors.New("demo state exceeds limit")
	}
	if json.Unmarshal(data, &d.state) != nil {
		return errors.New("invalid demo state")
	}
	return d.validateState()
}
func (d *Demo) validateState() error {
	st := &d.state
	if st.Version != 1 || st.Headings == nil || st.Profile != d.profile || len(st.Projects) < 2 || len(st.Projects) > 4 || len(st.Sessions) > demoMaxSessions || len(st.Headings) > demoMaxSessions+4 || st.Next > 1_000_000 {
		return errors.New("incompatible or oversized demo state")
	}
	projects := map[string]string{}
	ids := map[string]bool{}
	for _, p := range st.Projects {
		if !idPattern.MatchString(p.ID) || projects[p.ID] != "" || !validText(p.Name, 200) || p.Provider != "herdr" {
			return errors.New("invalid demo project")
		}
		projects[p.ID] = p.Provider
	}
	for _, v := range st.Sessions {
		provider := projects[v.Session.ProjectID]
		expected := "fixtures/" + v.Session.ProjectID
		if v.Directory != expected && v.Directory != "worktrees/"+v.Session.ID {
			return errors.New("invalid demo fixture root")
		}
		if !idPattern.MatchString(v.Session.ID) || ids[v.Session.ID] || provider == "" || !validText(v.Session.Name, 200) || len(v.Session.Goal) > 8000 || len(v.Screen.Text) > demoMaxTranscript || !utf8.ValidString(v.Screen.Text) || demoSafeText(v.Screen.Text) != v.Screen.Text || v.Screen.Columns < 20 || v.Screen.Columns > 500 || v.Screen.Rows < 5 || v.Screen.Rows > 200 || v.TaskPhase < 0 || v.TaskPhase > 2 {
			return errors.New("invalid demo session")
		}
		ids[v.Session.ID] = true
	}
	allowed := map[string]bool{}
	for id := range projects {
		allowed["fixtures/"+id] = true
	}
	for _, v := range st.Sessions {
		allowed[v.Directory] = true
	}
	for directory, heading := range st.Headings {
		if !allowed[directory] || !validText(heading, 200) || demoSafeText(heading) != heading {
			return errors.New("invalid demo heading")
		}
	}
	return nil
}
func (d *Demo) seed() {
	d.state = demoState{Version: 1, Profile: d.profile, Next: 1, Headings: map[string]string{}, Revision: 1}
	providers := []string{d.profile}
	for _, provider := range providers {
		project := "demo-" + provider
		d.state.Projects = append(d.state.Projects, demoProject{project, "Demo · " + strings.ToUpper(provider[:1]) + provider[1:], provider}, demoProject{project + "-empty", "Demo · Empty project", provider})
		for i, item := range []struct {
			name, status, activity string
			hidden                 bool
		}{{"Welcome", "running", "idle", false}, {"Build preview", "running", "working", false}, {"Needs attention", "waiting", "waiting", false}, {"Archived example", "running", "idle", true}, {"Stopped example", "stopped", "idle", false}, {"Failed example", "failed", "idle", false}} {
			id := fmt.Sprintf("%s-%d", project, i+1)
			v := demoSession{Session: rawSession{ID: id, Name: item.name, ProjectID: project, Status: item.status, ActivityState: item.activity, CurrentAgent: "Demo", Mode: "terminal", TerminalLive: activeStatus(item.status), HiddenFromSidebar: item.hidden, Source: "terminal", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}, Directory: "fixtures/" + project, Screen: DemoSnapshot{Text: "Trill Demo — interactive sample workspace.\nType help to explore. No commands run on a shell.\n", Revision: 1, Live: activeStatus(item.status), Columns: 80, Rows: 24}}

			d.state.Sessions = append(d.state.Sessions, v)
		}
	}
}
func (d *Demo) persist() error {
	data, err := json.Marshal(d.state)
	if err != nil || len(data) > demoMaxState {
		return errors.New("demo state exceeds limit")
	}
	return d.atomicWrite("demo-state.json", data)
}
func (d *Demo) copyState() demoState {
	data, _ := json.Marshal(d.state)
	var copy demoState
	_ = json.Unmarshal(data, &copy)
	return copy
}
func (d *Demo) commit(before demoState) error {
	d.state.Revision++
	if err := d.persist(); err != nil {
		d.state = before
		return errUnavailable
	}
	// Fixture HTML is a derived view; restart repairs it from durable state.
	if err := d.renderHeadings(); err != nil {
		return errUnavailable
	}
	return nil
}
func (d *Demo) project(id string) (demoProject, bool) {
	for _, p := range d.state.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return demoProject{}, false
}
func (d *Demo) session(id string) (*demoSession, error) {
	if d.closed {
		return nil, errClosed
	}
	for i := range d.state.Sessions {
		if d.state.Sessions[i].Session.ID == id {
			return &d.state.Sessions[i], nil
		}
	}
	return nil, errNotFound
}
func (d *Demo) available(provider string) bool {
	return !d.faults["provider-unavailable"] || provider != "herdr"
}
func (d *Demo) materialize(v demoSession) rawSession {
	raw := v.Session
	p, _ := d.project(raw.ProjectID)
	raw.ProviderKey = "demo-" + p.Provider
	raw.HerdrTerminal = "term_" + raw.ID
	raw.WorktreePath = filepath.Join(d.rootPath, filepath.FromSlash(v.Directory))
	return raw
}
func (d *Demo) catalog() (catalog, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return catalog{}, errClosed
	}
	c := catalog{Projects: []Project{}, Raw: []rawSession{}, Providers: []ProviderStatus{}}
	providers := []string{d.profile}
	healthy := false
	for _, provider := range providers {
		available := d.available(provider)
		healthy = healthy || available
		status := ProviderStatus{ID: "demo-" + provider, Name: "Demo · " + provider, Available: available}
		if !available {
			status.Message = "Demo provider temporarily unavailable"
		}
		c.Providers = append(c.Providers, status)
	}
	if !healthy {
		return c, errUnavailable
	}
	for _, p := range d.state.Projects {
		if !d.available(p.Provider) {
			continue
		}
		caps := &Capabilities{TerminalStream: true, Rename: true, CloseTerminal: true, CreateTerminal: true, Files: true}

		c.Projects = append(c.Projects, Project{ID: p.ID, Name: p.Name, Provider: p.Provider, ProviderName: "Demo · " + p.Provider, Path: filepath.Join(d.rootPath, "fixtures", p.ID), Capabilities: caps})
	}
	for _, v := range d.state.Sessions {
		p, _ := d.project(v.Session.ProjectID)
		if d.available(p.Provider) {
			c.Raw = append(c.Raw, d.materialize(v))
		}
	}
	return c, nil
}
func (d *Demo) create(projectID, name, goal string, options CreateOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errClosed
	}
	p, ok := d.project(projectID)
	if !ok {
		return errNotFound
	}
	if !d.available(p.Provider) {
		return errUnavailable
	}
	if options.Kind != "terminal" || options.UseWorktree {
		return errUnsupported
	}
	if p.Provider == "herdr" && (options.Kind != "terminal" || options.UseWorktree) {
		return errUnsupported
	}
	if (name != "" && !validText(name, 200)) || len(goal) > 8000 || demoSafeText(name) != name || demoSafeText(goal) != goal || (options.Branch != "" && !validBranch(options.Branch)) {
		return errUnsupported
	}
	if len(d.state.Sessions) >= demoMaxSessions || d.state.Next >= 1_000_000 {
		return errCapacity
	}
	before := d.copyState()
	id := fmt.Sprintf("demo-created-%d", d.state.Next)
	d.state.Next++
	if name == "" {
		name = "Demo terminal"
	}
	raw := rawSession{ID: id, Name: name, ProjectID: projectID, Status: "running", ActivityState: "idle", CurrentAgent: "Demo", Mode: "terminal", TerminalLive: true, Goal: goal, Source: options.Kind, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	directory := "fixtures/" + projectID
	if options.Directory != "" {
		canonicalRoot, err := filepath.EvalSymlinks(d.rootPath)
		if err != nil {
			d.state = before
			return errFolderUnavailable
		}
		relativeDirectory, err := filepath.Rel(canonicalRoot, options.Directory)
		if err != nil || !filepath.IsLocal(relativeDirectory) {
			d.state = before
			return errFolderUnavailable
		}
		directory = filepath.ToSlash(relativeDirectory)
	}
	v := demoSession{Session: raw, Directory: directory, Screen: DemoSnapshot{Text: "Trill Demo — new sample session.\nType help to explore.\n", Revision: 1, Live: true, Columns: 80, Rows: 24}}
	if err := d.fixture(directory); err != nil {
		d.state = before
		return errUnavailable
	}
	d.state.Sessions = append(d.state.Sessions, v)
	if err := d.commit(before); err != nil {
		return err
	}
	if d.faults["create-unconfirmed"] {
		delete(d.faults, "create-unconfirmed")
		return errOutcomeUnknown
	}
	return nil
}
func (d *Demo) mutate(id, action, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.session(id)
	if err != nil {
		return err
	}
	p, _ := d.project(v.Session.ProjectID)
	if !d.available(p.Provider) {
		return errUnavailable
	}
	if p.Provider == "herdr" && action != "rename" {
		return errUnsupported
	}
	before := d.copyState()
	switch action {
	case "rename":
		if !validText(value, 200) || demoSafeText(value) != value {
			return errUnsupported
		}
		v.Session.Name = value
	default:
		return errUnsupported
	}
	v.Session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return d.commit(before)
}
func (d *Demo) closeSession(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.session(id); err != nil {
		return err
	}
	before := d.copyState()
	for index, session := range d.state.Sessions {
		if session.Session.ID == id {
			d.state.Sessions = append(d.state.Sessions[:index], d.state.Sessions[index+1:]...)
			return d.commit(before)
		}
	}
	return errNotFound
}

func (d *Demo) append(v *demoSession, text string) {
	v.Screen.Text += demoSafeText(text)
	if len(v.Screen.Text) > demoMaxTranscript {
		v.Screen.Text = v.Screen.Text[len(v.Screen.Text)-demoMaxTranscript:]
		for !utf8.ValidString(v.Screen.Text) {
			v.Screen.Text = v.Screen.Text[1:]
		}
	}
	v.Screen.Revision++
}
func (d *Demo) Snapshot(id string) (DemoSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.session(id)
	if err != nil {
		return DemoSnapshot{}, err
	}
	return v.Screen, nil
}
func (d *Demo) Resize(id string, cols, rows int) (DemoSnapshot, error) {
	if cols < 20 || cols > 500 || rows < 5 || rows > 200 {
		return DemoSnapshot{}, errUnsupported
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.session(id)
	if err != nil {
		return DemoSnapshot{}, err
	}
	if cols == v.Screen.Columns && rows == v.Screen.Rows {
		return v.Screen, nil
	}
	before := d.copyState()
	v.Screen.Columns = cols
	v.Screen.Rows = rows
	v.Screen.Revision++
	if err = d.commit(before); err != nil {
		return DemoSnapshot{}, err
	}
	return v.Screen, nil
}
func (d *Demo) prepareTerminal(id string, cols, rows int) error {
	d.mu.Lock()
	if d.faults["busy"] {
		delete(d.faults, "busy")
		d.mu.Unlock()
		return errTerminalBusy
	}
	if d.faults["capacity"] {
		delete(d.faults, "capacity")
		d.mu.Unlock()
		return errCapacity
	}
	v, err := d.session(id)
	if err == nil && (!v.Session.TerminalLive || !activeStatus(v.Session.Status)) {
		err = errNotAttachable
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if _, err = d.Resize(id, cols, rows); err != nil {
		return err
	}
	return nil
}

// Fault arms one-shot busy/capacity/create-unconfirmed faults. The persistent
// provider-unavailable fault hides the synthetic Herdr provider.
// clear disarms all faults. drop-leases stops only this service’s tracked terminal
// streams. preview-expired expires grants from this Demo’s registered HTTP
// instances, leaving production TTLs and other instances untouched.
// These methods are for local IPC, never public routing.
func (d *Demo) Fault(name string) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errClosed
	}
	if name == "preview-expired" {
		handlers := append([]*HTTP(nil), d.httpHandlers...)
		d.mu.Unlock()
		for _, h := range handlers {
			m := h.previews
			m.mu.Lock()
			for _, g := range m.grants {
				g.expires = time.Now().Add(-time.Second)
			}
			m.sweepLocked() // Production cancellation closes owned streams and sockets.
			m.mu.Unlock()
		}
		return nil
	}
	if name == "drop-leases" {
		d.mu.Unlock()
		d.service.mu.Lock()
		ids := make([]string, 0, len(d.service.leases))
		for id := range d.service.leases {
			ids = append(ids, id)
		}
		d.service.mu.Unlock()
		for _, id := range ids {
			d.service.DeleteLease(id)
		}
		return nil
	}
	defer d.mu.Unlock()
	switch name {
	case "clear":
		d.faults = map[string]bool{}
	case "busy", "capacity", "create-unconfirmed", "provider-unavailable":
		d.faults[name] = true
	default:
		return errors.New("unknown demo fault")
	}
	return nil
}
func (d *Demo) Status() any {
	d.mu.Lock()
	defer d.mu.Unlock()
	type item struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Status    string `json:"status"`
		Archived  bool   `json:"archived"`
		Revision  uint64 `json:"revision"`
		Columns   int    `json:"columns"`
		Rows      int    `json:"rows"`
	}
	sessions := []item{}
	for _, v := range d.state.Sessions {
		p, _ := d.project(v.Session.ProjectID)
		sessions = append(sessions, item{v.Session.ID, p.ID, v.Session.Name, p.Provider, v.Session.Status, v.Session.HiddenFromSidebar || endedStatus(v.Session.Status), v.Screen.Revision, v.Screen.Columns, v.Screen.Rows})
	}
	faults := []string{}
	for _, name := range []string{"busy", "capacity", "provider-unavailable", "create-unconfirmed"} {
		if d.faults[name] {
			faults = append(faults, name)
		}
	}
	return struct {
		Version  int      `json:"version"`
		Profile  string   `json:"profile"`
		Revision uint64   `json:"revision"`
		Closed   bool     `json:"closed"`
		Sessions []item   `json:"sessions"`
		Faults   []string `json:"faults"`
	}{1, d.profile, d.state.Revision, d.closed, sessions, faults}
}
func (d *Demo) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.closeDone
		return d.closeErr
	}
	d.closed = true
	close(d.done)
	d.mu.Unlock()
	<-d.stopped
	d.service.Close()
	d.mu.Lock()
	d.closeErr = d.root.Close()
	close(d.closeDone)
	d.mu.Unlock()
	return d.closeErr
}
func (d *Demo) tasks() {
	defer close(d.stopped)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.done:
			return
		case now := <-ticker.C:
			d.mu.Lock()
			if !d.closed {
				before := d.copyState()
				changed := false
				for i := range d.state.Sessions {
					v := &d.state.Sessions[i]
					if !v.TaskDue.IsZero() && !now.Before(v.TaskDue) {
						v.TaskPhase++
						changed = true
						if v.TaskPhase == 1 {
							d.append(v, "Checking sample files… done.\n")
							v.TaskDue = now.Add(500 * time.Millisecond)
						} else {
							d.append(v, "Demo task complete. Open web/index.html or the live preview.\n")
							v.TaskDue = time.Time{}
							v.Session.ActivityState = "idle"
						}
					}
				}
				if changed {
					_ = d.commit(before)
				}
			}
			d.mu.Unlock()
		}
	}
}
