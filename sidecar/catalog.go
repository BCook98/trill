package sidecar

import (
	"context"
	"strings"
	"time"
)

// Capabilities are evaluated per item. Missing fields in older v1 clients remain
// harmless; unsupported capabilities stay disabled.
type Capabilities struct {
	TerminalStream bool `json:"terminalStream,omitempty"`
	Rename         bool `json:"rename"`
	CreateTerminal bool `json:"createTerminal"`
	Worktree       bool `json:"worktree"`
	Files          bool `json:"files"`
}
type ProviderStatus struct {
	CreateSpace bool   `json:"createSpace,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Available   bool   `json:"available"`
	Message     string `json:"message,omitempty"`
}
type catalog struct {
	Projects  []Project
	Raw       []rawSession
	Providers []ProviderStatus
}

func (s *Service) configureSources() error {
	var configs []Config
	for _, serverName := range strings.Split(s.config.HerdrSessions, ",") {
		configuration := s.config
		configuration.HerdrSession = serverName
		configuration.HerdrSessions = serverName
		configs = append(configs, configuration)
	}
	if len(configs) == 1 {
		s.config = configs[0]
		s.sourceKey = s.backendName()
		if s.config.HerdrPath != "" {
			s.sourceKey += ":" + s.config.HerdrSession
		}
		return nil
	}
	for _, c := range configs {
		child, err := NewService(c, s.runner)
		if err != nil {
			return err
		}
		s.sources = append(s.sources, child)
	}
	return nil
}
func (s *Service) providerName() string {
	if s.config.HerdrPath != "" {
		return "Herdr · " + s.config.HerdrSession
	}
	return "Herdr"
}
func (s *Service) capabilities(files bool) *Capabilities {
	return &Capabilities{TerminalStream: true, Rename: true, CreateTerminal: true, Files: files}
}
func (s *Service) localCatalog(ctx context.Context) (catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var projects []Project
	var raw []rawSession
	var err error
	projects, raw, err = s.readMetadata(ctx)
	if err != nil {
		return catalog{}, err
	}
	for i := range projects {
		projects[i].Provider = s.backendName()
		projects[i].ProviderName = s.providerName()
		projects[i].Capabilities = s.capabilities(projects[i].Path != "")
		if s.config.HerdrPath != "" {
			projects[i].Capabilities.Worktree = projects[i].Path != "" && !projects[i].IsWorktree
		}
		if s.config.HerdrLabRoot != "" {
			projects[i].Capabilities = &Capabilities{Files: true, TerminalStream: true}
		}
	}
	for i := range raw {
		raw[i].ProviderKey = s.sourceKey
	}
	return catalog{Projects: projects, Raw: raw}, nil
}
func (s *Service) catalog(ctx context.Context) (catalog, error) {
	if err := ctx.Err(); err != nil {
		return catalog{}, err
	}
	if s.demo != nil {
		return s.demo.catalog()
	}
	sources := s.sources
	if len(sources) == 0 {
		sources = []*Service{s}
	}
	type result struct {
		c   catalog
		err error
	}
	results := make([]chan result, len(sources))
	for i, source := range sources {
		ch := make(chan result, 1)
		results[i] = ch
		go func(p *Service) { c, err := p.localCatalog(ctx); ch <- result{c, err} }(source)
	}
	out := catalog{Projects: []Project{}, Raw: []rawSession{}, Providers: []ProviderStatus{}}
	healthy := 0
	projects, sessions := map[string]bool{}, map[string]bool{}
	for i, ch := range results {
		var r result
		select {
		case r = <-ch:
		case <-ctx.Done():
			return catalog{}, ctx.Err()
		}
		source := sources[i]
		status := ProviderStatus{ID: source.sourceKey, Name: source.providerName(), Available: r.err == nil, CreateSpace: r.err == nil && source.config.HerdrPath != "" && source.config.HerdrLabRoot == ""}
		if r.err != nil {
			status.Message = status.Name + " is unavailable. Check that it is running and its CLI is compatible."
		} else {
			// Reject duplicate public identities instead of routing an action ambiguously.
			for _, p := range r.c.Projects {
				if projects[p.ID] {
					return catalog{}, errUnavailable
				}
				projects[p.ID] = true
			}
			for _, v := range r.c.Raw {
				if sessions[v.ID] {
					return catalog{}, errUnavailable
				}
				sessions[v.ID] = true
			}
			out.Projects = append(out.Projects, r.c.Projects...)
			out.Raw = append(out.Raw, r.c.Raw...)
			healthy++
		}
		out.Providers = append(out.Providers, status)
	}
	if err := ctx.Err(); err != nil {
		return catalog{}, err
	}
	if healthy == 0 {
		return out, errUnavailable
	}
	return out, nil
}
func (s *Service) source(key string) *Service {
	if len(s.sources) == 0 {
		if s.sourceKey == key {
			return s
		}
		return nil
	}
	for _, p := range s.sources {
		if p.sourceKey == key {
			return p
		}
	}
	return nil
}
func (s *Service) sessionOwner(ctx context.Context, id string) (*Service, rawSession, error) {
	v, err := s.find(ctx, id)
	if err != nil {
		return nil, v, err
	}
	owner := s.source(v.ProviderKey)
	if owner == nil {
		return nil, v, errNotFound
	}
	return owner, v, nil
}
func (s *Service) projectOwner(ctx context.Context, id string) (*Service, error) {
	c, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range c.Projects {
		if p.ID != id {
			continue
		}
		for _, source := range s.sources {
			if source.providerName() == p.ProviderName && source.backendName() == p.Provider {
				return source, nil
			}
		}
	}
	return nil, errNotFound
}
func (c catalog) sessions() []Session {
	projects := map[string]Project{}
	for _, p := range c.Projects {
		projects[p.ID] = p
	}
	out := make([]Session, 0, len(c.Raw))
	for _, v := range c.Raw {
		p := projects[v.ProjectID]
		caps := p.Capabilities
		if caps != nil {
			copy := *caps
			copy.Files = v.WorktreePath != "" || p.Path != ""
			caps = &copy
		}
		attachable := v.Mode == "terminal" && v.TerminalLive && activeStatus(v.Status) && v.HerdrTerminal != ""
		out = append(out, Session{ID: v.ID, Name: v.Name, ProjectID: v.ProjectID, ProjectName: p.Name, Status: v.Status, ActivityState: v.ActivityState, Agent: v.CurrentAgent, Branch: v.Branch, UpdatedAt: v.UpdatedAt, Attachable: attachable, Archived: v.HiddenFromSidebar || endedStatus(v.Status), Resumable: false, Provider: p.Provider, ProviderName: p.ProviderName, Capabilities: caps})
	}
	return out
}

// Used by scoped reads (uploads/files/actions) so unrelated project/session
// discovery cannot broaden or break an otherwise independent operation.
func collectSourceValues[T any](ctx context.Context, sources []*Service, read func(*Service) ([]T, error)) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		values []T
		err    error
	}
	channels := make([]chan result, len(sources))
	for i, source := range sources {
		ch := make(chan result, 1)
		channels[i] = ch
		go func(p *Service) { v, err := read(p); ch <- result{v, err} }(source)
	}
	out := []T{}
	healthy := false
	for _, ch := range channels {
		select {
		case r := <-ch:
			if r.err == nil {
				healthy = true
				out = append(out, r.values...)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !healthy {
		return nil, errUnavailable
	}
	return out, nil
}
