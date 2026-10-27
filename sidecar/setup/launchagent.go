package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// LaunchAgent contains no shell interpolation. All user supplied text is escaped
// as XML, including executable paths, names and dependency PATH entries.
func LaunchAgent(c Config, dir string, renew bool) ([]byte, error) {
	if err := c.Validate(dir); err != nil {
		return nil, err
	}
	label := Label
	args := agentArgs(c, dir, renew)
	if renew {
		label = RenewalLabel
	}
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	esc := func(s string) { _ = xml.EscapeText(&b, []byte(s)) }
	str := func(s string) { b.WriteString("<string>"); esc(s); b.WriteString("</string>\n") }
	b.WriteString("<key>Label</key>")
	str(label)
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, a := range args {
		str(a)
	}
	b.WriteString("</array>\n")
	b.WriteString("<key>EnvironmentVariables</key><dict><key>PATH</key>")
	str(c.Path)
	b.WriteString("</dict>\n")
	b.WriteString("<key>WorkingDirectory</key>")
	str(dir)
	b.WriteString("<key>RunAtLoad</key><true/>\n<key>Umask</key><integer>63</integer>\n<key>ProcessType</key><string>Background</string>\n")
	if renew {
		b.WriteString("<key>StartInterval</key><integer>21600</integer>\n")
	} else {
		b.WriteString("<key>KeepAlive</key><true/>\n<key>ThrottleInterval</key><integer>30</integer>\n")
	}
	// Pair invitations are printed only by interactive trillctl, never a service.
	b.WriteString("<key>StandardOutPath</key>")
	str(filepath.Join(dir, label+".log"))
	b.WriteString("<key>StandardErrorPath</key>")
	str(filepath.Join(dir, label+".log"))
	b.WriteString("</dict></plist>\n")
	return b.Bytes(), nil
}

type Agents struct {
	Runner      Runner
	Home        string
	UID         int
	ListenCheck func(string) error
}

func (a Agents) domain() string { return "gui/" + strconv.Itoa(a.UID) }
func (a Agents) path(renew bool) string {
	label := Label
	if renew {
		label = RenewalLabel
	}
	return filepath.Join(a.Home, "Library", "LaunchAgents", label+".plist")
}
func (a Agents) expected(c Config, dir string, renew bool) ([]byte, error) {
	return LaunchAgent(c, dir, renew)
}
func (a Agents) verify(c Config, dir string, renew bool) error {
	want, err := a.expected(c, dir, renew)
	if err != nil {
		return err
	}
	got, err := ReadPrivate(a.path(renew))
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("LaunchAgent differs from this companion configuration; refusing to replace or stop an unowned service")
	}
	return nil
}
func (a Agents) Installed(c Config, dir string) error { return a.verify(c, dir, false) }

