package sidecar

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opt-in real probe. Every tmux command names this test's private socket.
func TestRealMoshOnIsolatedTmuxSocket(t *testing.T) {
	if os.Getenv("TRILL_REAL_MOSH_TEST") != "1" {
		t.Skip("set TRILL_REAL_MOSH_TEST=1 for isolated integration")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	mosh, err := exec.LookPath("mosh-server")
	if err != nil {
		t.Skip("mosh-server unavailable")
	}
	tmux, err = filepath.Abs(tmux)
	if err != nil {
		t.Fatal(err)
	}
	// macOS's default test temp path exceeds the Unix socket path limit.
	dir, err := os.MkdirTemp("/tmp", "trill-mosh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "isolated.sock")
	start := exec.Command(tmux, "-S", socket, "new-session", "-d", "-s", "trill-probe", "sleep 120")
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("isolated tmux start: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	portText := strconv.Itoa(port)
	args := []string{"new", "-i", "127.0.0.1", "-p", portText + ":" + portText, "-c", "256", "-l", "LANG=en_US.UTF-8", "--", tmux, "-S", socket, "attach-session", "-t", "=trill-probe"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, line, err := (ExecRunner{}).StartMosh(ctx, mosh, args, []string{"MOSH_SERVER_NETWORK_TMOUT=600", "MOSH_SERVER_SIGNAL_TMOUT=600"})
	if err != nil {
		t.Fatal("real mosh startup failed", err)
	}
	t.Cleanup(proc.Stop)
	if m := connectLine.FindStringSubmatch(line); len(m) != 3 || m[1] != portText {
		t.Fatal("invalid real mosh connection line")
	}
	owned, ok := proc.(*execMosh)
	if !ok || !owned.owned() {
		t.Fatal("detached mosh PID is not in its launch process group")
	}
	proc.Stop()
	select {
	case <-proc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("owned mosh process did not exit")
	}
	probe := exec.Command(tmux, "-S", socket, "has-session", "-t", "=trill-probe")
	if err := probe.Run(); err != nil {
		t.Fatal("stopping mosh affected isolated tmux session")
	}
	rebind, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal("mosh UDP port remained bound after lease cleanup")
	}
	rebind.Close()
}

func TestForcedMoshStopWaitsForExitConfirmation(t *testing.T) {
	// An isolated process ignores TERM, forcing the runner's KILL fallback.
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; printf 'ready\\n'; exec sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if ready, err := bufio.NewReader(out).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatal("process did not become ready", err)
	}
	p := &execMosh{pid: cmd.Process.Pid, pgid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		// Model the production poller's delay between exit and notification.
		time.Sleep(100 * time.Millisecond)
		close(p.done)
	}()
	p.Stop()
	select {
	case <-p.done:
	default:
		t.Fatal("Stop returned before exit was confirmed")
	}
	if p.owned() {
		t.Fatal("owned process survived forced cleanup")
	}
}

// This probe owns a fresh config, named server, repository, and every terminal.
func TestRealHerdrSpaceAndWorktreeCreation(t *testing.T) {
	if os.Getenv("TRILL_REAL_HERDR_TEST") != "1" {
		t.Skip("set TRILL_REAL_HERDR_TEST=1 for isolated Herdr integration")
	}
	binary, err := exec.LookPath("herdr")
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("/tmp", "trill-herdr-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	// Do not inherit routing to the user's currently running Herdr server.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "HERDR_") {
			t.Setenv(key, "")
		}
	}
	configHome := filepath.Join(directory, "config")
	configDirectory := filepath.Join(configHome, "herdr")
	if err := os.MkdirAll(configDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	configuration := "onboarding = false\n[worktrees]\ndirectory = " + strconv.Quote(filepath.Join(directory, "worktrees")) + "\n[terminal]\ndefault_shell = \"/bin/sh\"\nshell_mode = \"non_login\"\n[update]\nversion_check = false\nmanifest_check = false\n"
	if err := os.WriteFile(filepath.Join(configDirectory, "config.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(configDirectory, "config.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TERM", "xterm-256color")
	repository := filepath.Join(directory, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Trill Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Synthetic repository"}} {
		command := exec.Command("git", arguments...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	const serverName = "trill-creation-test"
	server := exec.Command(binary, "--session", serverName, "server")
	server.Dir = directory
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "--session", serverName, "server", "stop").Run()
		done := make(chan error, 1)
		go func() { done <- server.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = server.Process.Kill()
			<-done
		}
	})
	service, err := NewService(Config{Backends: "herdr", HerdrPath: binary, HerdrConfigHome: configHome, HerdrSessions: serverName, AllowControl: true}, ExecRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err := service.catalog(ctx); err == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated Herdr server did not become ready")
	}
	if err := service.CreateSpace(ctx, service.sourceKey, "Synthetic space", repository); err != nil {
		t.Fatal("create space:", err)
	}
	catalog, err := service.catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var root Project
	for _, project := range catalog.Projects {
		if project.Name == "Synthetic space" {
			root = project
		}
	}
	if root.ID == "" {
		t.Fatal("created space missing")
	}
	// Herdr may populate Git provenance asynchronously after workspace creation.
	for deadline := time.Now().Add(5 * time.Second); root.Path == "" && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		catalog, err = service.catalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, project := range catalog.Projects {
			if project.ID == root.ID {
				root = project
			}
		}
	}
	if err := service.Create(ctx, root.ID, "Phone feature", "", CreateOptions{Kind: "worktree", UseWorktree: true, Branch: "feature/phone"}); err != nil {
		t.Fatal("create worktree:", err)
	}
	catalog, err = service.catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, project := range catalog.Projects {
		if project.Name == "Phone feature" {
			found = true
			if !project.IsWorktree || project.ParentID != root.ID || project.Branch != "feature/phone" {
				t.Fatalf("worktree hierarchy: %+v", project)
			}
			if err := service.Create(ctx, project.ID, "", "", CreateOptions{Kind: "terminal"}); err != nil {
				t.Fatal("create terminal:", err)
			}
		}
	}
	if !found {
		t.Fatal("created worktree missing")
	}
}
