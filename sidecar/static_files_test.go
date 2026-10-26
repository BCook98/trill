package sidecar

import (
	"bytes"
	"encoding/json"
	"image"
	"image/gif"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func staticRoute(folder, asset string) string {
	return "/v1/sessions/s1/static-file?" + url.Values{"root": {folder}, "path": {asset}}.Encode()
}

func staticMkdir(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
		t.Fatal(err)
	}
}

func staticSymlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestStaticFilesAuthenticationAndControl(t *testing.T) {
	for _, control := range []bool{false, true} {
		h, f, root := explorerHTTP(t, control)
		explorerWrite(t, root, "index.html", []byte("fixture"))
		for _, token := range []string{"", "wrong", "secret"} {
			want := 401
			if token == "secret" {
				want = 403
				if control {
					want = 200
				}
			}
			w := request(h, "GET", staticRoute("", "index.html"), token, "")
			if w.Code != want {
				t.Fatalf("control=%v token=%q: %d want %d", control, token, w.Code, want)
			}
			if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), root) {
				t.Fatal("cache/root disclosure", w.Header(), w.Body.String())
			}
		}
		f.mu.Lock()
		calls := len(f.calls)
		f.mu.Unlock()
		if !control && calls != 0 {
			t.Fatal("disabled control consulted metadata")
		}
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if w := request(h, method, staticRoute("", "index.html"), "secret", ""); w.Code != 405 {
				t.Fatal(method, w.Code)
			}
		}
	}
}