// Call while holding the service/configuration lock. A caller may have loaded
// its snapshot before another command committed a control/provider change.
func requireCurrentConfig(c Config, dir string) error {
	saved, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	if saved != c {
		return errors.New("configuration changed; retry with current configuration")
	}
	return nil
}
func (a Agents) Install(c Config, dir string) error {
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := requireCurrentConfig(c, dir); err != nil {
		return err
	}
	folder := filepath.Dir(a.path(false))
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	// Preflight both files before creating anything. Existing services belonging to
	// another state directory are never overwritten, even with the same label.
	for _, renew := range []bool{false, true} {
		_, err := os.Lstat(a.path(renew))
		if err == nil {
			if err = a.verify(c, dir, renew); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, label := range []string{Label, RenewalLabel} {
		path := filepath.Join(dir, label+".log")
		if _, err := os.Lstat(path); err == nil {
			if err = privateLog(path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, label := range []string{Label, RenewalLabel} {
		path := filepath.Join(dir, label+".log")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err = WritePrivate(path, nil, false); err != nil {
				return err
			}
		}
	}
	for _, renew := range []bool{false, true} {
		b, err := a.expected(c, dir, renew)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(a.path(renew)); errors.Is(err, os.ErrNotExist) {
			if err = WritePrivate(a.path(renew), b, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func agentArgs(c Config, dir string, renew bool) []string {
	if renew {
		return []string{c.CLI, "renew", "--state-dir", dir}
	}
	return append([]string{c.Daemon}, DaemonArgs(c, dir)...)
}

type loadedJob struct {
	present, running bool
	program          string
	args             []string
}

// launchctl print is human-readable. Accept only unambiguous top-level fields;
// malformed or unfamiliar successful output must never authorize a service action.
func parseLoadedJob(data []byte) (loadedJob, error) {
	job := loadedJob{present: true}
	depth, programs, arguments := 0, 0, 0
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if depth == 1 {
			if strings.HasPrefix(line, "program = ") {
				programs++
				job.program = strings.TrimPrefix(line, "program = ")
			}
			if line == "state = running" {
				job.running = true
			}
			if line == "arguments = {" {
				arguments++
				indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
				closed := false
				for i++; i < len(lines); i++ {
					raw := lines[i]
					if raw == indent+"}" {
						closed = true
						break
					}
					value := strings.TrimSpace(raw)
					if value == "" || !strings.HasPrefix(raw, indent) || len(raw) <= len(indent) || (raw[len(indent)] != ' ' && raw[len(indent)] != '\t') {
						return loadedJob{}, errors.New("cannot verify loaded LaunchAgent arguments")
					}
					job.args = append(job.args, value)
				}
				if !closed {
					return loadedJob{}, errors.New("incomplete loaded LaunchAgent arguments")
				}
				continue
			}
		}
		if strings.HasSuffix(line, " = {") || strings.HasSuffix(line, " => {") {
			depth++
		} else if line == "}" {
			depth--
		}
		if depth < 0 {
			return loadedJob{}, errors.New("invalid loaded LaunchAgent output")
		}
	}
	if depth != 0 || programs != 1 || arguments != 1 || job.program == "" || len(job.args) == 0 {
		return loadedJob{}, errors.New("cannot verify loaded LaunchAgent program and arguments")
	}
	return job, nil
}

func (a Agents) job(ctx context.Context, c Config, dir string, renew bool, alternatives ...Config) (loadedJob, error) {
	label := Label
	if renew {
		label = RenewalLabel
	}
	b, err := a.Runner.Run(ctx, "/bin/launchctl", "print", a.domain()+"/"+label)
	if errors.Is(err, errNotLoaded) {
		return loadedJob{}, nil
	}
	if err != nil {
		return loadedJob{}, errors.New("cannot determine loaded LaunchAgent ownership")
	}
	job, err := parseLoadedJob(b)
	if err != nil {
		return loadedJob{}, err
	}
	for _, candidate := range append([]Config{c}, alternatives...) {
		args := agentArgs(candidate, dir, renew)
		if job.program == args[0] && reflect.DeepEqual(job.args, args) {
			return job, nil
		}
	}
	return loadedJob{}, errors.New("loaded LaunchAgent program or arguments belong to another configuration; refusing service action")
}

const (
	unloadTimeout      = 30 * time.Second
	unloadPollInterval = 100 * time.Millisecond
)

// bootout can return while launchd still holds the terminating job. Keep the
// service/control lock until print confirms its removal, checking ownership on
// every poll. Neither a query failure nor a stopped process proves removal.
func (a Agents) waitUnloaded(ctx context.Context, c Config, dir string, renew bool) error {
	ctx, cancel := context.WithTimeout(ctx, unloadTimeout)
	defer cancel()
	ticker := time.NewTicker(unloadPollInterval)
	defer ticker.Stop()
	label := Label
	if renew {
		label = RenewalLabel
	}
	failure := func(err error) error {
		return fmt.Errorf("LaunchAgent %s unload not confirmed; inspect status --state-dir %q and retry after resolving launchd errors: %w", label, dir, err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return failure(err)
		}
		job, err := a.job(ctx, c, dir, renew)
		if ctx.Err() != nil {
			return failure(ctx.Err())
		}
		if err != nil {
			return failure(err)
		}
		if !job.present {
			return nil
		}
		select {
		case <-ctx.Done():
			return failure(ctx.Err())
		case <-ticker.C:
		}
	}
}

// The committed config is authoritative. Recovery accepts an exact control-flag
// inversion or a privately journaled previous provider/control configuration;
// executable, network, credential-store and service identities cannot change.
func (a Agents) repairControl(ctx context.Context, c Config, dir string) error {
	if err := a.verify(c, dir, false); err == nil {
		return nil
	}
	saved, err := LoadConfig(dir)
	if err != nil || saved != c {
		return errors.New("cannot repair an unowned LaunchAgent configuration")
	}
	old := c
	old.AllowControl = !c.AllowControl
	if err := a.verify(old, dir, false); err != nil {
		b, readErr := ReadPrivate(filepath.Join(dir, "config.previous.json"))
		old = Config{}
		if readErr != nil || json.Unmarshal(b, &old) != nil || old.Validate(dir) != nil {
			return err
		}
		allowed := old
		allowed.Backends = c.Backends
		allowed.HerdrPath = c.HerdrPath
		allowed.HerdrSessions = c.HerdrSessions
		allowed.HerdrConfigHome = c.HerdrConfigHome
		allowed.AllowControl = c.AllowControl
		if allowed != c {
			return errors.New("previous configuration changes fields outside provider/control settings")
		}
		if err := a.verify(old, dir, false); err != nil {
			return err
		}
	}

	if err := a.verify(c, dir, true); err != nil {
		return err
	}
	// Preflight both loaded identities before stopping even one service.
	if _, err := a.job(ctx, c, dir, true); err != nil {
		return err
	}
	job, err := a.job(ctx, old, dir, false, c)
	if err != nil {
		return err
	}
	if job.present && !reflect.DeepEqual(job.args, agentArgs(c, dir, false)) {
		if _, err := a.Runner.Run(ctx, "/bin/launchctl", "bootout", a.domain()+"/"+Label); err != nil {
			return errors.New("could not stop owned companion during control recovery")
		}
		if err := a.waitUnloaded(ctx, old, dir, false); err != nil {
			return err
		}
	}
	plist, err := a.expected(c, dir, false)
	if err != nil {
		return err
	}
	if err := WritePrivate(a.path(false), plist, true); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(a.path(false)))
}
func (a Agents) Start(ctx context.Context, c Config, dir string) error {
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := requireCurrentConfig(c, dir); err != nil {
		return err
	}
	if err := a.repairControl(ctx, c, dir); err != nil {
		return err
	}
	// Check ownership before querying or manipulating the shared stable label.
	for _, renew := range []bool{false, true} {
		if err := a.verify(c, dir, renew); err != nil {
			return err
		}
	}
	daemon, err := a.job(ctx, c, dir, false)
	if err != nil {
		return err
	}
	renewal, err := a.job(ctx, c, dir, true)
	if err != nil {
		return err
	}
	if !daemon.present {
		check := a.ListenCheck
		if check == nil {
			check = CheckListener
		}
		if err := check(c.ListenIP()); err != nil {
			return err
		}
		if _, err := a.Runner.Run(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.path(false)); err != nil {
			return errors.New("LaunchAgent bootstrap failed; inspect status and companion log")
		}
	}
	if !renewal.present {
		if _, err := a.Runner.Run(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.path(true)); err != nil {
			return errors.New("certificate renewal LaunchAgent bootstrap failed; daemon was left running")
		}
	}
	return nil
}
func (a Agents) Stop(ctx context.Context, c Config, dir string) error {
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return a.stop(ctx, c, dir)
}

func (a Agents) stop(ctx context.Context, c Config, dir string) error {
	for _, renew := range []bool{false, true} {
		if err := a.verify(c, dir, renew); err != nil {
			return err
		}
	}
	daemon, err := a.job(ctx, c, dir, false)
	if err != nil {
		return err
	}
	renewal, err := a.job(ctx, c, dir, true)
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		label   string
		renew   bool
		present bool
	}{{RenewalLabel, true, renewal.present}, {Label, false, daemon.present}} {
		if entry.present {
			// The previous job's unload may have taken time. Recheck before
			// acting so its wait cannot leave us using a stale owned identity.
			job, err := a.job(ctx, c, dir, entry.renew)
			if err != nil {
				return err
			}
			if !job.present {
				continue
			}
			if _, err := a.Runner.Run(ctx, "/bin/launchctl", "bootout", a.domain()+"/"+entry.label); err != nil {
				return fmt.Errorf("could not stop owned LaunchAgent %s", entry.label)
			}
			if err := a.waitUnloaded(ctx, c, dir, entry.renew); err != nil {
				return err
			}
		}
	}

	return nil
}
func (a Agents) Uninstall(ctx context.Context, c Config, dir string) error {
	unlock, err := lockControl(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.stop(ctx, c, dir); err != nil {
		return err
	}
	for _, renew := range []bool{false, true} {
		if err := a.verify(c, dir, renew); err != nil {
			return err
		}
		if err := os.Remove(a.path(renew)); err != nil {
			return err
		}
	}
	return nil
}
func (a Agents) Status(ctx context.Context, c Config, dir string) (string, error) {
	if err := a.Installed(c, dir); err != nil {
		return "not installed or ownership mismatch", err
	}
	job, err := a.job(ctx, c, dir, false)
	if err != nil {
		return "ownership unknown or mismatch", err
	}
	if !job.present {
		return "stopped", nil
	}
	if job.running {
		return "running", nil
	}
	return "loaded (not running; inspect companion log)", nil
}

func privateLog(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return errors.New("companion log must be a regular 0600 file")
	}
	return nil
}
