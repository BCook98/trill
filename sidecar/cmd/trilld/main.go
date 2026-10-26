package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"trill/sidecar"
	"trill/sidecar/pairing"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	var listen, cert, key, tokenFile, pairingFile, tmuxSocket, labFilesRoot, moshHost, ports string
	var insecure, allowControl bool
	var backends, herdrPath, herdrSessions, herdrLabRoot, herdrConfigHome string
	var leaseTTL time.Duration
	flag.StringVar(&listen, "listen", "127.0.0.1:7443", "HTTP bind address")
	flag.StringVar(&cert, "cert", "", "TLS certificate PEM")
	flag.StringVar(&key, "key", "", "TLS private key PEM")
	flag.StringVar(&tokenFile, "token-file", "", "bearer token file (default ~/.config/trill/token)")
	flag.StringVar(&pairingFile, "pairing-store", "", "device state file (pairing only unless --token-file is also supplied)")
	flag.BoolVar(&insecure, "insecure-loopback", false, "allow HTTP only on loopback for development")
	flag.BoolVar(&allowControl, "allow-control", false, "enable session control and mosh bootstrap")
	flag.StringVar(&tmuxSocket, "tmux-socket", "", "absolute isolated tmux socket; enables terminal lab backend")
	flag.StringVar(&labFilesRoot, "lab-files-root", "", "opt in to file browsing in terminal lab: absolute existing directory; requires --tmux-socket")
	flag.StringVar(&moshHost, "mosh-host", "127.0.0.1", "mosh bind and advertised IP address")
	flag.StringVar(&ports, "udp-ports", "60000:60010", "mosh UDP port range")
	flag.DurationVar(&leaseTTL, "lease-ttl", 12*time.Hour, "maximum mosh lease lifetime")
	flag.StringVar(&backends, "backends", "", "session providers: xirp (default), herdr, or xirp,herdr")
	flag.StringVar(&herdrPath, "herdr-path", "", "Herdr executable (default herdr on PATH)")
	flag.StringVar(&herdrSessions, "herdr-sessions", "", "comma-separated existing Herdr servers (default default)")
	flag.StringVar(&herdrLabRoot, "herdr-lab-root", "", "disposable probe directory; requires exactly one Herdr backend")
	flag.StringVar(&herdrConfigHome, "herdr-config-home", "", "absolute XDG config base containing herdr/ (pins server identity)")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if err := validateNetwork(listen, cert, key, insecure, moshHost); err != nil {
		return err
	}
	svc, err := sidecar.NewService(sidecar.Config{Backends: backends, HerdrConfigHome: herdrConfigHome, HerdrPath: herdrPath, HerdrSessions: herdrSessions, HerdrLabRoot: herdrLabRoot, AllowControl: allowControl, TmuxSocket: tmuxSocket, LabFilesRoot: labFilesRoot, MoshHost: moshHost, UDPPorts: ports, LeaseTTL: leaseTTL}, nil)
	if err != nil {
		return err
	}
	defer svc.Close()
	// Validate service flags before reading or creating authentication state.
	token, store, err := loadAuthentication(tokenFile, pairingFile)
	if err != nil {
		return err
	}
	handler := sidecar.NewHTTPWithPairing(svc, token, store)
	defer handler.Close()
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	if cert != "" {
		reloader, err := newCertificateReloader(cert, key)
		if err != nil {
			return fmt.Errorf("invalid TLS certificate or key")
		}
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: reloader.GetCertificate}
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("trilld listening on %s (readOnly=%t, terminalLab=%t)", listen, !allowControl, tmuxSocket != "" || herdrLabRoot != "")
	if cert != "" {
		err = server.ServeTLS(ln, "", "")
	} else {
		err = server.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func validateNetwork(listen, cert, key string, insecure bool, moshHost string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen host must be an IP address")
	}
	if insecure && !ip.IsLoopback() {
		return fmt.Errorf("insecure HTTP requires a loopback listen address")
	}
	if (cert == "") != (key == "") {
		return fmt.Errorf("both --cert and --key are required")
	}
	if cert == "" && !insecure {
		return fmt.Errorf("TLS certificate and key required unless --insecure-loopback is set")
	}
	moshIP := net.ParseIP(moshHost)
	if moshIP == nil || moshIP.IsUnspecified() {
		return fmt.Errorf("mosh host must be a concrete IP address")
	}
	if !ip.IsLoopback() && moshIP.IsLoopback() {
		return fmt.Errorf("non-loopback listener requires an explicit reachable --mosh-host")
	}
	return nil
}

func loadToken(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("token file path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("cannot prepare token directory")
	}
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot generate token")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		v := hex.EncodeToString(b)
		if _, err = f.WriteString(v); err != nil {
			f.Close()
			os.Remove(path)
			return "", fmt.Errorf("cannot write token")
		}
		if err = f.Close(); err != nil {
			return "", fmt.Errorf("cannot close token file")
		}
		return v, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cannot create token file")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", fmt.Errorf("token file must be a regular 0600 file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("cannot open token file")
	}
	f = os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", fmt.Errorf("token file must be a regular 0600 file")
	}
	if info.Size() != 96 {
		return "", fmt.Errorf("token file must contain a 48-byte hex token")
	}
	data := make([]byte, 96)
	if _, err = io.ReadFull(f, data); err != nil {
		return "", fmt.Errorf("cannot read token file")
	}
	if _, err = hex.DecodeString(string(data)); err != nil {
		return "", fmt.Errorf("invalid token file")
	}
	return string(data), nil
}

// Explicit pairing mode must never create or consult the legacy master token
// unless its file was also explicitly requested.
func loadAuthentication(tokenFile, pairingFile string) (string, *pairing.Store, error) {
	var store *pairing.Store
	if pairingFile != "" {
		store = pairing.NewStore(pairingFile)
		if _, err := store.Devices(); err != nil {
			return "", nil, fmt.Errorf("invalid pairing store")
		}
		if tokenFile == "" {
			return "", store, nil
		}
	}
	if tokenFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, fmt.Errorf("home directory unavailable")
		}
		tokenFile = filepath.Join(home, ".config", "trill", "token")
	}
	token, err := loadToken(tokenFile)
	return token, store, err
}
