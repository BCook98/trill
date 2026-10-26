package sidecar

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
)

func (s *Service) sessionManagement() bool {
	if s.demo != nil {
		return s.demo.profile != "herdr"
	}
	if len(s.sources) > 0 {
		for _, p := range s.sources {
			if p.sessionManagement() {
				return true
			}
		}
		return false
	}
	return s.config.HerdrPath == "" && s.config.TmuxSocket == ""
}
func (s *Service) backendName() string {
	if s.demo != nil {
		return s.demo.profile
	}
	if len(s.sources) > 0 {
		return "mixed"
	}
	if s.config.HerdrPath != "" {
		return "herdr"
	}
	if s.config.TmuxSocket != "" {
		return "terminal-lab"
	}
	return "xirp"
}

// The transport owns mosh leases; each backend supplies a validated attach argv.
// Never execute an attach command received from the phone or metadata.
func (s *Service) attachCommand(ctx context.Context, v rawSession, cols, rows int) ([]string, error) {
	if s.demo != nil {
		return s.demo.attach(v.ID, cols, rows)
	}
	if len(s.sources) > 0 {
		owner := s.source(v.ProviderKey)
		if owner == nil {
			return nil, errNotFound
		}
		return owner.attachCommand(ctx, v, cols, rows)
	}
	if v.Mode != "terminal" || !v.TerminalLive || !activeStatus(v.Status) {
		return nil, errNotAttachable
	}
	if s.config.HerdrPath != "" {
		if !herdrTerminalPattern.MatchString(v.HerdrTerminal) {
			return nil, errNotAttachable
		}
		path := s.config.HerdrPath
		if !filepath.IsAbs(path) {
			var err error
			path, err = exec.LookPath(path)
			if err != nil {
				return nil, errUnavailable
			}
		}
		checker, ok := s.runner.(interface {
			CheckHerdrAttach(context.Context, string, []string, []string) error
		})
		if !ok {
			return nil, errUnavailable
		}
		if err := checker.CheckHerdrAttach(ctx, path, []string{"--session", s.config.HerdrSession, "terminal", "session", "control", v.HerdrTerminal, "--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows)}, s.herdrEnv()); err != nil {
			return nil, err
		}
		return []string{path, "--session", s.config.HerdrSession, "terminal", "attach", v.HerdrTerminal}, nil
	}
	if !tmuxPattern.MatchString(v.TmuxSession) {
		return nil, errNotAttachable
	}
	tmuxPath := s.config.TmuxPath
	if !filepath.IsAbs(tmuxPath) {
		path, err := exec.LookPath(tmuxPath)
		if err != nil {
			return nil, errUnavailable
		}
		tmuxPath, err = filepath.Abs(path)
		if err != nil {
			return nil, errUnavailable
		}
	}
	args := []string{}
	if s.config.TmuxSocket != "" {
		args = append(args, "-S", s.config.TmuxSocket)
	}
	check := append(append([]string{}, args...), "has-session", "-t", "="+v.TmuxSession)
	if _, err := s.command(ctx, tmuxPath, check...); err != nil {
		return nil, errNotAttachable
	}
	args = append(args, "attach-session", "-t", "="+v.TmuxSession)
	return append([]string{tmuxPath}, args...), nil
}
