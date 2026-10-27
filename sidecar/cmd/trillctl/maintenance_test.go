package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar/buildinfo"
	"trill/sidecar/setup"
)

func TestMaintenanceHelpAndVersionNeedNoConfiguration(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"doctor", "--help"}, {"update", "--help"}} {
		var output, errorOutput bytes.Buffer
		err := runWithHome(args, strings.NewReader(""), &output, &errorOutput, func() (string, error) { return t.TempDir(), nil })
		if err != nil || output.Len()+errorOutput.Len() == 0 {
			t.Fatalf("%v: %v", args, err)
		}
		if args[0] == "version" {
			var info buildinfo.Info
			if err := json.Unmarshal(output.Bytes(), &info); err != nil || info.Version == "" {
				t.Fatalf("%s %v", output.String(), err)
			}
		}
	}
}

func TestDoctorReportsMissingConfigurationWithoutCreatingState(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "missing-state")
	var output, errorOutput bytes.Buffer
	err := doctor([]string{"--json", "--state-dir", state}, &output, &errorOutput, home)
	if err == nil {
		t.Fatal("missing configuration passed doctor")
	}
	var findings []doctorFinding
	if err := json.Unmarshal(output.Bytes(), &findings); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		if finding.Check == "configuration" && finding.Status == "fail" && finding.Remedy != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing useful diagnostic: %s", output.String())
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatal("doctor modified the home directory")
	}
}

func TestRunningVersionUsesPinnedTLSWithoutDeviceCredentials(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/health" || request.Header.Get("Authorization") != "" {
			t.Error("diagnostics sent credentials or requested wrong endpoint")
		}
		response.Header().Set("X-Trill-Version", "0.1.0")
		response.Header().Set("X-Trill-Commit", strings.Repeat("a", 40))
		response.WriteHeader(http.StatusUnauthorized)
	}))
	bundle, _, err := setup.SelfSigned("192.168.1.2", "companion.local", "Test companion", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	digest := sha256.Sum256(server.Certificate().Raw)
	address, _ := url.Parse(server.URL)
	c := setup.Config{Address: "https://companion.local:" + address.Port(), BindIP: "127.0.0.1", Fingerprint: hex.EncodeToString(digest[:])}
	if err := probe(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	version, commit, err := runningCompanion(context.Background(), c)
	if err != nil || version != "0.1.0" || commit != strings.Repeat("a", 40) {
		t.Fatalf("%s %s %v", version, commit, err)
	}
	c.Fingerprint = strings.Repeat("0", 64)
	if _, _, err := runningCompanion(context.Background(), c); err == nil {
		t.Fatal("accepted wrong pinned identity")
	}
}

func TestUpdateFailsClosedBeforeChangingInstallation(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"extra"}, {"--install", "--check"}, {"--version", "../../bad"}, {"--install"}, {"--check"}} {
		if err := update(args, &bytes.Buffer{}, &bytes.Buffer{}, home); err == nil {
			t.Fatalf("accepted unconfigured/invalid update %v", args)
		}
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatal("unconfigured update modified installation")
	}
}
