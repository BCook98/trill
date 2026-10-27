package demoapp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"trill/sidecar"
	"trill/sidecar/pairing"
	"trill/sidecar/setup"
)

const usage = `Usage: trill demo <command> --state-dir /absolute/private/directory

  serve       Run the isolated demo in the foreground (HTTPS terminal streams)
  pair        Issue a single-use invitation; --url, --json, or terminal QR
  status      Show owned server status; --json for smoke tooling
  fault NAME  Arm a private test fault; use clear to remove faults
  revoke ID   Revoke one demo device and its access
  reset       Reset a stopped demo's data and credentials; requires --yes
  stop        Ask only this demo's private control socket to shut down

No Herdr, shell, or coding agent is started. Demo workloads are scripted.
Use trill demo serve --help for network and reviewer options.
`

type options struct {
	root, listen, address, name, profile, preview, cert, key, reviewPassword string
	readOnly, flyEdgeTLS                                                     bool
	maxLeases                                                                int
	retention                                                                time.Duration
}

func Run(args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := io.WriteString(out, usage)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet("demo "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var opt options
	fs.StringVar(&opt.root, "state-dir", "", "new or existing owned private demo directory (required)")
	if command == "serve" {
		fs.StringVar(&opt.listen, "listen", "127.0.0.1:19443", "HTTPS bind address; never changes installed companion")
		fs.StringVar(&opt.address, "address", "", "HTTPS address reachable by the phone (default listen address)")
		fs.StringVar(&opt.name, "name", "Trill Demo", "paired computer name (Demo label is always included)")
		fs.StringVar(&opt.profile, "profile", "herdr", "fixture provider (herdr only)")
		fs.StringVar(&opt.preview, "preview-listen", "127.0.0.1:18080", "fixture preview bind address (IPv4 loopback only)")
		fs.StringVar(&opt.cert, "cert", "", "TLS certificate PEM (default private generated local certificate)")
		fs.StringVar(&opt.key, "key", "", "TLS key PEM")
		fs.StringVar(&opt.reviewPassword, "review-password-file", "", "private file containing reviewer password; enables /review (requires supplied TLS cert/key or --fly-edge-tls)")
		fs.BoolVar(&opt.flyEdgeTLS, "fly-edge-tls", false, "Fly-only deployment: HTTPS terminates at Fly Proxy; private HTTP listener on port 8080")
		fs.BoolVar(&opt.readOnly, "read-only", false, "exercise server-side read-only permissions")
		fs.IntVar(&opt.maxLeases, "max-leases", 2, "maximum concurrent owned terminal leases (1-8)")
		fs.DurationVar(&opt.retention, "retention", 24*time.Hour, "idle sandbox lifetime; expired data is reset on next start (minimum 1h)")
		if err := fs.Parse(args[1:]); err != nil {
			return flagError(err)
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected demo serve arguments")
		}
		return serve(opt, out)
	}
	var operand string
	rest := args[1:]
	if command == "fault" || command == "revoke" {
		if len(rest) == 0 {
			return errors.New("fault/revoke requires a name or device ID before flags")
		}
		operand = rest[0]
		rest = rest[1:]
	}
	asJSON := fs.Bool("json", false, "print structured JSON")
	asURL := fs.Bool("url", false, "print the private pairing URL")
	yes := fs.Bool("yes", false, "confirm reset of this stopped demo")
	if err := fs.Parse(rest); err != nil {
		return flagError(err)
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected demo arguments")
	}
	switch command {
	case "pair", "status", "fault", "revoke", "reset", "stop":
	default:
		return errors.New("unknown demo command; use trill demo --help")
	}
	root, err := openState(opt.root, false)
	if err != nil {
		return err
	}
	if command == "reset" {
		if !*yes {
			return errors.New("reset deletes this demo's data and pairings; pass --yes")
		}
		return resetState(root)
	}
	var result json.RawMessage
	method, path, body := "GET", "/status", any(nil)
	switch command {
	case "pair":
		method, path = "POST", "/pair"
	case "fault":
		method, path, body = "POST", "/fault", map[string]string{"name": operand}
	case "revoke":
		method, path, body = "POST", "/revoke", map[string]string{"id": operand}
	case "stop":
		method, path = "POST", "/stop"
	}
	err = callControl(root, method, path, body, &result)
	if err != nil {
		if command != "status" {
			return err
		}
		// A missing socket alone does not prove the server is stopped. Verify lock.
		lock, e := stateLock(root)
		if e != nil {
			return err
		}
		unlock(lock)
		var m manifest
		if e = readPrivate(filepath.Join(root, "runtime.json"), &m); e != nil {
			return err
		}
		m.Running = false
		m.PID = 0
		result, _ = json.Marshal(m)
	}
	if command == "pair" {
		if *asJSON {
			_, err = fmt.Fprintln(out, string(result))
			return err
		}
		if *asURL {
			_, err = fmt.Fprintln(out, setup.PairURL(result))
			return err
		}
		if err = setup.PrintQR(out, result); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Single-use demo invitation; expires in 10 minutes. Keep this code private.")
		return err
	}
	if *asJSON {
		_, err = fmt.Fprintln(out, string(result))
		return err
	}
	if command == "status" {
		var m manifest
		if err = json.Unmarshal(result, &m); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s · %s\n%s\nRunning: %t · PID: %d · Preview: localhost:%d\n", m.Name, m.Profile, m.Address, m.Running, m.PID, m.PreviewPort)
		return err
	}
	_, err = fmt.Fprintln(out, "Demo "+command+" complete.")
	return err
}
func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func validateOptions(o *options) error {
	if o.maxLeases < 1 || o.maxLeases > 8 {
		return errors.New("max-leases must be 1-8")
	}
	if o.retention < time.Hour {
		return errors.New("retention must be at least one hour")
	}
	host, port, err := net.SplitHostPort(o.listen)
	if err != nil {
		return err
	}
	if port == "7443" {
		return errors.New("demo must not use the installed companion's default port 7443")
	}
	if o.address == "" {
		if host == "" || host == "0.0.0.0" || host == "::" {
			return errors.New("wildcard listen requires an explicit --address")
		}
		o.address = "https://" + o.listen
	}
	u, err := url.Parse(o.address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("address must be an HTTPS origin")
	}
	o.address = strings.TrimSuffix(o.address, "/")
	ph, _, err := net.SplitHostPort(o.preview)
	if err != nil || ph != "127.0.0.1" {
		return errors.New("preview-listen must bind IPv4 loopback")
	}
	if o.flyEdgeTLS {
		if err := validateFlyOptions(o); err != nil {
			return err
		}
	}
	if o.profile != "herdr" {
		return errors.New("profile must be herdr")
	}
	if o.name == "" || len(o.name) > 150 || strings.ContainsAny(o.name, "\r\n\x00") {
		return errors.New("invalid demo name")
	}
	if !strings.Contains(o.name, "Demo") {
		o.name = "Demo · " + o.name
	}
	if o.reviewPassword != "" && !o.flyEdgeTLS && (o.cert == "" || o.key == "") {
		return errors.New("reviewer page requires an explicitly supplied TLS certificate and key")
	}
	return nil
}

func serve(o options, out io.Writer) error {
	if err := validateOptions(&o); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	root, err := openState(o.root, true)
	if err != nil {
		return err
	}
	lock, err := stateLock(root)
	if err != nil {
		return err
	}
	defer unlock(lock)
	if err = expireStored(root, o.retention); err != nil {
		return err
	}
	lastActivity, err := retainedActivity(root)
	if err != nil {
		return err
	}
	if err = writeAtomic(filepath.Join(root, "retention.json"), retentionState{lastActivity}); err != nil {
		return err
	}
	var cert tls.Certificate
	var pin string
	if !o.flyEdgeTLS {
		cert, pin, err = certificate(root, o.address, o.cert, o.key)
		if err != nil {
			return err
		}
	}
	publicLn, err := net.Listen("tcp", o.listen)
	if err != nil {
		return err
	}
	defer publicLn.Close()
	publicLn = limitListener(publicLn, 48)
	previewLn, err := net.Listen("tcp", o.preview)
	if err != nil {
		return err
	}
	defer previewLn.Close()
	previewLn = limitListener(previewLn, 24)
	controlLn, err := listenControl(root)
	if err != nil {
		return err
	}
	defer controlLn.Close()
	previewPort := previewLn.Addr().(*net.TCPAddr).Port
	service, demo, err := sidecar.NewDemoService(sidecar.Config{AllowControl: !o.readOnly, LeaseTTL: 30 * time.Minute}, sidecar.DemoOptions{Root: root, Profile: o.profile, Executable: exe, Socket: filepath.Join(root, "control.sock"), MaxLeases: o.maxLeases})
	if err != nil {
		return err
	}
	defer demo.Close()
	defer service.Close()
	store := pairing.NewStore(filepath.Join(root, "devices.json"))
	api, err := sidecar.NewDemoHTTP(service, demo, store, previewPort)
	if err != nil {
		return err
	}
	defer api.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	m := manifest{Kind: kind, Running: true, Address: o.address, Name: o.name, Profile: o.profile, Fingerprint: pin, PreviewPort: previewPort, PID: os.Getpid()}
	runtime := &runtimeState{root: root, manifest: m, demo: demo, store: store, cancel: cancel, faults: map[string]bool{}, lastActivity: lastActivity, retention: o.retention}
	if err = writeAtomic(filepath.Join(root, "runtime.json"), m); err != nil {
		return err
	}
	handler, err := runtime.public(api, o.reviewPassword)
	if err != nil {
		return err
	}
	if o.flyEdgeTLS {
		handler = flyTLSHandler(o.address, handler)
	}
	public := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, ErrorLog: quietLog()}
	preview := &http.Server{Handler: runtime.preview(demo.PreviewHandler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: quietLog()}
	control := &http.Server{Handler: runtime.control(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: quietLog()}
	for _, srv := range []*http.Server{public, preview, control} {
		srv.BaseContext = func(net.Listener) context.Context { return ctx }
	}
	servers := []*http.Server{public, preview, control}
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := f()
			if e != nil && !errors.Is(e, http.ErrServerClosed) {
				errs <- e
				cancel()
			}
		}()
	}
	if o.flyEdgeTLS {
		start(func() error { return public.Serve(publicLn) })
	} else {
		start(func() error { return public.Serve(tls.NewListener(publicLn, public.TLSConfig)) })
	}
	start(func() error { return preview.Serve(previewLn) })
	start(func() error { return control.Serve(controlLn) })
	go runtime.expireIdle(ctx)
	fmt.Fprintf(out, "Trill Demo (%s) is ready at %s\nPreview: localhost:%d · private state: %s\nPair with: trill demo pair --state-dir %s\n", o.profile, o.address, previewPort, root, root)
	<-ctx.Done()
	// Release terminal streams before waiting for IPC/HTTP clients so terminal polling ends.
	api.Close()
	service.Close()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
		_ = srv.Close()
	}
	wg.Wait()
	_ = runtime.saveRetention()
	_ = demo.Close()
	runtime.mu.Lock()
	expired := runtime.expired
	runtime.mu.Unlock()
	if expired {
		if err = clearData(root); err != nil {
			return fmt.Errorf("demo idle cleanup incomplete: %w", err)
		}
	}
	m.Running = false
	m.PID = 0
	_ = writeAtomic(filepath.Join(root, "runtime.json"), m)
	select {
	case err = <-errs:
		return err
	default:
		if expired {
			return errors.New("demo idle lifetime reached; owned data cleared")
		}
		return nil
	}
}
