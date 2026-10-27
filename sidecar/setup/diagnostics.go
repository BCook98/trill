package setup

import (
	"context"
	"errors"
	"os"
)

type AgentDiagnostic struct {
	Label  string
	Status string
	Err    error
}

// Diagnostics checks both on-disk and loaded identities without changing jobs.
func (a Agents) Diagnostics(ctx context.Context, c Config, dir string) []AgentDiagnostic {
	findings := make([]AgentDiagnostic, 0, 2)
	for _, renewal := range []bool{false, true} {
		label := Label
		if renewal {
			label = RenewalLabel
		}
		finding := AgentDiagnostic{Label: label}
		if err := a.verify(c, dir, renewal); err != nil {
			finding.Err = err
		} else {
			job, err := a.job(ctx, c, dir, renewal)
			finding.Err = err
			finding.Status = "stopped"
			if job.present {
				finding.Status = "loaded"
			}
			if job.running {
				finding.Status = "running"
			}
		}
		findings = append(findings, finding)
	}
	return findings
}

// RequireNoServices prevents a missing/incorrect --state-dir from authorizing
// binary replacement underneath a service owned by another configuration.
func (a Agents) RequireNoServices(ctx context.Context) error {
	for _, renewal := range []bool{false, true} {
		if _, err := os.Lstat(a.path(renewal)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("companion LaunchAgents exist; supply their original --state-dir before updating")
		}
		label := Label
		if renewal {
			label = RenewalLabel
		}
		if _, err := a.Runner.Run(ctx, "/bin/launchctl", "print", a.domain()+"/"+label); !errors.Is(err, errNotLoaded) {
			return errors.New("companion service ownership cannot be verified without its configuration; supply the original --state-dir")
		}
	}
	return nil
}
