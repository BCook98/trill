package demoapp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trill/sidecar"
	"trill/sidecar/pairing"
	"trill/sidecar/setup"
)

func reviewRuntime(t *testing.T) *runtimeState {
	t.Helper()
	root := ownedTestRoot(t)
	return &runtimeState{root: root, store: pairing.NewStore(filepath.Join(root, "devices.json")), manifest: manifest{Address: "https://demo.example", Name: "Trill Demo"}, faults: map[string]bool{}, retention: 24 * time.Hour}
}
func reviewRequest(handler http.Handler, method, origin, password string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://demo.example/review", nil)
	r.TLS = &tls.ConnectionState{}
	if password != "" {
		r.SetBasicAuth("reviewer", password)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestReviewerAuthenticationAndOrigin(t *testing.T) {
	s := reviewRuntime(t)
	password := strings.Repeat("a", 32)
	handler := s.reviewer(password)
	for _, tc := range []struct {
		method, origin, password string
		status                   int
	}{{"GET", "", "", 401}, {"POST", "https://evil.example", password, 403}, {"POST", "", password, 403}, {"POST", "null", password, 403}, {"GET", "", password, 200}, {"POST", "https://demo.example", password, 200}} {
		w := reviewRequest(handler, tc.method, tc.origin, tc.password)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.origin, w.Code)
		}
		if tc.status == 200 && tc.method == "POST" {
			if !strings.Contains(w.Body.String(), "trill://pair?data=") || !strings.Contains(w.Body.String(), "data:image/png;base64,") {
				t.Fatal("missing real pairing outputs")
			}
		}
		if tc.status != 200 && strings.Contains(w.Body.String(), "trill://") {
			t.Fatal("leaked invitation")
		}
	}
	if w := reviewRequest(handler, "GET", "", password); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("review cache policy missing")
	}
}
func TestInvitationUsesRealStoreAndExpiry(t *testing.T) {
	s := reviewRuntime(t)
	b, err := s.invitation()
	if err != nil {
		t.Fatal(err)
	}
	var p setup.Payload
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.store.Redeem(p.Code, "Demo phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.Authenticate(token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.store.Redeem(p.Code, "Again"); err == nil {
		t.Fatal("reused invitation")
	}
	s.faults["invitation-expired"] = true
	b, err = s.invitation()
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &p)
	if _, _, err = s.store.Redeem(p.Code, "Late"); err == nil {
		t.Fatal("expired invitation accepted")
	}
}
func TestPublicRouterDoesNotExposeControl(t *testing.T) {
	s := reviewRuntime(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service, demo, err := sidecar.NewDemoService(sidecar.Config{AllowControl: true}, sidecar.DemoOptions{Root: s.root, Profile: "mixed", Executable: exe, Socket: filepath.Join(s.root, "control.sock"), BindHost: "127.0.0.1", MaxLeases: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Close()
	api, err := sidecar.NewDemoHTTP(service, demo, s.store, 18080)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	s.demo = demo
	handler, err := s.public(api, "")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.store.CreateInvitation(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.store.Redeem(code, "Test phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/status", "/terminal?session=demo", "/pair", "/fault", "/resize", "/stop", "/revoke"} {
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, "https://demo.example"+path, strings.NewReader(`{"name":"capacity"}`))
			r.TLS = &tls.ConnectionState{}
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("private route exposed: %s %s %d", method, path, w.Code)
			}
		}
	}
	sessions, err := service.Sessions(context.Background())
	if err != nil || len(sessions) == 0 {
		t.Fatal("no fixture", err)
	}
	for _, port := range []int{7443, 18081, 19443} {
		body := fmt.Sprintf(`{"sessionId":%q,"port":%d}`, sessions[0].ID, port)
		r := httptest.NewRequest("POST", "https://demo.example/v1/previews", strings.NewReader(body))
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("preview to non-fixture port %d accepted: %d", port, w.Code)
		}
	}
}

func TestReviewerPasswordFileIsPrivate(t *testing.T) {
	s := reviewRuntime(t)
	path := filepath.Join(s.root, "review-password")
	os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0644)
	if _, err := s.public(http.NotFoundHandler(), path); err == nil {
		t.Fatal("accepted world-readable password")
	}
	os.Chmod(path, 0600)
	if _, err := s.public(http.NotFoundHandler(), path); err != nil {
		t.Fatal(err)
	}
}
func TestLimiterBoundedBurst(t *testing.T) {
	l := &limiter{rate: 0, burst: 2}
	if !l.allow() || !l.allow() || l.allow() {
		t.Fatal("burst limit not enforced")
	}
}

func TestPrivateStatusExposesRuntimeFaultsForSmokePreflight(t *testing.T) {
	s := reviewRuntime(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, demo, err := sidecar.NewDemoService(sidecar.Config{AllowControl: true}, sidecar.DemoOptions{Root: s.root, Profile: "mixed", Executable: exe, Socket: filepath.Join(s.root, "control.sock"), BindHost: "127.0.0.1", MaxLeases: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Close()
	s.demo = demo
	if err = s.setFault("api-unavailable"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.control().ServeHTTP(w, httptest.NewRequest("GET", "http://unix/status", nil))
	var m manifest
	if err = json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.RuntimeFaults) != 1 || m.RuntimeFaults[0] != "api-unavailable" {
		t.Fatal("runtime fault invisible", m.RuntimeFaults)
	}
	if err = s.setFault("clear"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.control().ServeHTTP(w, httptest.NewRequest("GET", "http://unix/status", nil))
	if err = json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.RuntimeFaults == nil || len(m.RuntimeFaults) != 0 {
		t.Fatal("clear not observable")
	}
}

func TestPublicReviewerPreservesBrowserFormOrigin(t *testing.T) {
	s := reviewRuntime(t)
	password := strings.Repeat("x", 32)
	path := filepath.Join(s.root, "review-password")
	if err := os.WriteFile(path, []byte(password), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := s.public(http.NotFoundHandler(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		w := reviewRequest(handler, method, s.manifest.Address, password)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", method, w.Code)
		}
		if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
			t.Fatalf("%s: browser form would lose origin under policy %q", method, got)
		}
	}
	r := httptest.NewRequest("GET", "https://demo.example/unrelated", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("non-review route policy changed: %q", got)
	}
}
