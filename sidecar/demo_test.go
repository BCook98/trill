package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"trill/sidecar/pairing"
)

func demoForTest(t *testing.T, profile string, control bool) (*Service, *Demo, DemoOptions) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	options := DemoOptions{Root: root, Profile: profile, Executable: "/unused/trill", Socket: filepath.Join(root, "control.sock")}
	s, d, err := NewDemoService(Config{AllowControl: control}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, d, options
}
func demoHTTPForTest(t *testing.T, profile string, control bool, port int) (*Service, *Demo, *HTTP, string) {
	t.Helper()
	s, d, options := demoForTest(t, profile, control)
	store := pairing.NewStore(filepath.Join(options.Root, "devices.json"))
	h, err := NewDemoHTTP(s, d, store, port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	_, token := pairPhone(t, h, store, "Demo test phone")
	return s, d, h, token
}
func TestDemoProfilesUseSyntheticMetadataAndCaps(t *testing.T) {
	for _, profile := range []string{"xirp", "herdr", "mixed"} {
		t.Run(profile, func(t *testing.T) {
			s, d, _ := demoForTest(t, profile, true)
			f := &fakeRunner{}
			s.runner = f
			c, err := s.catalog(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if profile == "mixed" {
				want = 2
			}
			if len(c.Providers) != want || len(c.Projects) != want*2 || len(c.Raw) != want*6 {
				t.Fatalf("bad seed: %+v", c)
			}
			for _, p := range c.Projects {
				if p.Capabilities == nil || !p.Capabilities.Rename || !p.Capabilities.Files || !p.Capabilities.CreateTerminal || p.Capabilities.CreateAgent != (p.Provider == "xirp") || p.Capabilities.Archive != (p.Provider == "xirp") {
					t.Fatalf("capabilities %+v", p)
				}
			}
			for _, v := range c.Raw {
				if v.HerdrTerminal != "" || v.WorktreePath == "" {
					t.Fatalf("unsafe attach metadata: %+v", v)
				}
			}
			if len(f.calls) != 0 {
				t.Fatal("demo invoked real metadata provider")
			}
			status, _ := json.Marshal(d.Status())
			if !bytes.Contains(status, []byte(`"projectId"`)) || bytes.Contains(status, []byte(`"text"`)) || bytes.Contains(status, []byte(d.rootPath)) {
				t.Fatal("unsafe/unusable status", string(status))
			}
		})
	}
}
func TestDemoLifecycleDurabilityAndWorktrees(t *testing.T) {
	s, d, options := demoForTest(t, "mixed", true)
	ctx := context.Background()
	if err := s.Create(ctx, "demo-xirp", "My sample", "Build sample", CreateOptions{Kind: "agent", UseWorktree: true, Branch: "demo/feature"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.find(ctx, "demo-created-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.WorktreePath, "worktrees/demo-created-1") || v.Branch != "demo/feature" {
		t.Fatal("not synthetic worktree", v)
	}
	if err := s.Rename(ctx, v.ID, "Renamed demo"); err != nil {
		t.Fatal(err)
	}
	snap, err := d.Command(v.ID, "demo heading Hello 日本語 <script>alert(1)</script>")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap.Text, "\x1b") {
		t.Fatal("control injection")
	}
	data, err := os.ReadFile(filepath.Join(v.WorktreePath, "web/index.html"))
	if err != nil || !bytes.Contains(data, []byte("&lt;script&gt;")) || bytes.Contains(data, []byte("<script>alert(1)")) {
		t.Fatal("HTML not updated/escaped", err)
	}
	if err := s.Archive(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command(v.ID, "help"); !errors.Is(err, errUnsupported) {
		t.Fatal("archived accepted input", err)
	}
	if err := s.Resume(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command(v.ID, "help"); !errors.Is(err, errUnsupported) {
		t.Fatal("stopped accepted input", err)
	}
	if err := s.Resume(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "demo-herdr", "", "", CreateOptions{Kind: "agent"}); !errors.Is(err, errUnsupported) {
		t.Fatal("Herdr agent create", err)
	}
	if err := s.Archive(ctx, "demo-herdr-1"); !errors.Is(err, errUnsupported) {
		t.Fatal("Herdr archive", err)
	}
	if err := s.Create(ctx, "demo-herdr", "", "", CreateOptions{Kind: "terminal"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal("close not idempotent", err)
	}
	s2, d2, err := NewDemoService(Config{AllowControl: true}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	after, err := s2.find(ctx, v.ID)
	if err != nil || after.Name != "Renamed demo" || after.Status != "running" {
		t.Fatal("state lost", after, err)
	}
	reloaded, err := d2.Snapshot(v.ID)
	if err != nil || !strings.Contains(reloaded.Text, "Hello 日本語") {
		t.Fatal("transcript lost", err)
	}
	data, _ = os.ReadFile(filepath.Join(after.WorktreePath, "web/index.html"))
	if !bytes.Contains(data, []byte("&lt;script&gt;")) {
		t.Fatal("heading lost")
	}
}
func TestDemoReadOnlyAndStateBounds(t *testing.T) {
	_, d, h, token := demoHTTPForTest(t, "xirp", false, 3000)
	if _, err := d.Command("demo-xirp-1", "help"); !errors.Is(err, errUnsupported) {
		t.Fatal(err)
	}
	for _, url := range []string{"/v1/sessions/demo-xirp-1/files", "/v1/sessions/demo-xirp-1/attachments"} {
		if w := request(h, "GET", url, token, ""); w.Code != 403 {
			t.Fatal(url, w.Code)
		}
	}
	_, d2, _ := demoForTest(t, "xirp", true)
	if _, err := d2.Command("demo-xirp-1", strings.Repeat("x", demoMaxInput+1)); !errors.Is(err, errUnsupported) {
		t.Fatal("input limit", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := d2.Command("demo-xirp-1", strings.Repeat("界", 2000)); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := d2.Snapshot("demo-xirp-1")
	if len(snap.Text) > demoMaxTranscript || !strings.Contains(snap.Text, "Unknown demo command") {
		t.Fatal("transcript unbounded")
	}
	if _, err := d2.Resize("demo-xirp-1", 10000, 10000); !errors.Is(err, errUnsupported) {
		t.Fatal("resize bounds", err)
	}
	for len(d2.state.Sessions) < demoMaxSessions {
		if err := d2.create("demo-xirp", "", "", CreateOptions{Kind: "terminal"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d2.create("demo-xirp", "", "", CreateOptions{Kind: "terminal"}); !errors.Is(err, errCapacity) {
		t.Fatal("session limit", err)
	}
}
func TestDemoFaultsAndUnknownCreate(t *testing.T) {
	s, d, h, token := demoHTTPForTest(t, "mixed", true, 3000)
	_ = d.Fault("create-unconfirmed")
	w := request(h, "POST", "/v1/sessions", token, `{"projectId":"demo-xirp","kind":"terminal","useWorktree":false}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "unconfirmed") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.find(context.Background(), "demo-created-1"); err != nil {
		t.Fatal("uncertain mutation not retained", err)
	}
	if w = request(h, "POST", "/v1/sessions", token, `{"projectId":"demo-xirp","kind":"terminal","useWorktree":false}`); w.Code != 200 {
		t.Fatal("one shot not consumed", w.Code)
	}
	_ = d.Fault("provider-unavailable")
	c, err := s.catalog(context.Background())
	if err != nil || len(c.Projects) != 2 || len(c.Providers) != 2 || c.Providers[1].Available {
		t.Fatal("partial provider fault", err, c.Providers)
	}
	_ = d.Fault("clear")
	c, err = s.catalog(context.Background())
	if err != nil || len(c.Projects) != 4 {
		t.Fatal("clear", err)
	}
	if err := d.Fault("unknown"); err == nil {
		t.Fatal("unknown fault accepted")
	}
	for name, want := range map[string]error{"busy": errTerminalBusy, "capacity": errCapacity} {
		_ = d.Fault(name)
		if _, err := d.attach("demo-xirp-1", 80, 24); !errors.Is(err, want) {
			t.Fatal(name, err)
		}
	}
}
func TestDemoAttachUsesFixedArgvAndOwnedLeases(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, d, err := NewDemoService(Config{AllowControl: true, MoshHost: "demo.example.test"}, DemoOptions{Root: root, Profile: "mixed", Executable: "/unused/trill", Socket: filepath.Join(root, "control.sock"), BindHost: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	// Unit transport seam: no real Mosh client/server is exercised by this test.
	f := &fakeRunner{}
	s.runner = f
	ctx := context.Background()
	first, err := s.bootstrapDevice(ctx, "demo-xirp-1", 90, 30, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	args := append([]string{}, f.moshArgs...)
	proc := f.proc
	calls := len(f.calls)
	f.mu.Unlock()
	expected := []string{d.executable, "demo", "terminal", "--socket", d.socket, "--session", "demo-xirp-1"}
	if !reflect.DeepEqual(args[len(args)-len(expected):], expected) || args[2] != "0.0.0.0" || first.Host != "demo.example.test" || calls != 1 {
		t.Fatal("unsafe transport argv/provider call", args)
	}
	if _, err = s.bootstrapDevice(ctx, "demo-xirp-1", 80, 24, "phone-b"); !errors.Is(err, errTerminalBusy) {
		t.Fatal("other device controller", err)
	}
	if _, err = s.bootstrapDevice(ctx, "demo-xirp-1", 80, 24, "phone-a"); err != nil || proc.stopped() != 1 {
		t.Fatal("own replacement", err, proc.stopped())
	}
	if _, err = s.bootstrapDevice(ctx, "demo-herdr-1", 80, 24, "phone-b"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.bootstrapDevice(ctx, "demo-herdr-2", 80, 24, "phone-b"); !errors.Is(err, errCapacity) {
		t.Fatal("lease cap", err)
	}
	if err = d.Fault("drop-leases"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	count := len(s.leases)
	s.mu.Unlock()
	if count != 0 {
		t.Fatal("leases remain", count)
	}
	if _, err = s.bootstrapDevice(ctx, "demo-xirp-1", 80, 24, "phone-b"); err != nil {
		t.Fatal("did not release controller", err)
	}
}
func TestDemoFilesAttachmentsAndPrivateRoots(t *testing.T) {
	_, d, h, token := demoHTTPForTest(t, "xirp", true, 3000)
	for _, path := range []string{"/v1/sessions/demo-xirp-1/files", "/v1/projects/demo-xirp/files?path=nested/examples", "/v1/sessions/demo-xirp-1/file?path=media/sample.png", "/v1/sessions/demo-xirp-1/video?path=media/sample.mp4", "/v1/sessions/demo-xirp-1/static-file?root=web&path=index.html", "/v1/sessions/demo-xirp-1/static-file?root=web&path=media/sample.png"} {
		w := request(h, "GET", path, token, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, name := range []string{"../demo-state.json", "%2Fetc%2Fpasswd", "nested/../../../devices.json"} {
		w := request(h, "GET", "/v1/sessions/demo-xirp-1/file?path="+name, token, "")
		if w.Code == 200 {
			t.Fatal("path escape", name)
		}
	}
	root := filepath.Join(d.rootPath, "fixtures", "demo-xirp")
	if err := os.Symlink(filepath.Join(d.rootPath, "demo-state.json"), filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/v1/sessions/demo-xirp-1/file?path=escape.txt", token, ""); w.Code == 200 {
		t.Fatal("state exposed via link")
	}
	photo, _ := os.ReadFile(filepath.Join(root, "media/sample.png"))
	r := httptest.NewRequest("POST", "/v1/sessions/demo-xirp-1/attachments", bytes.NewReader(photo))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var uploaded photoAttachment
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &uploaded) != nil || !strings.HasPrefix(uploaded.Path, filepath.Join(d.rootPath, "attachments")+string(os.PathSeparator)) {
		t.Fatal("wrong upload root", w.Code, w.Body.String())
	}
	w = request(h, "GET", "/v1/sessions/demo-xirp-1/attachments", token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), uploaded.FileName) {
		t.Fatal("upload absent from browser", w.Code, w.Body.String())
	}
	w = request(h, "GET", "/v1/sessions/demo-xirp-1/attachment?path="+uploaded.FileName, token, "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), photo) {
		t.Fatal("photo not readable")
	}
	snap, err := d.Command("demo-xirp-1", "demo photo '"+uploaded.Path+"'")
	if err != nil || !strings.Contains(snap.Text, "Photo received") {
		t.Fatal("photo ack", err)
	}
	snap, _ = d.Command("demo-xirp-2", "demo photo '"+uploaded.Path+"'")
	if !strings.Contains(snap.Text, "Other paths are unavailable") {
		t.Fatal("cross-session photo accepted")
	}
	status, _ := json.Marshal(d.Status())
	if bytes.Contains(status, []byte(uploaded.FileName)) || bytes.Contains(status, []byte(uploaded.Path)) {
		t.Fatal("photo leaked through status")
	}
	snap, _ = d.Command("demo-xirp-1", "cat ../../demo-state.json")
	if !strings.Contains(snap.Text, "relative sample filename") {
		t.Fatal("command escaped fixture")
	}
	if w = request(h, "GET", "/v1/sessions", "", ""); w.Code != 401 {
		t.Fatal("auth bypass")
	}
	if w = request(h, "POST", "/terminal", token, `{"session":"demo-xirp-1","line":"help"}`); w.Code != 404 {
		t.Fatal("IPC accidentally public", w.Code)
	}
}
func TestDemoAggregateUploadQuotaSerializesAcrossSessions(t *testing.T) {
	_, d, h, token := demoHTTPForTest(t, "xirp", true, 3000)
	photo, _ := os.ReadFile(filepath.Join(d.rootPath, "fixtures/demo-xirp/media/sample.png"))
	h.attachmentLimit = int64(len(photo)) // exactly one photo across the entire root
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, id := range []string{"demo-xirp-1", "demo-xirp-2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/sessions/"+id+"/attachments", bytes.NewReader(photo))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "image/png")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes <- w.Code
		}(id)
	}
	wg.Wait()
	close(codes)
	success, full := 0, 0
	for code := range codes {
		if code == 201 {
			success++
		} else if code == 507 {
			full++
		} else {
			t.Fatal("unexpected upload status", code)
		}
	}
	if success != 1 || full != 1 {
		t.Fatal("aggregate quota race", success, full)
	}
}
func TestDemoTasksPersistAcrossDisconnectAndRestart(t *testing.T) {
	_, d, options := demoForTest(t, "xirp", true)
	if _, err := d.Command("demo-xirp-1", "demo task"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	_, d2, err := NewDemoService(Config{AllowControl: true}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	eventually(t, func() bool {
		snap, _ := d2.Snapshot("demo-xirp-1")
		return strings.Contains(snap.Text, "Demo task complete")
	})
}
func TestDemoRejectsUnsafeStateAndFixtureRoots(t *testing.T) {
	_, d, options := demoForTest(t, "xirp", true)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(options.Root, "demo-state.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var state demoState
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.Sessions[0].Directory = "../"
	data, _ = json.Marshal(state)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewDemoService(Config{}, options); err == nil {
		t.Fatal("malicious state accepted")
	}
	_, d2, options2 := demoForTest(t, "xirp", true)
	if err := d2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(options2.Root, "fixtures/demo-xirp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(options2.Root, filepath.Join(options2.Root, "fixtures/demo-xirp")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewDemoService(Config{}, options2); err == nil {
		t.Fatal("fixture root redirected to state")
	}
}

// These tests exercise an actual HTTP/TLS proxy and RFC6455 connection. They
// intentionally make no claim about real UDP Mosh transport or provider CLIs.
func TestDemoPreviewExactPortAndWebSocketThroughProductionGrant(t *testing.T) {
	s, d, _ := demoForTest(t, "mixed", true)
	if err := s.Create(context.Background(), "demo-xirp", "Preview task", "Build a sample", CreateOptions{Kind: "agent", UseWorktree: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Command("demo-created-1", "demo heading Own worktree initial"); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(d.PreviewHandler())
	defer upstream.Close()
	port := previewPort(t, upstream)
	store := pairing.NewStore(filepath.Join(d.rootPath, "devices.json"))
	h, err := NewDemoHTTP(s, d, store, port)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, token := pairPhone(t, h, store, "Preview phone")
	for _, bad := range []int{port + 1, 1, 7443} {
		w := previewRequest(h, "POST", "/v1/previews", token, "", fmt.Sprintf(`{"sessionId":"demo-created-1","port":%d}`, bad))
		if w.Code != 400 {
			t.Fatal("admin/other port allowed", bad, w.Code)
		}
	}
	frontend := httptest.NewTLSServer(h)
	defer frontend.Close()
	req, _ := http.NewRequest("POST", frontend.URL+"/v1/previews", strings.NewReader(fmt.Sprintf(`{"sessionId":"demo-created-1","port":%d}`, port)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := frontend.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var grant previewCredentials
	err = json.NewDecoder(res.Body).Decode(&grant)
	res.Body.Close()
	if err != nil || res.StatusCode != 201 {
		t.Fatal("grant", res.StatusCode, err)
	}
	req, _ = http.NewRequest("GET", frontend.URL+"/about?session=demo-xirp-1&colour=blue", nil)
	req.AddCookie(&http.Cookie{Name: previewCookie, Value: grant.Token})
	res, err = frontend.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Contains(page, []byte("Own worktree initial")) {
		t.Fatal("proxy page", res.StatusCode)
	}
	conn, err := tls.Dial("tcp", frontend.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /ws?session=demo-xirp-1 HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: trill_preview=%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n", frontend.Listener.Addr(), frontend.URL, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	upgrade, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || upgrade.StatusCode != 101 || upgrade.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal("websocket handshake", upgrade, err)
	}
	initial := demoReadServerFrame(t, reader)
	if !bytes.Contains(initial, []byte("Own worktree initial")) {
		t.Fatal("missing initial frame", string(initial))
	}
	if _, err = d.Command("demo-created-1", "demo heading Updated over real WebSocket"); err != nil {
		t.Fatal(err)
	}
	update := demoReadServerFrame(t, reader)
	if !bytes.Contains(update, []byte("Updated over real WebSocket")) {
		t.Fatal("missing update", string(update))
	}
	// Client masked ping gets a pong; no application command reaches the server.
	_, _ = conn.Write([]byte{0x89, 0x81, 1, 2, 3, 4, 'x' ^ 1})
	pong := demoReadServerFrame(t, reader)
	if string(pong) != "x" {
		t.Fatal("pong", pong)
	}
	if w := previewRequest(h, "DELETE", "/v1/previews/"+grant.ID, token, "", ""); w.Code != 200 {
		t.Fatal("grant delete", w.Code)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err = reader.ReadByte(); err == nil {
		t.Fatal("upgrade survived grant deletion")
	}
}
func demoReadServerFrame(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[0]&128 == 0 || h[1]&128 != 0 {
		t.Fatal("invalid server frame", h)
	}
	n := int(h[1] & 127)
	if n == 126 {
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			t.Fatal(err)
		}
		n = int(binary.BigEndian.Uint16(b[:]))
	}
	if n > 8192 || n == 127 {
		t.Fatal("oversized frame", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		t.Fatal(err)
	}
	return data
}
func TestDemoWSRejectsInvalidFrames(t *testing.T) {
	for _, data := range [][]byte{{0x89, 1, 0}, {0x09, 0x80, 0, 0, 0, 0}, {0xc9, 0x80, 0, 0, 0, 0}, {0x89, 0xfe}, {0x81, 0x80}, {0x88, 0x81, 0, 0, 0, 0, 0}} {
		if _, _, err := demoWSRead(bufio.NewReader(bytes.NewReader(data))); err == nil {
			t.Fatal("accepted invalid frame", data)
		}
	}
}

func TestDemoConcurrentCommandsResizeSnapshotsAndClose(t *testing.T) {
	_, d, _ := demoForTest(t, "mixed", true)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				id := fmt.Sprintf("demo-xirp-%d", 1+n%2)
				switch n {
				case 0:
					if _, err := d.Command(id, "demo heading Concurrent update"); err != nil {
						t.Error(err)
					}
				case 1:
					if _, err := d.Resize(id, 80+i, 24+i); err != nil {
						t.Error(err)
					}
				case 2:
					if _, err := d.Snapshot(id); err != nil {
						t.Error(err)
					}
				case 3:
					_ = d.Status()
				}
			}
		}(n)
	}
	wg.Wait()
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := d.Snapshot("demo-xirp-1"); !errors.Is(err, errClosed) {
		t.Fatal("closed demo readable", err)
	}
}

func TestDemoRejectsMissingHeadingMap(t *testing.T) {
	_, d, options := demoForTest(t, "xirp", true)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(options.Root, "demo-state.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var st demoState
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	st.Headings = nil
	data, _ = json.Marshal(st)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = NewDemoService(Config{}, options); err == nil {
		t.Fatal("missing heading map accepted")
	}
}

func TestDemoPreviewExpiryIsScopedAndRenewable(t *testing.T) {
	_, d, h, token := demoHTTPForTest(t, "xirp", true, 3000)
	_, other, otherHTTP, otherToken := demoHTTPForTest(t, "xirp", true, 3000)
	first := previewCreate(t, h, token, "demo-xirp-1", 3000)
	second := previewCreate(t, otherHTTP, otherToken, "demo-xirp-1", 3000)
	if err := d.Fault("preview-expired"); err != nil {
		t.Fatal(err)
	}
	if w := previewRequest(h, "GET", "/", "", first.Token, ""); w.Code != 401 {
		t.Fatal("expired cookie survived", w.Code)
	}
	otherHTTP.previews.mu.Lock()
	preserved := otherHTTP.previews.grants[second.ID]
	otherHTTP.previews.mu.Unlock()
	if preserved == nil {
		t.Fatal("other sandbox grant expired")
	}
	select {
	case <-preserved.ctx.Done():
		t.Fatal("other sandbox connection cancelled")
	default:
	}
	renewed := previewCreate(t, h, token, "demo-xirp-1", 3000)
	if renewed.Token == first.Token {
		t.Fatal("renewal reused credential")
	}
	expiry, err := time.Parse(time.RFC3339, renewed.ExpiresAt)
	if err != nil || time.Until(expiry) < 25*time.Minute || time.Until(expiry) > previewTTL {
		t.Fatal("production grant TTL changed", renewed.ExpiresAt, err)
	}
	if err := other.Fault("clear"); err != nil {
		t.Fatal(err)
	}
}

func TestDemoStopCancelsTaskActivityBeforeResume(t *testing.T) {
	s, d, _ := demoForTest(t, "xirp", true)
	ctx := context.Background()
	const id = "demo-xirp-1"
	if _, err := d.Command(id, "demo task"); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.find(ctx, id)
	if err != nil || stopped.Status != "stopped" || stopped.ActivityState != "idle" {
		t.Fatalf("cancelled task must be stopped and idle: %+v, %v", stopped, err)
	}
	if err := s.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Wait beyond the normal two-phase task completion window. A cancelled task
	// must neither keep the resumed session working nor emit a completion later.
	time.Sleep(1500 * time.Millisecond)
	resumed, err := s.find(ctx, id)
	if err != nil || resumed.Status != "running" || resumed.ActivityState != "idle" {
		t.Fatalf("resumed session must remain running and idle: %+v, %v", resumed, err)
	}
	snapshot, err := d.Snapshot(id)
	if err != nil || strings.Contains(snapshot.Text, "Demo task complete") {
		t.Fatalf("cancelled task completed after resume: %+v, %v", snapshot, err)
	}
	if _, err = d.Command(id, "demo task"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		snapshot, err := d.Snapshot(id)
		return err == nil && strings.Contains(snapshot.Text, "Demo task complete")
	})
}

func TestDemoPhotoPixelLimitKeepsByteLimit(t *testing.T) {
	_, _, h, token := demoHTTPForTest(t, "xirp", true, 3000)
	if h.photoPixelLimit != 8_000_000 {
		t.Fatal("demo pre-decode limit not configured", h.photoPixelLimit)
	}
	large := compressedPhotoPNG(t, 4096, 2049, 16, 6, 8)
	w := uploadPhoto(h, "demo-xirp-1", token, "image/png", large)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "8 megapixels") || !strings.Contains(w.Body.String(), "choose a smaller photo") {
		t.Fatal("unhelpful demo dimension rejection", w.Code, w.Body.String())
	}
	requireNoPhotos(t, h.attachmentRoot)
	// Pixel limits must not silently reduce the existing 10 MiB encoded limit.
	normal := photoBytes(t, "png")
	normal = append(normal, make([]byte, maxPhotoBytes-len(normal))...)
	if w = uploadPhoto(h, "demo-xirp-1", token, "image/png", normal); w.Code != 201 {
		t.Fatal("10 MiB normal-dimension photo rejected", w.Code, w.Body.String())
	}
}
