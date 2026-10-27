package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func explorerHTTP(t *testing.T, control bool) (*HTTP, *fakeRunner, string) {
	t.Helper()
	h, f, _ := setup(t, false, control)
	root := t.TempDir()
	data, err := json.Marshal([]Project{{ID: "p1", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.projectsJSON = string(data)
	f.mu.Unlock()
	h.attachmentRoot = filepath.Join(t.TempDir(), "photos")
	t.Cleanup(h.Close)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.calls {
			if c.name != "herdr" {
				t.Errorf("non-metadata command: %+v", c)
			}
		}
		if f.proc != nil || len(f.terminalArgs) != 0 {
			t.Error("browsing launched process")
		}
	})
	return h, f, root
}
func explorerWrite(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func explorerList(t *testing.T, h *HTTP, target string) fileDirectory {
	t.Helper()
	w := request(h, "GET", target, "secret", "")
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", target, w.Code, w.Body.String())
	}
	var v fileDirectory
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Entries == nil || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal("expected JSON array", w.Body.String())
	}
	return v
}
func explorerPhotoDir(t *testing.T, h *HTTP, id string) string {
	t.Helper()
	dir := filepath.Join(h.attachmentRoot, attachmentSessionDir(id))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func explorerRoutes() []string {
	return []string{"/v1/sessions/s1/files", "/v1/sessions/s1/file?path=a.txt", "/v1/projects/p1/files", "/v1/projects/p1/file?path=a.txt", "/v1/sessions/s1/attachments", "/v1/sessions/s1/attachment?path=a.png"}
}
func TestFilesAuthenticationAndControl(t *testing.T) {
	for _, control := range []bool{false, true} {
		h, f, root := explorerHTTP(t, control)
		explorerWrite(t, root, "a.txt", []byte("hello"))
		explorerWrite(t, explorerPhotoDir(t, h, "s1"), "a.png", photoBytes(t, "png"))
		for _, route := range explorerRoutes() {
			for _, token := range []string{"", "wrong", "secret"} {
				want := 401
				if token == "secret" {
					want = 403
					if control {
						want = 200
					}
				}
				w := request(h, "GET", route, token, "")
				if w.Code != want {
					t.Fatalf("control=%v route=%s: %d want %d", control, route, w.Code, want)
				}
				if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), h.attachmentRoot) {
					t.Fatal("cache or credential/root disclosure", w.Header(), w.Body.String())
				}
				if token == "secret" && w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal(w.Header())
				}
			}
		}
		f.mu.Lock()
		calls := len(f.calls)
		f.mu.Unlock()
		if !control && calls != 0 {
			t.Fatal("disabled browsing consulted metadata")
		}
		if w := request(h, "GET", "/v1/projects", "secret", ""); w.Code != 200 {
			t.Fatal("catalog unavailable", w.Code)
		}
	}
}
func TestFilesReadOnlyMethods(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	explorerWrite(t, root, "a.txt", []byte("unchanged"))
	// POST /attachments belongs to the existing uploader, so do not invoke it.
	for _, route := range explorerRoutes() {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if method == "POST" && route == "/v1/sessions/s1/attachments" {
				continue
			}
			if w := request(h, method, route, "secret", `{"path":"new"}`); w.Code != 405 {
				t.Fatal(method, route, w.Code)
			}
		}
	}
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("unsupported method reached backend")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("filesystem mutated", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(data) != "unchanged" {
		t.Fatal("file changed", err)
	}
	if _, err := os.Stat(h.attachmentRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created attachment storage", err)
	}
}
func TestFilesRootSelectionAndArchives(t *testing.T) {
	h, f, project := explorerHTTP(t, true)
	worktree := t.TempDir()
	explorerWrite(t, project, "which", []byte("project"))
	explorerWrite(t, worktree, "which", []byte("worktree"))
	f.mu.Lock()
	f.sessions[0].WorktreePath = worktree
	f.sessions[1].WorktreePath = worktree
	f.sessions[1].HiddenFromSidebar = true
	f.mu.Unlock()
	for _, tc := range []struct{ route, want string }{{"/v1/sessions/s1/file?path=which", "worktree"}, {"/v1/sessions/s2/file?path=which", "worktree"}, {"/v1/projects/p1/file?path=which", "project"}} {
		w := request(h, "GET", tc.route, "secret", "")
		if w.Code != 200 || w.Body.String() != tc.want {
			t.Fatal(tc.route, w.Code, w.Body.String())
		}
	}
	f.mu.Lock()
	f.sessions[0].WorktreePath = ""
	f.sessions[1].WorktreePath = filepath.Join(worktree, "missing")
	f.mu.Unlock()
	w := request(h, "GET", "/v1/sessions/s1/file?path=which", "secret", "")
	if w.Code != 200 || w.Body.String() != "project" {
		t.Fatal("fallback", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/v1/sessions/s2/files", "secret", ""); w.Code != 404 {
		t.Fatal("must not fall back from absent worktree", w.Code)
	}
}
func TestFilesMissingMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, route string
		status      int
		change      func(*fakeRunner)
	}{
		{"session", "/v1/sessions/unknown/files", 404, nil}, {"project", "/v1/projects/unknown/files", 404, nil},
		{"ID", "/v1/projects/bad%20id/files", 400, nil},
		{"parent", "/v1/sessions/s1/files", 404, func(f *fakeRunner) { f.sessions[0].ProjectID = "unknown" }},
		{"relative worktree", "/v1/sessions/s1/files", 502, func(f *fakeRunner) { f.sessions[0].WorktreePath = "relative" }},
		{"relative project", "/v1/projects/p1/files", 502, func(f *fakeRunner) { f.projectsJSON = `[{"id":"p1","path":"relative"}]` }},
		{"empty root", "/v1/projects/p1/files", 502, func(f *fakeRunner) { f.projectsJSON = `[{"id":"p1","path":""}]` }},
		{"catalog", "/v1/projects/p1/files", 502, func(f *fakeRunner) { f.projectsJSON = `broken` }},
		{"sessions", "/v1/sessions/s1/files", 502, func(f *fakeRunner) { f.sessions = nil }},
		{"photos session", "/v1/sessions/unknown/attachments", 404, nil},
		{"photo session", "/v1/sessions/unknown/attachment?path=x", 404, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, root := explorerHTTP(t, true)
			if tc.change != nil {
				f.mu.Lock()
				tc.change(f)
				f.mu.Unlock()
			}
			w := request(h, "GET", tc.route, "secret", "")
			if w.Code != tc.status || strings.Contains(w.Body.String(), root) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestFilesRejectClientPaths(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	paths := []string{"/etc/passwd", "../outside", "a/../../outside", "a/../b", ".", "./a", "a//b", "a/", "a\\b", "a\x00b", "a\nb", "a\rb", "a\tb", "a\x7fb", "a\u0085b", string([]byte{0xff})}
	for _, route := range explorerRoutes() {
		base := strings.Split(route, "?")[0]
		for _, p := range paths {
			if w := request(h, "GET", base+"?path="+url.QueryEscape(p), "secret", ""); w.Code != 400 {
				t.Fatalf("%s %q: %d", base, p, w.Code)
			}
		}
		for _, q := range []string{"root=" + url.QueryEscape(root), "path=a&path=b", "path=%zz", "path=a;root=b", "path=a&root=b", "worktreePath=x"} {
			if w := request(h, "GET", base+"?"+q, "secret", ""); w.Code != 400 {
				t.Fatal(q, w.Code)
			}
		}
		if strings.HasSuffix(base, "/file") || strings.HasSuffix(base, "/attachment") {
			for _, q := range []string{"", "?path="} {
				if w := request(h, "GET", base+q, "secret", ""); w.Code != 400 {
					t.Fatal("required path", w.Code)
				}
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("invalid request reached metadata")
	}
}
func TestFilesDirectoryListingAndTruncation(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	for _, d := range []string{"z-dir", "A-dir"} {
		if err := os.Mkdir(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	explorerWrite(t, root, "b.txt", []byte("three"))
	explorerWrite(t, root, ".hidden", []byte("one"))
	explorerWrite(t, root, "A-dir/nested", []byte("nested"))
	explorerWrite(t, root, "bad\nname", nil)
	v := explorerList(t, h, "/v1/sessions/s1/files")
	if v.Path != "" || v.Truncated || len(v.Entries) != 4 {
		t.Fatal(v)
	}
	for i, n := range []string{"A-dir", "z-dir", ".hidden", "b.txt"} {
		e := v.Entries[i]
		if e.Name != n || e.Path != n || i < 2 && e.Kind != "directory" || i >= 2 && e.Kind != "file" {
			t.Fatal(v)
		}
	}
	if v.Entries[3].Size != 5 {
		t.Fatal(v.Entries[3])
	}
	v = explorerList(t, h, "/v1/projects/p1/files?path=A-dir")
	if len(v.Entries) != 1 || v.Path != "A-dir" || v.Entries[0].Path != "A-dir/nested" {
		t.Fatal(v)
	}
	v = explorerList(t, h, "/v1/projects/p1/files?path=z-dir")
	if len(v.Entries) != 0 || v.Truncated {
		t.Fatal(v)
	}
	// Use the empty nested directory to test exactly 1000 and then overflow.
	dir := filepath.Join(root, "z-dir")
	for i := 999; i >= 0; i-- {
		explorerWrite(t, dir, fmt.Sprintf("f%04d", i), nil)
	}
	v = explorerList(t, h, "/v1/projects/p1/files?path=z-dir")
	if len(v.Entries) != 1000 || v.Truncated {
		t.Fatal("exact limit", len(v.Entries), v.Truncated)
	}
	if err := os.Mkdir(filepath.Join(dir, "z-last-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	explorerWrite(t, dir, "f1000", nil)
	v = explorerList(t, h, "/v1/projects/p1/files?path=z-dir")
	if len(v.Entries) != 1000 || !v.Truncated || v.Entries[0].Name != "z-last-directory" || v.Entries[999].Name != "f0998" {
		t.Fatal("global sorted cap", len(v.Entries), v.Truncated, v.Entries[0], v.Entries[999])
	}
}
func TestFilesSymlinks(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	outside := t.TempDir()
	explorerWrite(t, outside, "secret", []byte("must not escape"))
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	explorerWrite(t, root, "dir/ok", []byte("inside"))
	for _, l := range []struct{ name, target string }{{"file-link", "dir/ok"}, {"dir-link", "dir"}, {"dir/parent", "../dir/ok"}, {"escape", outside}, {"absolute-inside", filepath.Join(root, "dir")}, {"dangling", "absent"}, {"loop", "loop"}} {
		if err := os.Symlink(l.target, filepath.Join(root, l.name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"file-link", "dir-link/ok", "dir/parent"} {
		w := request(h, "GET", "/v1/sessions/s1/file?path="+p, "secret", "")
		if w.Code != 200 || w.Body.String() != "inside" {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
	if v := explorerList(t, h, "/v1/projects/p1/files?path=dir-link"); len(v.Entries) != 2 {
		t.Fatal(v)
	}
	for _, p := range []string{"escape", "escape/secret", "absolute-inside/ok", "loop", "dangling"} {
		for _, route := range []string{"file", "files"} {
			w := request(h, "GET", "/v1/projects/p1/"+route+"?path="+p, "secret", "")
			if w.Code != 403 && w.Code != 404 {
				t.Fatal(p, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), outside) || strings.Contains(w.Body.String(), "must not escape") {
				t.Fatal("disclosed outside", w.Body.String())
			}
		}
	}
	v := explorerList(t, h, "/v1/projects/p1/files")
	for _, e := range v.Entries {
		if e.Name == "escape" || e.Name == "dangling" || e.Name == "loop" || e.Name == "absolute-inside" {
			if e.Kind != "unsupported" || e.Size != 0 {
				t.Fatal(e)
			}
		}
	}
}
func TestFilesSymlinkReplacementCannotEscape(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	outside := t.TempDir()
	explorerWrite(t, root, "ok", []byte("inside"))
	explorerWrite(t, outside, "secret", []byte("outside secret"))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		link := filepath.Join(root, "changing")
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(link)
			_ = os.Symlink("ok", link)
			_ = os.Remove(link)
			_ = os.Symlink(filepath.Join(outside, "secret"), link)
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 100; i++ {
		w := request(h, "GET", "/v1/projects/p1/file?path=changing", "secret", "")
		if w.Code == 200 {
			if w.Body.String() != "inside" {
				t.Fatal("escaped", w.Body.String())
			}
		} else if w.Code != 403 && w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestFilesSpecialAndDisappearingFiles(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the Unix socket path below the platform's sockaddr_un limit.
	socket := filepath.Join(root, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, p := range []string{"fifo", "s"} {
		for _, route := range []string{"file", "files"} {
			if w := request(h, "GET", "/v1/projects/p1/"+route+"?path="+p, "secret", ""); w.Code != 415 {
				t.Fatal(p, w.Code)
			}
		}
	}
	for _, e := range explorerList(t, h, "/v1/projects/p1/files").Entries {
		if e.Kind != "unsupported" {
			t.Fatal(e)
		}
	}
	explorerWrite(t, root, "gone", []byte("deleted"))
	if err := os.Remove(filepath.Join(root, "gone")); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"file", "files"} {
		if w := request(h, "GET", "/v1/projects/p1/"+route+"?path=gone", "secret", ""); w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	explorerWrite(t, root, "regular", []byte("text"))
	if w := request(h, "GET", "/v1/projects/p1/files?path=regular", "secret", ""); w.Code != 415 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/v1/projects/p1/file?path=regular/child", "secret", ""); w.Code < 400 {
		t.Fatal(w.Code)
	}
}
func TestFilesTextAndImagePreviews(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	webp := append([]byte("RIFF\x0c\x00\x00\x00WEBPVP8 "), make([]byte, 8)...)
	for _, tc := range []struct {
		name, kind string
		data       []byte
		status     int
	}{
		{"empty", "text/plain; charset=utf-8", nil, 200}, {"unicode", "text/plain; charset=utf-8", []byte("Hello 世界\n\tline\r\n"), 200},
		{"page.html", "text/plain; charset=utf-8", []byte("<!DOCTYPE html><script>alert(1)</script>"), 200},
		{"page.svg", "text/plain; charset=utf-8", []byte(`<svg onload="alert(1)"/>`), 200}, {"fake.png", "text/plain; charset=utf-8", []byte("text"), 200},
		{"jpeg.txt", "image/jpeg", photoBytes(t, "jpeg"), 200}, {"png", "image/png", photoBytes(t, "png"), 200}, {"gif", "image/gif", g.Bytes(), 200}, {"webp", "image/webp", webp, 200},
		{"pdf", "", []byte("%PDF-1.7 ASCII binary format"), 415},
		{"binary", "", []byte("a\x00b"), 415}, {"invalid-utf8", "", []byte{0xff, 0xfe, 0x41}, 415}, {"control", "", []byte("x\x1by"), 415}, {"del", "", []byte("x\x7fy"), 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			explorerWrite(t, root, tc.name, tc.data)
			w := request(h, "GET", "/v1/projects/p1/file?path="+tc.name, "secret", "")
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status == 200 && (w.Header().Get("Content-Type") != tc.kind || !bytes.Equal(w.Body.Bytes(), tc.data)) {
				t.Fatal("wrong type or bytes", w.Header())
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
		})
	}
}
func TestFilesPreviewSizeBoundaries(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	for _, img := range []bool{false, true} {
		limit := maxTextPreview
		prefix := []byte("plain text")
		if img {
			limit = maxImagePreview
			prefix = photoBytes(t, "png")
		}
		for _, extra := range []int{0, 1} {
			data := bytes.Repeat([]byte("a"), limit+extra)
			copy(data, prefix)
			explorerWrite(t, root, "large", data)
			w := request(h, "GET", "/v1/sessions/s1/file?path=large", "secret", "")
			want := 200
			if extra != 0 {
				want = 413
			}
			if w.Code != want {
				t.Fatal(img, extra, w.Code)
			}
			if want == 200 && !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatal("incomplete preview")
			}
		}
	}
}

// Endless input models growth after stat; a failing reader must publish no data.
type explorerRepeater struct{ read int }

func (r *explorerRepeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += len(p)
	return len(p), nil
}

type explorerFailingReader struct{}

func (explorerFailingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestFilesPreviewBoundedDuringChanges(t *testing.T) {
	r := &explorerRepeater{}
	data, _, err := readFilePreview(r)
	if !errors.Is(err, errPreviewSize) || data != nil || r.read != maxTextPreview+1 {
		t.Fatal("unbounded", len(data), err, r.read)
	}
	if data, _, err := readFilePreview(explorerFailingReader{}); !errors.Is(err, io.ErrUnexpectedEOF) || data != nil {
		t.Fatal(data, err)
	}
	if data, kind, err := readFilePreview(strings.NewReader("")); err != nil || len(data) != 0 || kind != "text/plain; charset=utf-8" {
		t.Fatal(data, kind, err)
	}
}
func TestFilesClosedCanceledAndIndependentProject(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	f.mu.Lock()
	f.sessions = []rawSession{}
	f.mu.Unlock()
	explorerList(t, h, "/v1/projects/p1/files")
	f.mu.Lock()
	calls := append([]call(nil), f.calls...)
	f.mu.Unlock()
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readFileDirectory(ctx, opened, "."); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	h.Close()
	for _, route := range []string{"/v1/projects/p1/files", "/v1/sessions/s1/attachments"} {
		if w := request(h, "GET", route, "secret", ""); w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
}
func TestFilesDuplicateAuthorizationRejected(t *testing.T) {
	h, _, _ := explorerHTTP(t, true)
	r := httptest.NewRequest("GET", "/v1/projects/p1/files", nil)
	r.Header.Add("Authorization", "Bearer secret")
	r.Header.Add("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestFilesPhotosMissingAndArchived(t *testing.T) {
	h, f, _ := explorerHTTP(t, true)
	v := explorerList(t, h, "/v1/sessions/s1/attachments")
	if v.Path != "" || v.Truncated || len(v.Entries) != 0 {
		t.Fatal(v)
	}
	if _, err := os.Stat(h.attachmentRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("listing created storage", err)
	}
	if w := request(h, "GET", "/v1/sessions/s1/attachment?path=missing.png", "secret", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	f.mu.Lock()
	f.sessions[1].HiddenFromSidebar = true
	f.sessions[1].WorktreePath = "/missing/worktree"
	f.mu.Unlock()
	dir := explorerPhotoDir(t, h, "s2")
	data := photoBytes(t, "png")
	explorerWrite(t, dir, "photo.png", data)
	v = explorerList(t, h, "/v1/sessions/s2/attachments")
	if len(v.Entries) != 1 || v.Entries[0].Path != "photo.png" || v.Entries[0].Kind != "file" || v.Entries[0].Size != int64(len(data)) {
		t.Fatal(v)
	}
	w := request(h, "GET", "/v1/sessions/s2/attachment?path=photo.png", "secret", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal(w.Code, w.Header())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
	if w := request(h, "GET", "/v1/projects/p1/attachments", "secret", ""); w.Code != 404 {
		t.Fatal("unexpected project photos", w.Code)
	}
}
func TestFilesPhotosStayWithinSession(t *testing.T) {
	h, _, worktree := explorerHTTP(t, true)
	a := explorerPhotoDir(t, h, "s1")
	b := explorerPhotoDir(t, h, "s2")
	explorerWrite(t, a, "own.png", photoBytes(t, "png"))
	explorerWrite(t, b, "foreign.png", photoBytes(t, "png"))
	explorerWrite(t, worktree, "worktree.png", photoBytes(t, "png"))
	for _, link := range []struct{ name, target string }{{"foreign", "../" + attachmentSessionDir("s2")}, {"worktree", worktree}, {"inside", "own.png"}} {
		if err := os.Symlink(link.target, filepath.Join(a, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"foreign/foreign.png", "worktree/worktree.png"} {
		if w := request(h, "GET", "/v1/sessions/s1/attachment?path="+p, "secret", ""); w.Code != 403 {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"foreign.png", "worktree.png"} {
		if w := request(h, "GET", "/v1/sessions/s1/attachment?path="+p, "secret", ""); w.Code != 404 {
			t.Fatal(p, w.Code)
		}
	}
	if w := request(h, "GET", "/v1/sessions/s1/attachment?path=inside", "secret", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, e := range explorerList(t, h, "/v1/sessions/s1/attachments").Entries {
		if (e.Name == "foreign" || e.Name == "worktree") && e.Kind != "unsupported" {
			t.Fatal(e)
		}
	}
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attachmentSessionDir("s2"), a); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"attachments", "attachment?path=foreign.png"} {
		if w := request(h, "GET", "/v1/sessions/s1/"+route, "secret", ""); w.Code != 415 {
			t.Fatal("session root symlink", w.Code)
		}
	}
}
func TestFilesPhotosOnlyImagesAndBounded(t *testing.T) {
	h, _, _ := explorerHTTP(t, true)
	dir := explorerPhotoDir(t, h, "s1")
	explorerWrite(t, dir, "html.png", []byte("<html>not a photo</html>"))
	explorerWrite(t, dir, "binary", []byte{0, 1, 2})
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"html.png", "binary", "fifo"} {
		if w := request(h, "GET", "/v1/sessions/s1/attachment?path="+p, "secret", ""); w.Code != 415 {
			t.Fatal(p, w.Code)
		}
	}
	data := make([]byte, maxImagePreview+1)
	copy(data, photoBytes(t, "jpeg"))
	explorerWrite(t, dir, "huge.jpg", data)
	if w := request(h, "GET", "/v1/sessions/s1/attachment?path=huge.jpg", "secret", ""); w.Code != 413 {
		t.Fatal(w.Code)
	}
}
func TestFilesPhotosDefaultStorageUsesTestHome(t *testing.T) {
	h, _, _ := explorerHTTP(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	h.attachmentRoot = ""
	dir := filepath.Join(home, ".local", "share", "trill", "attachments", attachmentSessionDir("s1"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	explorerWrite(t, dir, "image.png", photoBytes(t, "png"))
	v := explorerList(t, h, "/v1/sessions/s1/attachments")
	if len(v.Entries) != 1 || v.Entries[0].Name != "image.png" {
		t.Fatal(v)
	}
}
