package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LockMaintenance serializes installation changes using the same private lock
// validation as service/configuration changes. Callers release it after staging.
func LockMaintenance(dir string) (func(), error) { return lockControl(dir) }

type binaryUpdateJournal struct {
	PreviousTarget string `json:"previousTarget"`
	DaemonLoaded   bool   `json:"daemonLoaded"`
	RenewalLoaded  bool   `json:"renewalLoaded"`
}

// RecoverRelease restores an interrupted transaction before any network access.
func (a Agents) RecoverRelease(ctx context.Context, c Config, dir, root string) (bool, error) {
	unlock, err := lockControl(dir)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := a.verifyBinaryInstallation(c, dir, root); err != nil {
		return false, err
	}
	return a.recoverPendingRelease(ctx, c, dir, root)
}

func (a Agents) verifyBinaryInstallation(c Config, dir, root string) error {
	if err := requireCurrentConfig(c, dir); err != nil {
		return err
	}
	if c.CLI != filepath.Join(root, "bin", "trill") || c.Daemon != filepath.Join(root, "bin", "trilld") {
		return errors.New("source/custom installations require explicit migration before signed updates")
	}
	for _, renewal := range []bool{false, true} {
		if err := a.verify(c, dir, renewal); err != nil {
			return err
		}
	}
	return nil
}

func (a Agents) recoverPendingRelease(ctx context.Context, c Config, dir, root string) (bool, error) {
	journalPath := filepath.Join(dir, ".binary-update.json")
	if _, err := os.Lstat(journalPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	data, err := ReadPrivate(journalPath)
	if err != nil {
		return false, err
	}
	var journal binaryUpdateJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return false, errors.New("invalid binary update recovery journal")
	}
	if err := a.restoreBinaryUpdate(ctx, c, dir, root, journal); err != nil {
		return false, fmt.Errorf("interrupted update recovery failed: %w", err)
	}
	if err := removeUpdateJournal(journalPath); err != nil {
		return false, err
	}
	return true, nil
}

// SwitchRelease serializes with control/provider/start/stop commands. A durable
// journal permits recovery on the next update after a process or machine crash.
// Config and pairing state are never rewritten by a binary update.
func (a Agents) SwitchRelease(ctx context.Context, c Config, dir, installationRoot, nextTarget string, health func(context.Context) error) error {
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.verifyBinaryInstallation(c, dir, installationRoot); err != nil {
		return err
	}
	journalPath := filepath.Join(dir, ".binary-update.json")
	// Recover before considering a new version. Keep the journal until both the
	// old pointer and the exact previously loaded services have been restored.
	if recovered, err := a.recoverPendingRelease(ctx, c, dir, installationRoot); err != nil {
		return err
	} else if recovered {
		return errors.New("recovered an interrupted update; run trill update again")
	}
	previousTarget, err := os.Readlink(filepath.Join(installationRoot, "current"))
	if err != nil {
		return err
	}
	if !validReleaseTarget(previousTarget) || !validReleaseTarget(nextTarget) {
		return errors.New("invalid managed release target")
	}
	if previousTarget == nextTarget {
		return nil
	}
	daemon, err := a.job(ctx, c, dir, false)
	if err != nil {
		return err
	}
	renewal, err := a.job(ctx, c, dir, true)
	if err != nil {
		return err
	}
	journal := binaryUpdateJournal{previousTarget, daemon.present, renewal.present}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := WritePrivate(journalPath, data, false); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	operationErr := a.stop(ctx, c, dir)
	if operationErr == nil {
		operationErr = SetCurrentRelease(installationRoot, nextTarget)
	}
	if operationErr == nil {
		operationErr = a.restoreLoadedServices(ctx, c, dir, journal)
	}
	if operationErr == nil && journal.DaemonLoaded {
		operationErr = health(ctx)
	}
	if operationErr != nil {
		// Recovery gets an independent deadline when the update context expires.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := a.restoreBinaryUpdate(recoveryCtx, c, dir, installationRoot, journal); err != nil {
			return fmt.Errorf("update failed (%v); rollback incomplete (%v); run trill update again to recover", operationErr, err)
		}
		if err := removeUpdateJournal(journalPath); err != nil {
			return err
		}
		return fmt.Errorf("update failed; previous release and service state restored: %w", operationErr)
	}
	return removeUpdateJournal(journalPath)
}

func (a Agents) restoreBinaryUpdate(ctx context.Context, c Config, dir, root string, journal binaryUpdateJournal) error {
	if !validReleaseTarget(journal.PreviousTarget) {
		return errors.New("invalid previous release in update journal")
	}
	if err := a.stop(ctx, c, dir); err != nil {
		return err
	}
	if err := SetCurrentRelease(root, journal.PreviousTarget); err != nil {
		return err
	}
	return a.restoreLoadedServices(ctx, c, dir, journal)
}

func (a Agents) restoreLoadedServices(ctx context.Context, c Config, dir string, journal binaryUpdateJournal) error {
	for _, entry := range []struct{ renewal, loaded bool }{{false, journal.DaemonLoaded}, {true, journal.RenewalLoaded}} {
		if !entry.loaded {
			continue
		}
		if err := a.verify(c, dir, entry.renewal); err != nil {
			return err
		}
		job, err := a.job(ctx, c, dir, entry.renewal)
		if err != nil {
			return err
		}
		if !job.present {
			if _, err := a.Runner.Run(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.path(entry.renewal)); err != nil {
				return errors.New("could not restore owned LaunchAgent")
			}
		}
	}
	return nil
}

func validReleaseTarget(target string) bool {
	return strings.HasPrefix(target, "releases/") && filepath.Clean(target) == target && len(strings.Split(target, "/")) == 2 && target != "releases/"
}

// SetCurrentRelease switches the entire binary set with one same-directory
// atomic rename. Targets must be real private release directories.
func SetCurrentRelease(root, target string) error {
	if !validReleaseTarget(target) {
		return errors.New("invalid release target")
	}
	if err := checkPrivateDir(root); err != nil {
		return err
	}
	if err := checkPrivateDir(filepath.Join(root, "releases")); err != nil {
		return err
	}
	if err := checkPrivateDir(filepath.Join(root, target)); err != nil {
		return err
	}
	placeholder, err := os.CreateTemp(root, ".current-*")
	if err != nil {
		return err
	}
	name := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	defer os.Remove(name)
	if err := os.Symlink(target, name); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(root, "current")); err != nil {
		return err
	}
	return syncDirectory(root)
}

func removeUpdateJournal(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
