package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"trill/sidecar/providers"
)

// Control is an explicit local permission change. It never starts or replaces Herdr servers,
// and restarts only the daemon with this exact installed configuration. The
// certificate renewal service is left running. A stable lock serializes changes.
func (a Agents) Control(ctx context.Context, c Config, dir string, allow bool) error {
	next := c
	next.AllowControl = allow
	return a.changeConfiguration(ctx, c, next, dir)
}
func (a Agents) Providers(ctx context.Context, c Config, dir, backends, path, sessions, configHome string) error {
	selection, err := providers.Normalize(backends, path, sessions, false)
	if err != nil {
		return err
	}
	next := c
	next.Backends = selection.Backends
	next.HerdrPath = selection.HerdrPath
	next.HerdrSessions = selection.HerdrSessions
	next.HerdrConfigHome = ""
	if selection.HerdrPath != "" {
		next.HerdrConfigHome, err = providers.HerdrHome(configHome)
		if err != nil {
			return err
		}
	} else if configHome != "" {
		return errors.New("Herdr config home requires a Herdr backend")
	}
	return a.changeConfiguration(ctx, c, next, dir)
}
func (a Agents) changeConfiguration(ctx context.Context, c, next Config, dir string) error {
	if err := next.Validate(dir); err != nil {
		return err
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	if err := c.Validate(dir); err != nil {
		return err
	}
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	actual, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	if actual != c {
		return errors.New("configuration changed; retry with current configuration")
	}

	present := 0
	for _, renew := range []bool{false, true} {
		if _, err = os.Lstat(a.path(renew)); err == nil {
			present++
			if err = a.verify(c, dir, renew); err != nil {
				return fmt.Errorf("LaunchAgent mismatch; use start --state-dir %q to recover an interrupted configuration change: %w", dir, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if present == 1 {
		return fmt.Errorf("incomplete LaunchAgent installation; run install --state-dir %q before changing control", dir)
	}
	if c == next {
		return nil
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if present == 0 {
		if err := WritePrivate(filepath.Join(dir, "config.json"), b, true); err != nil {
			return err
		}
		return syncDirectory(dir)
	}
	newPlist, err := a.expected(next, dir, false)
	if err != nil {
		return err
	}
	job, err := a.job(ctx, c, dir, false)
	if err != nil {
		return err
	}
	if _, err := a.job(ctx, c, dir, true); err != nil {
		return err
	}
	// Make the recovery journal durable before disconnecting the companion.
	// A failed write must leave its running configuration and leases intact.
	oldConfig, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	previousPath := filepath.Join(dir, "config.previous.json")
	_, previousErr := os.Lstat(previousPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	if err = WritePrivate(previousPath, append(oldConfig, '\n'), previousErr == nil); err != nil {
		return err
	}
	if err = syncDirectory(dir); err != nil {
		return err
	}
	oldPlist, err := a.expected(c, dir, false)
	if err != nil {
		return err
	}
	running := job.present
	if running {
		if _, err = a.Runner.Run(ctx, "/bin/launchctl", "bootout", a.domain()+"/"+Label); err != nil {
			return errors.New("could not stop owned companion; configuration unchanged")
		}
		if err := a.waitUnloaded(ctx, c, dir, false); err != nil {
			return err
		}
	}
	restartOld := func() error {
		if !running {
			return nil
		}
		_, err := a.Runner.Run(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.path(false))
		return err
	}
	type change struct {
		path           string
		next, previous []byte
	}
	changes := []change{
		{filepath.Join(dir, "config.json"), b, append(oldConfig, '\n')},
		{a.path(false), newPlist, oldPlist},
	}
	// Disable commits the restrictive plist first; enable commits config first.
	if c.AllowControl && !next.AllowControl {
		changes[0], changes[1] = changes[1], changes[0]
	}
	for i, change := range changes {
		if err = WritePrivate(change.path, change.next, true); err != nil {
			if i > 0 {
				first := changes[0]
				if rollbackErr := WritePrivate(first.path, first.previous, true); rollbackErr != nil {
					return fmt.Errorf("finish interrupted configuration change with start --state-dir %q: %w", dir, rollbackErr)
				}
				if syncErr := syncDirectory(filepath.Dir(first.path)); syncErr != nil {
					return fmt.Errorf("finish interrupted configuration change with start --state-dir %q: %w", dir, syncErr)
				}
			}
			if restartOld() != nil {
				return fmt.Errorf("control unchanged; use start --state-dir %q after resolving: %w", dir, err)
			}
			return err
		}
		if err := syncDirectory(filepath.Dir(change.path)); err != nil {
			return fmt.Errorf("finish interrupted configuration change with start --state-dir %q: %w", dir, err)
		}
	}
	if running {
		if _, err = a.Runner.Run(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.path(false)); err != nil {
			return fmt.Errorf("configuration saved; companion restart failed; use start --state-dir %q after resolving launchd/listener errors", dir)
		}
	}
	return nil
}

// Service actions share the control lock so Start cannot race the two-file
// control commit and accidentally bootstrap the old permission setting.
func lockControl(dir string) (func(), error) {
	return lockPrivateState(dir, ".control.lock", "control", "another companion service/configuration change is in progress; retry shortly")
}

// LockTSNetState prevents enrollment, reauthentication and the daemon from
// opening the same embedded node concurrently. Hold until tsnet has closed.
func LockTSNetState(dir string) (func(), error) {
	return lockPrivateState(dir, ".node.lock", "embedded Tailscale", "embedded Tailscale is already in use; stop this companion before logging in")
}

func lockPrivateState(dir, filename, description, busyMessage string) (func(), error) {
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(dir, filename), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open private %s lock", description)
	}
	lock := os.NewFile(uintptr(fd), description+" lock")
	st, err := lock.Stat()
	if err != nil {
		lock.Close()
		return nil, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
		lock.Close()
		return nil, fmt.Errorf("%s lock must be a user-owned, single-link regular 0600 file", description)
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New(busyMessage)
	}
	return func() { syscall.Flock(fd, syscall.LOCK_UN); lock.Close() }, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
