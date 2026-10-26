package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trill/sidecar/pairing"
)

func TestDemoPreviewGrantBindsOwnWorktreeWithoutQuery(t *testing.T) {
	s, d, _ := demoForTest(t, "mixed", true)
	upstream := httptest.NewServer(d.PreviewHandler())
	defer upstream.Close()
	store := pairing.NewStore(filepath.Join(d.rootPath, "devices.json"))
	h, err := NewDemoHTTP(s, d, store, previewPort(t, upstream))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, token := pairPhone(t, h, store, "Worktree preview phone")
	w := request(h, "POST", "/v1/sessions", token, `{"projectId":"demo-xirp","kind":"agent","goal":"Build a sample","useWorktree":true}`)
	if w.Code != 200 {
		t.Fatal("create worktree", w.Code, w.Body.String())
	}
	const id = "demo-created-1"
	const heading = "Hosted worktree heading verified"
	if _, err := d.Command(id, "demo heading "+heading); err != nil {
		t.Fatal(err)
	}
	own, err := s.find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	const css = "/* Own worktree asset */ body { color: purple; }"
	if err := os.WriteFile(filepath.Join(own.WorktreePath, "web/assets/style.css"), []byte(css), 0600); err != nil {
		t.Fatal(err)
	}
	grant := previewCreate(t, h, token, id, previewPort(t, upstream))
	for _, test := range []struct{ path, want string }{
		{"/", heading},
		{"/?session=demo-xirp-1&colour=purple", heading},
		{"/about?colour=purple", heading},
		{"/state?session=demo-xirp-1", heading},
		{"/assets/style.css", css},
		{"/assets/style.css?session=demo-xirp-1", css},
	} {
		r := httptest.NewRequest("GET", previewTestOrigin+test.path, nil)
		r.AddCookie(&http.Cookie{Name: previewCookie, Value: grant.Token})
		r.Header.Set("X-Trill-Demo-Session", "demo-xirp-1") // Public input cannot select the upstream workspace.
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), test.want) {
			t.Fatalf("%s used the wrong fixture: %d %s", test.path, w.Code, w.Body.String())
		}
	}
	// A second grant on the same HTTP instance must retain its own selection.
	other := previewCreate(t, h, token, "demo-xirp-1", previewPort(t, upstream))
	r := httptest.NewRequest("GET", previewTestOrigin+"/?session="+id, nil)
	r.AddCookie(&http.Cookie{Name: previewCookie, Value: other.Token})
	r.Header.Set("X-Trill-Demo-Session", id)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), heading) || !strings.Contains(w.Body.String(), "A little progress, anywhere.") {
		t.Fatal("grant selection leaked across sessions", w.Code, w.Body.String())
	}
}
