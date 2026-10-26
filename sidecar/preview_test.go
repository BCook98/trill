package sidecar

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const previewTestOrigin = "https://companion.test:7443"

func previewRequest(h http.Handler, method, path, bearer, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", previewTestOrigin+path, strings.NewReader(body))
	r.Method = method
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: previewCookie, Value: token})
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func previewPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func previewCreate(t *testing.T, h *HTTP, bearer, session string, port int) previewCredentials {
	t.Helper()
	w := previewRequest(h, "POST", "/v1/previews", bearer, "", fmt.Sprintf(`{"sessionId":%q,"port":%d}`, session, port))
	var result previewCredentials
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return result
}

func previewGrantFor(t *testing.T, h *HTTP, id string) *previewGrant {
	t.Helper()
	h.previews.mu.Lock()
	defer h.previews.mu.Unlock()
	g := h.previews.grants[id]
	if g == nil {
		t.Fatal("missing grant")
	}
	return g
}

func TestPreviewCreateValidationAndScope(t *testing.T) {
	h, f, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "dev") }))
	defer upstream.Close()
	port := previewPort(t, upstream)
	good := fmt.Sprintf(`{"sessionId":"s1","port":%d}`, port)
	for _, tc := range []struct {
		name, bearer, body string
		status             int
	}{
		{"missing auth", "", good, 401}, {"bad auth", "wrong", good, 401},
		{"missing fields", "secret", `{}`, 400}, {"low port", "secret", `{"sessionId":"s1","port":1023}`, 400},
		{"high port", "secret", `{"sessionId":"s1","port":65536}`, 400}, {"negative", "secret", `{"sessionId":"s1","port":-1}`, 400},
		{"float", "secret", `{"sessionId":"s1","port":3000.5}`, 400}, {"string port", "secret", `{"sessionId":"s1","port":"3000"}`, 400},
		{"url", "secret", `{"sessionId":"s1","port":3000,"url":"http://evil.test"}`, 400},
		{"trailing json", "secret", good + ` {}`, 400}, {"unknown session", "secret", `{"sessionId":"missing","port":3000}`, 404},
		{"bad id", "secret", `{"sessionId":"../s1","port":3000}`, 400}, {"ended", "secret", `{"sessionId":"s2","port":3000}`, 409},
		{"self", "secret", `{"sessionId":"s1","port":7443}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := previewRequest(h, "POST", "/v1/previews", tc.bearer, "", tc.body)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, field := range []string{"hidden", "not-live", "unknown-status"} {
		f.mu.Lock()
		original := f.sessions[0]
		switch field {
		case "hidden":
			f.sessions[0].HiddenFromSidebar = true
		case "not-live":
			f.sessions[0].TerminalLive = false
		default:
			f.sessions[0].Status = "unknown"
		}
		f.mu.Unlock()
		if w := previewRequest(h, "POST", "/v1/previews", "secret", "", good); w.Code != 409 {
			t.Fatal(field, w.Code)
		}
		f.mu.Lock()
		f.sessions[0] = original
		f.mu.Unlock()
	}
	before := time.Now()
	grant := previewCreate(t, h, "secret", "s1", port)
	expires, err := time.Parse(time.RFC3339, grant.ExpiresAt)
	if err != nil || !strings.HasSuffix(grant.ExpiresAt, "Z") || expires.Before(before.Add(previewTTL-time.Second)) || expires.After(time.Now().Add(previewTTL)) {
		t.Fatal("TTL", grant.ExpiresAt, err)
	}
	if len(grant.Token) != 64 || len(grant.ID) != 48 || grant.Token == "secret" {
		t.Fatal("invalid capability")
	}
	g := previewGrantFor(t, h, grant.ID)
	if g.hash != sha256.Sum256([]byte(grant.Token)) || g.owner != previewLegacyOwner {
		t.Fatal("hash or legacy scope")
	}
	if w := previewRequest(h, "GET", "/v1/sessions", grant.Token, "", ""); w.Code != 401 {
		t.Fatal("preview token authenticated API")
	}
	if w := previewRequest(h, "GET", "/", "", grant.Token, ""); w.Code != 200 || w.Body.String() != "dev" {
		t.Fatal(w.Code, w.Body.String())
	}
	ro, _, _ := setup(t, false, false)
	t.Cleanup(ro.Close)
	if w := previewRequest(ro, "POST", "/v1/previews", "secret", "", good); w.Code != 403 {
		t.Fatal("control", w.Code)
	}
}

func TestPreviewHostAndTLSValidation(t *testing.T) {
	h, _, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	for _, host := range []string{"", "https://companion.test:7443", "companion.test:", "companion.test:bad", "companion.test:0", "companion.test:65536", "companion.test:07443", "user@host:7443", "[::1", "host/path", "host\\evil", "host#frag", "host?query", "bad..host", "-bad:7443", "bad_:7443", "[host]:7443"} {
		t.Run(host, func(t *testing.T) {
			r := httptest.NewRequest("POST", previewTestOrigin+"/v1/previews", strings.NewReader(`{"sessionId":"s1","port":3000}`))
			r.Host = host
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("host %q accepted: %d", host, w.Code)
			}
		})
	}
	r := httptest.NewRequest("POST", previewTestOrigin+"/v1/previews", strings.NewReader(`{"sessionId":"s1","port":3000}`))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3000}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("forged Host bypassed actual listener port", w.Code)
	}
	for _, tc := range []struct {
		local, remote string
		want          bool
	}{
		{"127.0.0.1", "127.0.0.1:40000", true}, {"::1", "[::1]:40000", true},
		{"0.0.0.0", "127.0.0.1:40000", false}, {"127.0.0.1", "192.0.2.1:40000", false},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:7443/", nil)
		r.RemoteAddr = tc.remote
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(tc.local), Port: 7443}))
		if got := previewSecure(r); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	r = httptest.NewRequest("POST", "http://companion.test:7443/v1/previews", strings.NewReader(`{"sessionId":"s1","port":3000}`))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("insecure request without listener proof", w.Code)
	}
}

func TestPreviewPathsAssetsHeadersAndDispatch(t *testing.T) {
	h, _, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	var seen atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("X-Trill-Demo-Session") != "client-owned-value" {
			t.Error("ordinary proxy altered an application header")
		}
		if r.Host != r.URL.Host && r.URL.Host != "" {
			t.Error("bad host")
		}
		for name := range r.Header {
			if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") || strings.EqualFold(name, "Forwarded") || strings.HasPrefix(strings.ToLower(name), "x-forwarded") || strings.EqualFold(name, "X-Real-IP") || strings.EqualFold(name, "X-Original-URL") {
				t.Error("leaked header", name)
			}
		}
		if strings.Contains(r.Header.Get("Cookie"), previewCookie) || strings.Contains(r.Header.Get("Cookie"), "secret") {
			t.Error("leaked credential cookie")
		}
		if r.Header.Get("Cookie") != "app=ok" {
			t.Error("lost app cookie", r.Header.Get("Cookie"))
		}
		if r.Header.Get("Origin") != "http://"+r.Host {
			t.Error("HMR Origin", r.Header.Get("Origin"))
		}
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Add("Set-Cookie", previewCookie+"=overwrite; HttpOnly; Secure; Path=/")
		w.Header().Add("Set-Cookie", "app=next; Path=/")
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		_ = json.NewEncoder(w).Encode(map[string]string{"uri": r.RequestURI, "host": r.Host, "method": r.Method})
	}))
	defer upstream.Close()
	port := previewPort(t, upstream)
	grant := previewCreate(t, h, "secret", "s1", port)
	for _, path := range []string{"/", "/assets/index.js?v=1&v=2", "/nested/relative.css", "/v1/sessions?raw=a%2Fb&bare&bad=%zz", "/v1/pair", "/a%2Fb?x=hello+world", "/@vite/client"} {
		r := httptest.NewRequest("GET", previewTestOrigin+path, nil)
		r.Header.Set("Cookie", previewCookie+"="+grant.Token+"; app=ok")
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Proxy-Authorization", "Bearer secret")
		for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Forwarded-Prefix", "X-Real-IP", "X-Original-URL"} {
			r.Header.Set(name, "forged")
		}
		r.Header.Set("Origin", previewTestOrigin)
		r.Header.Set("X-Trill-Demo-Session", "client-owned-value")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var result map[string]string
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["uri"] != path || result["host"] != "127.0.0.1:"+strconv.Itoa(port) {
			t.Fatalf("path %s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Type") != "text/javascript" || w.Header().Get("Clear-Site-Data") != "" {
			t.Fatal("response headers", w.Header())
		}
		cookies := w.Header().Values("Set-Cookie")
		if len(cookies) != 1 || cookies[0] != "app=next; Path=/" {
			t.Fatal("cookie overwrite", cookies)
		}
	}
	before := seen.Load()
	for _, cookie := range []string{previewCookie + "=wrong", previewCookie, previewCookie + "=", previewCookie + "=\"" + grant.Token + "\"", previewCookie + "=" + grant.Token + "; " + previewCookie + "=" + grant.Token} {
		r := httptest.NewRequest("GET", previewTestOrigin+"/v1/health", nil)
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("invalid cookie fell through", cookie, w.Code)
		}
	}
	if w := previewRequest(h, "GET", "/v1/health", "secret", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "trilld") {
		t.Fatal("normal API unavailable")
	}
	if w := previewRequest(h, "GET", "/v1/health", "", "", ""); w.Code != 401 {
		t.Fatal("cookie-less API bypass")
	}
	for _, method := range []string{"CONNECT", "TRACE", "BREW"} {
		if w := previewRequest(h, method, "/", "", grant.Token, ""); w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	for _, proto := range []string{"h2c", "other", "websocket, h2c"} {
		r := httptest.NewRequest("GET", previewTestOrigin+"/", nil)
		r.Header.Set("Cookie", previewCookie+"="+grant.Token)
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", proto)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("upgrade", proto, w.Code)
		}
	}
	for _, origin := range []string{"https://other.test:7443", "https://companion.test:7444", "http://companion.test:7443"} {
		r := httptest.NewRequest("GET", origin+"/", nil)
		r.Header.Set("Cookie", previewCookie+"="+grant.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("grant escaped companion origin", origin, w.Code)
		}
	}
	if seen.Load() != before {
		t.Fatal("invalid requests reached upstream")
	}
}

func TestPreviewRedirectConfinement(t *testing.T) {
	h, _, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	var location atomic.Value
	location.Store("")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", location.Load().(string))
		w.WriteHeader(302)
	}))
	defer upstream.Close()
	grant := previewCreate(t, h, "secret", "s1", previewPort(t, upstream))
	for _, tc := range []struct {
		location, expected string
		status             int
	}{
		{"/next?q=1", "/next?q=1", 302}, {"relative.js", "relative.js", 302},
		{upstream.URL + "/next?q=1", previewTestOrigin + "/next?q=1", 302},
		{previewTestOrigin + "/next", previewTestOrigin + "/next", 302},
		{"http://127.0.0.1:1/", "", 502}, {"https://external.invalid/", "", 502}, {"//external.invalid/", "", 502},
		{"///external.invalid/", "", 502}, {"/\\external.invalid", "", 502}, {"javascript:alert(1)", "", 502},
		{"http://user:pass@companion.test:7443/", "", 502}, {"https:///external.invalid/", "", 502},
	} {
		location.Store(tc.location)
		w := previewRequest(h, "GET", "/redirect", "", grant.Token, "")
		if w.Code != tc.status || w.Header().Get("Location") != tc.expected {
			t.Fatal(tc.location, w.Code, w.Header())
		}
	}
	if calls.Load() != 12 {
		t.Fatal("proxy followed a redirect", calls.Load())
	}
}

func TestPreviewOwnerDeletionRevocationAndCapacity(t *testing.T) {
	h, _, _, store, _ := pairedHTTP(t, true, "legacy")
	id1, token1 := pairPhone(t, h, store, "One")
	_, token2 := pairPhone(t, h, store, "Two")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "dev") }))
	defer upstream.Close()
	port := previewPort(t, upstream)
	grant := previewCreate(t, h, token1, "lab-1", port)
	for _, other := range []string{token2, "legacy"} {
		if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, other, "", ""); w.Code != 404 {
			t.Fatal("non-owner delete", w.Code)
		}
	}
	if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, token1, "", ""); w.Code != 200 {
		t.Fatal("owner delete", w.Code)
	}
	if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, token1, "", ""); w.Code != 404 {
		t.Fatal("idempotent delete", w.Code)
	}
	if w := previewRequest(h, "GET", "/v1/health", "legacy", grant.Token, ""); w.Code != 401 {
		t.Fatal("deleted token usable")
	}
	grant = previewCreate(t, h, token1, "lab-1", port)
	g := previewGrantFor(t, h, grant.ID)
	if err := store.Revoke(id1); err != nil {
		t.Fatal(err)
	}
	if w := previewRequest(h, "GET", "/", "", grant.Token, ""); w.Code != 401 {
		t.Fatal("revoked cookie usable")
	}
	eventually(t, func() bool { return g.ctx.Err() != nil })
	legacy := previewCreate(t, h, "legacy", "lab-1", port)
	if w := previewRequest(h, "GET", "/", "", legacy.Token, ""); w.Code != 200 {
		t.Fatal("paired revocation affected legacy")
	}
	for i := 0; i < previewPerDevice; i++ {
		previewCreate(t, h, token2, "lab-1", port)
	}
	if w := previewRequest(h, "POST", "/v1/previews", token2, "", fmt.Sprintf(`{"sessionId":"lab-1","port":%d}`, port)); w.Code != 429 {
		t.Fatal("per-device bound", w.Code)
	}
	// Expired entries free capacity immediately; the sweep also cancels them.
	h.previews.mu.Lock()
	for _, g := range h.previews.grants {
		if g.owner != previewLegacyOwner {
			g.expires = time.Now().Add(-time.Second)
		}
	}
	h.previews.mu.Unlock()
	previewCreate(t, h, token2, "lab-1", port)
	for i := 0; i < 3; i++ {
		_, token := pairPhone(t, h, store, fmt.Sprintf("Extra%d", i))
		for j := 0; j < previewPerDevice; j++ {
			previewCreate(t, h, token, "lab-1", port)
		}
	}
	for i := 0; i < 6; i++ {
		previewCreate(t, h, "legacy", "lab-1", port)
	}
	if w := previewRequest(h, "POST", "/v1/previews", "legacy", "", fmt.Sprintf(`{"sessionId":"lab-1","port":%d}`, port)); w.Code != 429 {
		t.Fatal("global bound", w.Code)
	}
}

// Block only the read used by find, without touching real sessions or services.
type previewBlockingRunner struct {
	*fakeRunner
	entered, release chan struct{}
	once             atomic.Bool
}

func (f *previewBlockingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "xirp" && len(args) > 1 && args[0] == "session" && f.once.CompareAndSwap(false, true) {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.fakeRunner.Run(ctx, name, args...)
}
func TestPreviewRevocationDuringGrantCreation(t *testing.T) {
	h, f, svc, store, _ := pairedHTTP(t, true, "")
	id, token := pairPhone(t, h, store, "One")
	// Switch this fixture to its fake xirp backend so find can be paused.
	svc.config.TmuxSocket = ""
	blocked := &previewBlockingRunner{fakeRunner: f, entered: make(chan struct{}), release: make(chan struct{})}
	svc.runner = blocked
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- previewRequest(h, "POST", "/v1/previews", token, "", `{"sessionId":"s1","port":3000}`)
	}()
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("find did not begin")
	}
	if err := store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	close(blocked.release)
	select {
	case w := <-result:
		if w.Code != 401 {
			t.Fatal("published revoked grant", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("create stuck")
	}
}

func TestPreviewSSEExpiryAndShutdown(t *testing.T) {
	for _, action := range []string{"expiry", "http-close", "service-close", "delete", "revoke", "store-error"} {
		t.Run(action, func(t *testing.T) {
			h, _, svc, store, path := pairedHTTP(t, true, "")
			id, token := pairPhone(t, h, store, "One")
			closed := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: ready\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-time.After(200 * time.Millisecond):
					_, _ = io.WriteString(w, "data: alive\n\n")
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
				}
				<-r.Context().Done()
				close(closed)
			}))
			defer upstream.Close()
			frontend := httptest.NewUnstartedServer(h)
			frontend.Config.WriteTimeout = 150 * time.Millisecond
			frontend.Start()
			defer frontend.Close()
			defer h.Close()
			grant := previewRealCreate(t, frontend, token, previewPort(t, upstream))
			g := previewGrantFor(t, h, grant.ID)
			req, _ := http.NewRequest("GET", frontend.URL+"/events", nil)
			req.AddCookie(&http.Cookie{Name: previewCookie, Value: grant.Token})
			resp, err := frontend.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			if line, err := reader.ReadString('\n'); err != nil || line != "data: ready\n" {
				t.Fatal("SSE not flushed", line, err)
			}
			// Read a heartbeat written after the frontend's normal deadline.
			_, _ = reader.ReadString('\n')
			if line, err := reader.ReadString('\n'); err != nil || line != "data: alive\n" {
				t.Fatal("SSE hit frontend timeout", line, err)
			}
			select {
			case <-closed:
				t.Fatal("SSE closed by frontend timeout")
			default:
			}
			switch action {
			case "expiry":
				h.previews.mu.Lock()
				g.expires = time.Now().Add(-time.Second)
				h.previews.mu.Unlock()
			case "http-close":
				h.Close()
			case "service-close":
				svc.Close()
			case "delete":
				if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, token, "", ""); w.Code != 200 {
					t.Fatal(w.Code)
				}
			case "revoke":
				if err := store.Revoke(id); err != nil {
					t.Fatal(err)
				}
			case "store-error":
				if err := corruptPreviewStore(path); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream stream was not canceled")
			}
			finished := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, reader); close(finished) }()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("frontend stream was not closed")
			}
			eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.connections) == 0 })
		})
	}
}

func previewRealCreate(t *testing.T, frontend *httptest.Server, bearer string, port int) previewCredentials {
	t.Helper()
	req, _ := http.NewRequest("POST", frontend.URL+"/v1/previews", strings.NewReader(fmt.Sprintf(`{"sessionId":"lab-1","port":%d}`, port)))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := frontend.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var grant previewCredentials
	if resp.StatusCode != 201 || json.NewDecoder(resp.Body).Decode(&grant) != nil {
		t.Fatal("real create", resp.StatusCode)
	}
	return grant
}

func TestPreviewWebSocketCleanup(t *testing.T) {
	for _, action := range []string{"revoke", "expiry", "close", "delete"} {
		t.Run(action, func(t *testing.T) {
			h, _, _, store, _ := pairedHTTP(t, true, "")
			id, token := pairPhone(t, h, store, "One")
			closed := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || strings.Contains(r.Header.Get("Cookie"), previewCookie) {
					t.Error("WebSocket leaked credentials")
				}
				if r.URL.Path != "/hmr" || r.URL.RawQuery != "v=1" || r.Header.Get("Origin") != "http://"+r.Host {
					t.Error("WebSocket request", r.URL, r.Header)
				}
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				defer close(closed)
				_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSet-Cookie: trill_preview=replace; Path=/\r\n\r\n")
				_ = rw.Flush()
				// An unmasked server text frame is valid RFC 6455 wire data.
				_, _ = conn.Write([]byte{0x81, 2, 'o', 'k'})
				_, _ = io.Copy(io.Discard, rw)
			}))
			defer upstream.Close()
			frontend := httptest.NewServer(h)
			defer frontend.Close()
			defer h.Close()
			grant := previewRealCreate(t, frontend, token, previewPort(t, upstream))
			g := previewGrantFor(t, h, grant.ID)
			conn, err := net.DialTimeout("tcp", frontend.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = fmt.Fprintf(conn, "GET /hmr?v=1 HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: trill_preview=%s\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", frontend.Listener.Addr(), frontend.URL, grant.Token, token)
			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
			if err != nil || resp.StatusCode != 101 {
				t.Fatal("upgrade", resp, err)
			}
			if resp.Header.Get("Set-Cookie") != "" {
				t.Fatal("reserved cookie in upgrade")
			}
			frame := make([]byte, 4)
			if _, err := io.ReadFull(reader, frame); err != nil || string(frame) != string([]byte{0x81, 2, 'o', 'k'}) {
				t.Fatal("frame", frame, err)
			}
			switch action {
			case "revoke":
				if err := store.Revoke(id); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				h.previews.mu.Lock()
				g.expires = time.Now().Add(-time.Second)
				h.previews.mu.Unlock()
			case "close":
				h.Close()
			case "delete":
				if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, token, "", ""); w.Code != 200 {
					t.Fatal(w.Code)
				}
			}
			if _, err := reader.ReadByte(); err == nil {
				t.Fatal("upgraded frontend remained open")
			} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("upgraded frontend timed out instead of closing")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("upgraded upstream remained open")
			}
			eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.connections) == 0 })
		})
	}
}

func corruptPreviewStore(path string) error { return os.WriteFile(path, []byte("invalid"), 0600) }

func TestPreviewResponseHeaderBoundaries(t *testing.T) {
	h, _, _, store, _ := pairedHTTP(t, true, "")
	_, bearer := pairPhone(t, h, store, "One")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "trill_preview=early; Path=/")
		w.Header().Set("Location", "http://external.invalid/")
		w.Header().Set("Alt-Svc", `h2=":3000"`)
		w.WriteHeader(103)
		clear(w.Header())
		w.Header().Set("Trailer", "Set-Cookie, Location")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "body")
		w.Header().Set("Set-Cookie", "trill_preview=late; Path=/")
		w.Header().Set("Location", "http://external.invalid/")
	}))
	defer upstream.Close()
	frontend := httptest.NewServer(h)
	defer frontend.Close()
	grant := previewRealCreate(t, frontend, bearer, previewPort(t, upstream))
	req, _ := http.NewRequest("GET", frontend.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: previewCookie, Value: grant.Token})
	var early atomic.Int32
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
		early.Add(1)
		if header.Get("Set-Cookie") != "" || header.Get("Location") != "" || header.Get("Alt-Svc") != "" {
			t.Error("unsafe informational response", header)
		}
		return nil
	}}))
	resp, err := frontend.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "body" || early.Load() != 1 {
		t.Fatal("response", string(body), early.Load(), err)
	}
	if resp.Trailer.Get("Set-Cookie") != "" || resp.Trailer.Get("Location") != "" {
		t.Fatal("unsafe trailer", resp.Trailer)
	}
}

func TestPreviewCancellationInterruptsUpload(t *testing.T) {
	h, _, _, store, _ := pairedHTTP(t, true, "")
	id, bearer := pairPhone(t, h, store, "One")
	started, finished := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.Copy(io.Discard, r.Body)
		close(finished)
	}))
	defer upstream.Close()
	frontend := httptest.NewServer(h)
	defer frontend.Close()
	defer h.Close()
	grant := previewRealCreate(t, frontend, bearer, previewPort(t, upstream))
	conn, err := net.DialTimeout("tcp", frontend.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: %s\r\nCookie: trill_preview=%s\r\nContent-Length: 100000\r\n\r\nx", frontend.Listener.Addr(), grant.Token)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upload not forwarded")
	}
	if err := store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled upload not interrupted")
	}
	_, err = bufio.NewReader(conn).ReadByte()
	if err == nil {
		t.Fatal("frontend still readable after grant cancellation")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("frontend upload connection not closed")
	}
}

// Only isolated test listeners are used. Track hijacked sockets ourselves since
// httptest.Server.Close does not own them, including on a regression failure.
func previewUpgradeFixture(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var active, accepted atomic.Int32
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		connections[conn] = struct{}{}
		active.Add(1)
		accepted.Add(1)
		mu.Unlock()
		defer func() {
			_ = conn.Close()
			mu.Lock()
			delete(connections, conn)
			active.Add(-1)
			mu.Unlock()
		}()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(io.Discard, rw)
	}))
	t.Cleanup(func() {
		upstream.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
	})
	return upstream, &active, &accepted
}

func previewUpgradeRequest(t *testing.T, frontend *httptest.Server, token, method, connection, upgrade string) (net.Conn, *http.Response) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", frontend.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var headers strings.Builder
	if connection != "" {
		fmt.Fprintf(&headers, "Connection: %s\r\n", connection)
	}
	if upgrade != "" {
		fmt.Fprintf(&headers, "Upgrade: %s\r\n", upgrade)
	}
	_, err = fmt.Fprintf(conn, "%s /hmr HTTP/1.1\r\nHost: %s\r\nCookie: trill_preview=%s\r\n%s\r\n", method, frontend.Listener.Addr(), token, headers.String())
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	return conn, res
}

func TestPreviewBoundsUpgradedConnections(t *testing.T) {
	h, _, _ := setup(t, true, true)
	upstream, active, accepted := previewUpgradeFixture(t)
	frontend := httptest.NewServer(h)
	t.Cleanup(frontend.Close)
	t.Cleanup(h.Close)
	grant := previewRealCreate(t, frontend, "secret", previewPort(t, upstream))
	g := previewGrantFor(t, h, grant.ID)
	var clients []net.Conn
	for i := 0; i < 32; i++ {
		conn, res := previewUpgradeRequest(t, frontend, grant.Token, "GET", "Upgrade", "websocket")
		if res.StatusCode != 101 {
			t.Fatalf("upgrade %d: %d", i, res.StatusCode)
		}
		clients = append(clients, conn)
	}
	for i := 0; i < 4; i++ {
		conn, res := previewUpgradeRequest(t, frontend, grant.Token, "GET", "Upgrade", "websocket")
		_ = conn.Close()
		if res.StatusCode != 502 {
			t.Fatalf("upgrade beyond cap accepted: %d", res.StatusCode)
		}
	}
	if active.Load() != 32 || accepted.Load() != 32 {
		t.Fatal("connection cap did not prevent upstream dials", active.Load(), accepted.Load())
	}
	_ = clients[0].Close()
	eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.connections) == 31 && active.Load() == 31 })
	_, res := previewUpgradeRequest(t, frontend, grant.Token, "GET", "Upgrade", "websocket")
	if res.StatusCode != 101 {
		t.Fatal("closing upgrade did not return capacity", res.StatusCode)
	}
	h.Close()
	eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.connections) == 0 && active.Load() == 0 })
}

func TestPreviewRejectsUnsolicitedUpgradeWithoutOrphanSockets(t *testing.T) {
	for _, tc := range []struct{ name, method, connection, upgrade string }{
		{"ordinary GET", "GET", "", ""},
		{"missing Connection token", "GET", "keep-alive", "websocket"},
		{"wrong method", "POST", "Upgrade", "websocket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setup(t, true, true)
			upstream, active, accepted := previewUpgradeFixture(t)
			frontend := httptest.NewServer(h)
			t.Cleanup(frontend.Close)
			t.Cleanup(h.Close)
			grant := previewRealCreate(t, frontend, "secret", previewPort(t, upstream))
			g := previewGrantFor(t, h, grant.ID)
			// More requests than the connection cap prove failed handoffs return
			// ownership immediately, rather than accumulating until grant expiry.
			for i := 0; i < 36; i++ {
				conn, res := previewUpgradeRequest(t, frontend, grant.Token, tc.method, tc.connection, tc.upgrade)
				_ = conn.Close()
				if res.StatusCode != 502 {
					t.Fatal("unsolicited upgrade accepted", res.StatusCode)
				}
				eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.connections) == 0 && active.Load() == 0 })
			}
			if accepted.Load() != 36 {
				t.Fatal("rejected upgrades leaked capacity", accepted.Load())
			}
		})
	}
}

func previewDialFixture(t *testing.T) *previewGrant {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := &previewGrant{ctx: ctx, cancel: cancel, port: 3000, transport: &http.Transport{}, connections: make(map[*previewConn]struct{})}
	t.Cleanup(g.close)
	return g
}

func previewDialCounts(g *previewGrant) (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.connections), g.pendingDials
}

func TestPreviewConnectionReservationsIncludePendingDials(t *testing.T) {
	g := previewDialFixture(t)
	const addr = "127.0.0.1:3000"
	var held []net.Conn
	newSocket := func(_ context.Context, network, target string) (net.Conn, error) {
		if network != "tcp4" || target != addr {
			t.Errorf("unexpected dial %s %s", network, target)
		}
		conn, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		return conn, nil
	}
	for i := 0; i < previewConnections/2; i++ {
		conn, err := g.dialBounded(context.Background(), "tcp", addr, newSocket)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	entered := make(chan struct{}, previewConnections)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	failed := errors.New("fixture dial failure")
	results := make(chan error, previewConnections)
	for i := 0; i < previewConnections/2; i++ {
		go func() {
			_, err := g.dialBounded(context.Background(), "tcp", addr, func(ctx context.Context, _, _ string) (net.Conn, error) {
				entered <- struct{}{}
				select {
				case <-release:
					return nil, failed
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			results <- err
		}()
	}
	for i := 0; i < previewConnections/2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("reserved dial did not start")
		}
	}
	if open, pending := previewDialCounts(g); open != previewConnections/2 || pending != previewConnections/2 {
		t.Fatal("pending dials not reserved", open, pending)
	}
	var extraDials atomic.Int32
	for i := 0; i < previewConnections; i++ {
		go func() {
			_, err := g.dialBounded(context.Background(), "tcp", addr, func(context.Context, string, string) (net.Conn, error) {
				extraDials.Add(1)
				return nil, failed
			})
			results <- err
		}()
	}
	for i := 0; i < previewConnections; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, errPreviewConnections) {
				t.Fatal("reservation cap not enforced", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("capacity rejection blocked")
		}
	}
	if extraDials.Load() != 0 {
		t.Fatal("overflow reached dialer", extraDials.Load())
	}
	unblock()
	for i := 0; i < previewConnections/2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, failed) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("failed dial did not return")
		}
	}
	if open, pending := previewDialCounts(g); open != previewConnections/2 || pending != 0 {
		t.Fatal("failure leaked slots", open, pending)
	}
	// Every failed reservation can be reused for a live connection.
	for i := 0; i < previewConnections/2; i++ {
		conn, err := g.dialBounded(context.Background(), "tcp", addr, newSocket)
		if err != nil {
			t.Fatal("failed dial retained reservation", err)
		}
		held = append(held, conn)
	}
	var closed sync.WaitGroup
	for _, conn := range held {
		for i := 0; i < 2; i++ {
			closed.Add(1)
			go func(c net.Conn) { defer closed.Done(); _ = c.Close() }(conn)
		}
	}
	closed.Wait()
	if open, pending := previewDialCounts(g); open != 0 || pending != 0 {
		t.Fatal("close leaked slots", open, pending)
	}
}

func TestPreviewDialCancellationRaceClosesUnregisteredSocket(t *testing.T) {
	for _, action := range []string{"request cancellation", "grant closure", "dial error with socket"} {
		t.Run(action, func(t *testing.T) {
			g := previewDialFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			result := make(chan error, 1)
			failed := errors.New("fixture dial failure")
			go func() {
				out, err := g.dialBounded(ctx, "tcp", "127.0.0.1:3000", func(context.Context, string, string) (net.Conn, error) {
					close(entered)
					<-release
					// Simulate a socket returned just as cancellation wins. Even
					// a successful late dial must not escape grant ownership.
					if action == "dial error with socket" {
						return conn, failed
					}
					return conn, nil
				})
				if out != nil {
					_ = out.Close()
					result <- errors.New("published canceled socket")
					return
				}
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("dial did not start")
			}
			if action == "request cancellation" {
				cancel()
			}
			if action == "grant closure" {
				g.close()
			}
			unblock()
			want := error(context.Canceled)
			if action == "dial error with socket" {
				want = failed
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatal("unexpected dial result", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("dial did not finish")
			}
			if open, pending := previewDialCounts(g); open != 0 || pending != 0 {
				t.Fatal("canceled dial retained ownership", open, pending)
			}
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			var data [1]byte
			if _, err := peer.Read(data[:]); err != io.EOF {
				t.Fatal("unregistered socket left open", err)
			}
		})
	}
}

func TestPreviewUnavailableProvidesSafeBrowserRecovery(t *testing.T) {
	h, _, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	port := previewPort(t, upstream)
	upstream.Close()
	grant := previewCreate(t, h, "secret", "s1", port)
	for _, accept := range []string{"text/html,application/xhtml+xml", "application/json"} {
		r := httptest.NewRequest("GET", previewTestOrigin+"/private-path?secret=value", nil)
		r.Header.Set("Cookie", previewCookie+"="+grant.Token)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 502 || w.Header().Get("X-Trill-Preview-Error") != "upstream-unavailable" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing recovery error", w.Code, w.Header())
		}
		body := w.Body.String()
		if !strings.Contains(body, "localhost:"+strconv.Itoa(port)) || strings.Contains(body, grant.Token) || strings.Contains(body, "private-path") || strings.Contains(body, "secret=value") {
			t.Fatal("unsafe or unhelpful error body")
		}
		if strings.HasPrefix(accept, "text/html") {
			if !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, "Reload") || w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("browser fallback missing")
			}
		} else {
			var result map[string]string
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["error"] == "" {
				t.Fatal("non-browser error is not JSON")
			}
		}
	}
}
func TestPreviewUpstreamCannotSpoofCompanionFailure(t *testing.T) {
	h, _, _ := setup(t, false, true)
	t.Cleanup(h.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trill-Preview-Error", "upstream-unavailable")
		w.WriteHeader(502)
		_, _ = io.WriteString(w, "development server's own error")
	}))
	defer upstream.Close()
	grant := previewCreate(t, h, "secret", "s1", previewPort(t, upstream))
	w := previewRequest(h, "GET", "/", "", grant.Token, "")
	if w.Code != 502 || w.Header().Get("X-Trill-Preview-Error") != "" || w.Body.String() != "development server's own error" {
		t.Fatal("upstream spoofed native error or lost own response")
	}
}