func TestStaticFilesAllowlistedAssetsAndSelectedFolder(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	staticMkdir(t, root, "public/demo/assets")
	for _, ext := range []string{"html", "htm", "css", "js", "mjs", "svg", "HTML"} {
		name := "page." + ext
		body := []byte("<svg>fixture</svg>")
		explorerWrite(t, root, "public/demo/"+name, body)
		w := request(h, "GET", staticRoute("public/demo", name), "secret", "")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatal(ext, w.Code, w.Header(), w.Body.String())
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal(w.Header())
		}
	}
	for _, kind := range []string{"png", "jpeg", "gif"} {
		body := photoBytes(t, kind)
		if kind == "gif" {
			var encoded bytes.Buffer
			if err := gif.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
				t.Fatal(err)
			}
			body = encoded.Bytes()
		}
		explorerWrite(t, root, "public/demo/assets/picture."+kind, body)
		w := request(h, "GET", staticRoute("public/demo", "assets/picture."+kind), "secret", "")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("Content-Type") != "image/"+kind {
			t.Fatal(kind, w.Code, w.Header())
		}
	}
	explorerWrite(t, root, "site.css", []byte("project root"))
	explorerWrite(t, root, "public/demo/assets/site.css", []byte("selected folder"))
	for _, tc := range []struct{ folder, asset, want string }{
		{"", "site.css", "project root"}, {"public/demo", "assets/site.css", "selected folder"},
	} {
		w := request(h, "GET", staticRoute(tc.folder, tc.asset), "secret", "")
		if w.Code != 200 || w.Body.String() != tc.want {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	// Omitting root means the authoritative project/worktree root, never cwd.
	if w := request(h, "GET", "/v1/sessions/s1/static-file?path=site.css", "secret", ""); w.Code != 200 || w.Body.String() != "project root" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestStaticFilesRejectInvalidRootsPathsAndTypes(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	invalid := []string{"../outside", "/absolute", ".", "..", ".hidden", "a/.hidden", "a/../b", "a//b", "a/", "a\\b", "a\x00b", "a\nb", "a\x7fb"}
	for _, name := range invalid {
		for _, route := range []string{staticRoute(name, "index.html"), staticRoute("", name+"/index.html")} {
			if w := request(h, "GET", route, "secret", ""); w.Code != 400 {
				t.Fatalf("%q: %d %s", route, w.Code, w.Body.String())
			}
		}
	}
	for _, query := range []string{"", "root=", "path=", "path=a.html&path=b.html", "root=a&root=b&path=a.html", "path=a.html&extra=x", "path=%zz", "root=%ff&path=a.html", "path=%2e%2e%2foutside.html"} {
		if w := request(h, "GET", "/v1/sessions/s1/static-file?"+query, "secret", ""); w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	for _, name := range []string{"secret.env", "settings.json", "key.pem", "README", "font.woff2", "archive.zip"} {
		if w := request(h, "GET", staticRoute("", name), "secret", ""); w.Code != 415 {
			t.Fatal(name, w.Code)
		}
	}
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("invalid request consulted metadata")
	}
	explorerWrite(t, root, "outside.html", []byte("outside selected folder"))
	staticMkdir(t, root, "public")
	for _, asset := range []string{"../outside.html", "/outside.html"} {
		if w := request(h, "GET", staticRoute("public", asset), "secret", ""); w.Code != 400 {
			t.Fatal(asset, w.Code)
		}
	}
	if w := request(h, "GET", staticRoute("public", "outside.html"), "secret", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/v1/sessions/bad%20id/static-file?path=x.html", "secret", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/v1/sessions/unknown/static-file?path=x.html", "secret", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestStaticFilesDenyAllSymlinkPositions(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	staticMkdir(t, root, "public/assets")
	explorerWrite(t, root, ".env", []byte(`API_KEY="fixture-secret"`))
	explorerWrite(t, root, "public/assets/ok.js", []byte("safe"))
	outside := t.TempDir()
	explorerWrite(t, outside, "outside.js", []byte("outside secret"))
	staticSymlink(t, "../.env", filepath.Join(root, "public/leak.js"))
	staticSymlink(t, "assets/ok.js", filepath.Join(root, "public/inside.js"))
	staticSymlink(t, filepath.Join(outside, "outside.js"), filepath.Join(root, "public/outside.js"))
	staticSymlink(t, "public", filepath.Join(root, "root-link"))
	staticSymlink(t, "assets", filepath.Join(root, "public/dir-link"))
	staticSymlink(t, outside, filepath.Join(root, "public/outside-dir"))
	for _, route := range []string{
		staticRoute("public", "leak.js"), staticRoute("public", "inside.js"), staticRoute("public", "outside.js"),
		staticRoute("root-link", "assets/ok.js"), staticRoute("root-link/assets", "ok.js"),
		staticRoute("public/dir-link", "ok.js"), staticRoute("public", "dir-link/ok.js"),
		staticRoute("public", "outside-dir/outside.js"),
	} {
		w := request(h, "GET", route, "secret", "")
		if w.Code != 403 || strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), outside) {
			t.Fatal(route, w.Code, w.Body.String())
		}
	}
	// A symlink supplied by worktree metadata is rejected too.
	alias := filepath.Join(t.TempDir(), "worktree")
	staticSymlink(t, root, alias)
	f.mu.Lock()
	f.sessions[0].WorktreePath = alias
	f.mu.Unlock()
	if w := request(h, "GET", staticRoute("public", "assets/ok.js"), "secret", ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestStaticFilesAuthoritativeWorktreeAndLabRoot(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	worktree := t.TempDir()
	explorerWrite(t, root, "index.html", []byte("project"))
	explorerWrite(t, worktree, "index.html", []byte("worktree"))
	f.mu.Lock()
	f.sessions[0].WorktreePath = worktree
	f.mu.Unlock()
	if w := request(h, "GET", staticRoute("", "index.html"), "secret", ""); w.Code != 200 || w.Body.String() != "worktree" {
		t.Fatal(w.Code, w.Body.String())
	}
	f.mu.Lock()
	f.sessions[0].WorktreePath = filepath.Join(worktree, "missing")
	f.mu.Unlock()
	if w := request(h, "GET", staticRoute("", "index.html"), "secret", ""); w.Code != 404 {
		t.Fatal("fell back to project", w.Code)
	}
	for _, explicit := range []bool{false, true} {
		lab := t.TempDir()
		explorerWrite(t, lab, "index.html", []byte("explicit lab"))
		runner := &explorerLabRunner{fakeRunner: &fakeRunner{}, socket: filepath.Join(lab, "lab.sock")}
		config := Config{AllowControl: true, TmuxSocket: runner.socket, TmuxPath: "/usr/bin/tmux"}
		if explicit {
			config.LabFilesRoot = lab
		}
		svc, err := NewService(config, runner)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		handler := NewHTTP(svc, "secret")
		t.Cleanup(handler.Close)
		route := "/v1/sessions/lab-1/static-file?root=&path=index.html"
		w := request(handler, "GET", route, "secret", "")
		if explicit {
			if w.Code != 200 || w.Body.String() != "explicit lab" {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := request(handler, "GET", route+"&root="+url.QueryEscape(root), "secret", ""); w.Code != 400 {
				t.Fatal(w.Code)
			}
		} else if w.Code != 409 {
			t.Fatal("implicit lab root allowed", w.Code, w.Body.String())
		}
	}
}

func TestStaticFilesBoundedTextImagesAndSpecialFiles(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	png := photoBytes(t, "png")
	cases := []struct {
		name   string
		body   []byte
		status int
	}{
		{"limit.html", bytes.Repeat([]byte("a"), maxTextPreview), 200},
		{"oversize.html", bytes.Repeat([]byte("a"), maxTextPreview+1), 413},
		{"image.png", append(append([]byte{}, png...), bytes.Repeat([]byte{0}, maxImagePreview+1-len(png))...), 413},
		{"binary.js", []byte{0, 1, 2, 3}, 415},
		{"invalid.css", []byte{0xff, 0xfe, 0xff}, 415},
	}
	for _, tc := range cases {
		explorerWrite(t, root, tc.name, tc.body)
		w := request(h, "GET", staticRoute("", tc.name), "secret", "")
		if w.Code != tc.status {
			t.Fatal(tc.name, w.Code, w.Body.String())
		}
		if tc.status != 200 && (bytes.Equal(w.Body.Bytes(), tc.body) || strings.Contains(w.Body.String(), root)) {
			t.Fatal("error leaked file/root")
		}
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.js"), 0600); err != nil {
		t.Fatal(err)
	}
	staticMkdir(t, root, "directory.html")
	for _, asset := range []string{"fifo.js", "directory.html"} {
		if w := request(h, "GET", staticRoute("", asset), "secret", ""); w.Code != 415 {
			t.Fatal(asset, w.Code)
		}
	}
}

func TestStaticFilesSymlinkReplacementCannotRedirect(t *testing.T) {
	// Exercise all mutable request positions: selected root, asset parent, leaf.
	for _, position := range []string{"root", "directory", "leaf"} {
		t.Run(position, func(t *testing.T) {
			h, _, root := explorerHTTP(t, true)
			staticMkdir(t, root, "public/assets")
			explorerWrite(t, root, "public/assets/page.js", []byte("inside"))
			outside := t.TempDir()
			staticMkdir(t, outside, "assets")
			explorerWrite(t, outside, "assets/page.js", []byte("outside secret"))
			explorerWrite(t, outside, "page.js", []byte("outside secret"))
			name, target := filepath.Join(root, "public"), outside
			if position == "directory" {
				name = filepath.Join(root, "public/assets")
			}
			if position == "leaf" {
				name, target = filepath.Join(root, "public/assets/page.js"), filepath.Join(outside, "page.js")
			}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if os.Rename(name, name+"-held") != nil {
						continue
					}
					_ = os.Symlink(target, name)
					_ = os.Remove(name)
					_ = os.Rename(name+"-held", name)
				}
			}()
			defer func() { close(stop); wg.Wait() }()
			for i := 0; i < 200; i++ {
				w := request(h, "GET", staticRoute("public", "assets/page.js"), "secret", "")
				if w.Code == 200 {
					if w.Body.String() != "inside" {
						t.Fatal("read outside selected folder", w.Body.String())
					}
				} else if w.Code != 403 && w.Code != 404 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestStaticFilesPairingAuthRemainsCentral(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	explorerWrite(t, root, "index.html", []byte("fixture"))
	r := httptest.NewRequest("GET", staticRoute("", "index.html"), nil)
	r.Header.Set("Cookie", "trill_preview=secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("preview cookie authorized native read", w.Code)
	}
	r = httptest.NewRequest("GET", staticRoute("", "index.html"), nil)
	r.Header.Add("Authorization", "Bearer secret")
	r.Header.Add("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("duplicate auth accepted", w.Code)
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["error"] != "unauthorized" {
		t.Fatal(w.Body.String(), err)
	}
}
