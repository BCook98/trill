package sidecar

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabFilesRootConfig(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "foo.sock") // Never connected or created.
	for _, tc := range []struct {
		name, socket, root string
		valid              bool
	}{
		{"production default", "", "", true},
		{"lab browsing disabled", socket, "", true},
		{"explicit directory", socket, root, true},
		{"requires lab", "", root, false},
		{"relative", socket, "relative", false},
		{"missing", socket, filepath.Join(root, "missing"), false},
		{"file", socket, file, false},
		{"invalid path", socket, root + "\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			svc, err := NewService(Config{TmuxSocket: tc.socket, LabFilesRoot: tc.root}, f)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
			if svc != nil {
				defer svc.Close()
				if svc.config.LabFilesRoot != tc.root {
					t.Fatal("configured root changed", svc.config.LabFilesRoot)
				}
			}
			if len(f.calls) != 0 {
				t.Fatal("config validation invoked runner", f.calls)
			}
		})
	}
}

func TestLabFilesRootOptInAndNoSocketParentFallback(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "explicit root"
		}
		t.Run(name, func(t *testing.T) {
			socketParent, root := t.TempDir(), t.TempDir()
			explorerWrite(t, socketParent, "socket-secret", []byte("socket parent secret"))
			explorerWrite(t, root, "which", []byte("explicit lab root"))
			f := &explorerLabRunner{fakeRunner: &fakeRunner{}, socket: filepath.Join(socketParent, "foo.sock")}
			config := Config{AllowControl: true, TmuxSocket: f.socket, TmuxPath: "/usr/bin/tmux"}
			if enabled {
				config.LabFilesRoot = root
			}
			svc, err := NewService(config, f)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			h := NewHTTP(svc, "secret")
			t.Cleanup(h.Close)
			// Exercise the handlers independently of the HTTP constructor's
			// route registration.
			serve := func(project, preview bool, path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/?path="+path, nil)
				id := "lab-1"
				if project {
					id = "terminal-lab"
				}
				r.SetPathValue("id", id)
				w := httptest.NewRecorder()
				h.serveFiles(w, r, project, preview)
				return w
			}
			for _, project := range []bool{false, true} {
				for _, preview := range []bool{false, true} {
					path := ""
					if preview {
						path = "which"
					}
					w := serve(project, preview, path)
					want := 409
					if enabled {
						want = 200
					}
					if w.Code != want || strings.Contains(w.Body.String(), "socket-secret") || strings.Contains(w.Body.String(), socketParent) {
						t.Fatal(project, preview, w.Code, w.Body.String())
					}
					if enabled && preview && w.Body.String() != "explicit lab root" {
						t.Fatal("wrong lab root", w.Body.String())
					}
				}
				if enabled {
					if w := serve(project, true, "socket-secret"); w.Code != 404 {
						t.Fatal("socket parent accessible", w.Code, w.Body.String())
					}
				}
			}
			if !enabled {
				if len(f.calls) != 0 {
					t.Fatal("disabled browsing reached metadata", f.calls)
				}
				return
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			for _, project := range []bool{false, true} {
				if w := serve(project, false, ""); w.Code != 404 {
					t.Fatal("missing configured root fell back", w.Code, w.Body.String())
				}
			}
		})
	}
}
