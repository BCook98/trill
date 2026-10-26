package sidecar

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func videoTestAtom(kind string, payload []byte) []byte {
	data := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(data, uint32(len(data)))
	copy(data[4:8], kind)
	copy(data[8:], payload)
	return data
}

func videoTestMedia(brand string) []byte {
	data := videoTestAtom("ftyp", append([]byte(brand+"\x00\x00\x00\x00"), []byte(brand)...))
	return append(data, videoTestAtom("mdat", []byte{0, 1, 2, 255, 128, 13, 10})...)
}

func videoTestLegacy(version byte) []byte {
	length := 100
	if version == 1 {
		length = 112
	}
	mvhd := make([]byte, length)
	mvhd[0] = version
	data := videoTestAtom("wide", nil)
	data = append(data, videoTestAtom("mdat", []byte{1, 2, 3, 4})...)
	return append(data, videoTestAtom("moov", videoTestAtom("mvhd", mvhd))...)
}

func videoTestRoutes() []string {
	return []string{"/v1/sessions/s1/video", "/v1/projects/p1/video"}
}

func TestVideoAuthenticationAndControl(t *testing.T) {
	for _, control := range []bool{false, true} {
		h, f, root := explorerHTTP(t, control)
		explorerWrite(t, root, "clip.mp4", videoTestMedia("isom"))
		for _, route := range videoTestRoutes() {
			for _, token := range []string{"", "wrong", "secret"} {
				want := http.StatusUnauthorized
				if token == "secret" {
					want = http.StatusForbidden
					if control {
						want = http.StatusOK
					}
				}
				w := request(h, "GET", route+"?path=clip.mp4", token, "")
				if w.Code != want {
					t.Fatalf("control=%v token=%q route=%s: %d want %d", control, token, route, w.Code, want)
				}
				if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), "secret") {
					t.Fatal("cache or root/token disclosure", w.Header(), w.Body.String())
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
			t.Fatal("disabled video route consulted metadata")
		}
	}
}

func TestVideoRejectsInvalidPathsAndMethods(t *testing.T) {
	h, f, root := explorerHTTP(t, true)
	for _, route := range videoTestRoutes() {
		for _, name := range []string{"", ".", "..", "../clip.mp4", "/clip.mp4", "./clip.mp4", "a/../clip.mp4", "a//clip.mp4", "a/", "a\\clip.mp4", "a\x00.mp4", "a\n.mp4", "a\t.mp4", "a\x7f.mp4", string([]byte{255})} {
			if w := request(h, "GET", route+"?path="+url.QueryEscape(name), "secret", ""); w.Code != 400 {
				t.Fatalf("%s path %q: %d", route, name, w.Code)
			}
		}
		for _, query := range []string{"", "path=a.mp4&path=b.mp4", "path=%zz", "path=a.mp4;root=b", "path=a.mp4&root=" + url.QueryEscape(root), "worktreePath=x"} {
			if w := request(h, "GET", route+"?"+query, "secret", ""); w.Code != 400 {
				t.Fatalf("%s query %q: %d", route, query, w.Code)
			}
		}
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if w := request(h, method, route+"?path=clip.mp4", "secret", ""); w.Code != 405 {
				t.Fatal(method, route, w.Code)
			}
		}
		badID := strings.Replace(route, "/s1/", "/bad%20id/", 1)
		badID = strings.Replace(badID, "/p1/", "/bad%20id/", 1)
		if w := request(h, "GET", badID+"?path=clip.mp4", "secret", ""); w.Code != 400 {
			t.Fatal(badID, w.Code)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("invalid request consulted metadata")
	}
}

func TestVideoContentAndMIME(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	for _, tc := range []struct {
		name, mime string
		data       []byte
	}{
		{"clip.mp4", "video/mp4", videoTestMedia("isom")},
		{"clip.MP4", "video/mp4", videoTestMedia("mp42")},
		{"clip.M4v", "video/mp4", videoTestMedia("M4V ")},
		{"clip.MOV", "video/quicktime", videoTestMedia("qt  ")},
		{"legacy.mov", "video/quicktime", videoTestLegacy(0)},
		{"legacy-v1.MoV", "video/quicktime", videoTestLegacy(1)},
		{"mp4-container.mov", "video/mp4", videoTestMedia("avc1")},
		{"qt-container.mp4", "video/quicktime", videoTestMedia("qt  ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			explorerWrite(t, root, tc.name, tc.data)
			for _, route := range videoTestRoutes() {
				r := httptest.NewRequest("GET", route+"?path="+tc.name, nil)
				r.Header.Set("Authorization", "Bearer secret")
				r.Header.Set("Range", "bytes=1-2")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), tc.data) {
					t.Fatalf("%s: %d, got %d bytes want %d", route, w.Code, w.Body.Len(), len(tc.data))
				}
				if w.Header().Get("Content-Type") != tc.mime || w.Header().Get("Content-Length") != strconv.Itoa(len(tc.data)) || w.Header().Get("Content-Range") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal(w.Header())
				}
			}
		})
	}
}

func TestVideoAuthoritativeRoots(t *testing.T) {
	h, f, project := explorerHTTP(t, true)
	worktree := t.TempDir()
	projectData, worktreeData := videoTestMedia("mp42"), videoTestMedia("qt  ")
	explorerWrite(t, project, "clip.mov", projectData)
	explorerWrite(t, worktree, "clip.mov", worktreeData)
	f.mu.Lock()
	f.sessions[0].WorktreePath = worktree
	f.sessions[1].WorktreePath = worktree
	f.sessions[1].HiddenFromSidebar = true
	f.mu.Unlock()
	for _, scope := range []string{"sessions/s1", "sessions/s2", "projects/p1"} {
		want := worktreeData
		if scope == "projects/p1" {
			want = projectData
		}
		w := request(h, "GET", "/v1/"+scope+"/video?path=clip.mov", "secret", "")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatal(scope, w.Code, w.Body.String())
		}
	}
	f.mu.Lock()
	f.sessions[0].WorktreePath = ""
	f.sessions[1].WorktreePath = filepath.Join(worktree, "missing")
	f.mu.Unlock()
	w := request(h, "GET", "/v1/sessions/s1/video?path=clip.mov", "secret", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), projectData) {
		t.Fatal("session project fallback", w.Code)
	}
	for _, route := range []string{"sessions/s2", "sessions/unknown", "projects/unknown"} {
		if w := request(h, "GET", "/v1/"+route+"/video?path=clip.mov", "secret", ""); w.Code != 404 {
			t.Fatal("missing root/resource must not fall back", route, w.Code)
		}
	}
}

func TestVideoSymlinksAndFileKinds(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	outside := t.TempDir()
	explorerWrite(t, root, "clip.mp4", videoTestMedia("isom"))
	explorerWrite(t, outside, "secret.mp4", videoTestMedia("isom"))
	for _, link := range []struct{ name, target string }{
		{"internal.mp4", "clip.mp4"},
		{"escape.mp4", filepath.Join(outside, "secret.mp4")},
		{"escape-dir", outside},
		{"dangling.mp4", "absent.mp4"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "directory.mp4"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, route := range videoTestRoutes() {
		for _, tc := range []struct {
			name string
			code int
		}{
			{"internal.mp4", 200}, {"escape.mp4", 403}, {"escape-dir/secret.mp4", 403},
			{"dangling.mp4", 404}, {"absent.mp4", 404}, {"directory.mp4", 415}, {"fifo.mp4", 415},
		} {
			w := request(h, "GET", route+"?path="+tc.name, "secret", "")
			if w.Code != tc.code || strings.Contains(w.Body.String(), outside) || strings.Contains(w.Body.String(), root) {
				t.Fatal(route, tc.name, w.Code, w.Body.String())
			}
		}
	}
}

func TestVideoRejectsExtensionOnlyAndMalformedMedia(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	badExtended := []byte{0, 0, 0, 1, 'f', 't', 'y', 'p', 255, 255, 255, 255, 255, 255, 255, 255}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty.mp4", nil}, {"text.mp4", []byte("arbitrary text")},
		{"page.mov", []byte("<!DOCTYPE html><html>not media</html>")},
		{"image.m4v", photoBytes(t, "png")}, {"video.txt", videoTestMedia("isom")},
		{"no-extension", videoTestMedia("isom")}, {"audio.mp4", videoTestMedia("M4A ")},
		{"image-brand.mp4", videoTestMedia("avif")}, {"short.mp4", videoTestMedia("isom")[:12]},
		{"unaligned.mp4", videoTestAtom("ftyp", []byte("isom12345"))},
		{"too-small.mp4", []byte{0, 0, 0, 4, 'f', 't', 'y', 'p'}},
		{"overflow.mp4", badExtended},
		{"empty-moov.mov", videoTestAtom("moov", nil)},
		{"short-mvhd.mov", videoTestAtom("moov", videoTestAtom("mvhd", make([]byte, 99)))},
		{"bad-version.mov", videoTestLegacy(2)},
		{"bad-child.mov", videoTestAtom("moov", []byte{0, 0, 0, 100, 'm', 'v', 'h', 'd'})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			explorerWrite(t, root, tc.name, tc.data)
			for _, route := range videoTestRoutes() {
				w := request(h, "GET", route+"?path="+tc.name, "secret", "")
				if w.Code != 415 || w.Header().Get("Content-Type") != "application/json" {
					t.Fatal(tc.name, w.Code, w.Header(), w.Body.String())
				}
			}
		})
	}
}

func TestVideoContainerHeaderVariants(t *testing.T) {
	compatible := videoTestAtom("ftyp", []byte("xxxx\x00\x00\x00\x00mp42"))
	extended := make([]byte, 16)
	binary.BigEndian.PutUint32(extended, 1)
	copy(extended[4:], "ftyp")
	binary.BigEndian.PutUint64(extended[8:], 24)
	extended = append(extended, []byte("isom\x00\x00\x00\x00")...)
	toEOF := videoTestAtom("ftyp", []byte("qt  \x00\x00\x00\x00"))
	binary.BigEndian.PutUint32(toEOF, 0)
	for _, tc := range []struct {
		name, mime string
		data       []byte
	}{
		{"compatible brand", "video/mp4", compatible},
		{"extended size", "video/mp4", extended},
		{"EOF size", "video/quicktime", toEOF},
		{"padding", "video/mp4", append(videoTestAtom("free", nil), videoTestMedia("isom")...)},
		{"short extended size", "", extended[:12]},
		{"zero progress", "", []byte{0, 0, 0, 2, 'f', 'r', 'e', 'e'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := videoContentType(bytes.NewReader(tc.data), int64(len(tc.data)), true); got != tc.mime {
				t.Fatalf("got %q want %q", got, tc.mime)
			}
		})
	}
}

// Count streamed bytes without buffering a 100 MiB boundary fixture in RAM.
type videoCountingWriter struct {
	header http.Header
	code   int
	bytes  int64
	onHead func()
}

func (w *videoCountingWriter) Header() http.Header { return w.header }
func (w *videoCountingWriter) WriteHeader(code int) {
	w.code = code
	if w.onHead != nil {
		w.onHead()
	}
}
func (w *videoCountingWriter) Write(data []byte) (int, error) {
	w.bytes += int64(len(data))
	return len(data), nil
}

func videoTestSparse(t *testing.T, root, name string, size int64) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(videoTestMedia("isom")); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func TestVideoSizeBoundaries(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	for _, size := range []int64{maxVideoDownload - 1, maxVideoDownload, maxVideoDownload + 1} {
		videoTestSparse(t, root, "large.mp4", size)
		for _, route := range videoTestRoutes() {
			r := httptest.NewRequest("GET", route+"?path=large.mp4", nil)
			r.Header.Set("Authorization", "Bearer secret")
			if size > maxVideoDownload {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 413 || !strings.Contains(w.Body.String(), "100 MiB") || !strings.Contains(w.Body.String(), "104857600") {
					t.Fatal(w.Code, w.Body.String())
				}
				continue
			}
			w := &videoCountingWriter{header: make(http.Header)}
			h.ServeHTTP(w, r)
			if w.code != 200 || w.bytes != size || w.Header().Get("Content-Length") != strconv.FormatInt(size, 10) {
				t.Fatal("boundary", size, w.code, w.bytes, w.Header())
			}
		}
	}
}

func TestVideoGrowthAndTruncation(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		h, _, root := explorerHTTP(t, true)
		data := videoTestMedia("isom")
		explorerWrite(t, root, "changing.mp4", data)
		w := &videoCountingWriter{header: make(http.Header), onHead: func() {
			size := maxVideoDownload + 1
			if truncate {
				size = 12
			}
			if err := os.Truncate(filepath.Join(root, "changing.mp4"), size); err != nil {
				t.Fatal(err)
			}
		}}
		r := httptest.NewRequest("GET", "/v1/projects/p1/video?path=changing.mp4", nil)
		r.Header.Set("Authorization", "Bearer secret")
		var aborted any
		func() {
			defer func() { aborted = recover() }()
			h.ServeHTTP(w, r)
		}()
		if w.code != 200 || w.Header().Get("Content-Length") != strconv.Itoa(len(data)) {
			t.Fatal(w.code, w.Header())
		}
		if truncate {
			if aborted != http.ErrAbortHandler || w.bytes != 12 {
				t.Fatal("short stream must abort", aborted, w.bytes)
			}
		} else if aborted != nil || w.bytes != int64(len(data)) {
			t.Fatal("growth escaped observed size", aborted, w.bytes)
		}
	}
}

// Exercise the wire protocol: a native downloader sees an exact full response,
// while an on-disk truncation during streaming produces a transport failure.
func TestVideoHTTPDownloadAndTruncation(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		h, _, root := explorerHTTP(t, true)
		data := videoTestMedia("mp42")
		explorerWrite(t, root, "clip.mp4", data)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if truncate {
				w = &videoHeaderHook{ResponseWriter: w, hook: func() {
					if err := os.Truncate(filepath.Join(root, "clip.mp4"), 12); err != nil {
						t.Error(err)
					}
				}}
			}
			h.ServeHTTP(w, r)
		}))
		r, err := http.NewRequest("GET", server.URL+"/v1/sessions/s1/video?path=clip.mp4", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer secret")
		response, err := server.Client().Do(r)
		if err == nil {
			var received []byte
			received, err = io.ReadAll(response.Body)
			response.Body.Close()
			if !truncate && (response.StatusCode != 200 || response.ContentLength != int64(len(data)) || !bytes.Equal(received, data)) {
				t.Fatal(response.StatusCode, response.ContentLength, received)
			}
		}
		server.Close()
		if truncate && err == nil {
			t.Fatal("truncated transfer appeared successful")
		}
		if !truncate && err != nil {
			t.Fatal(err)
		}
	}
}

type videoHeaderHook struct {
	http.ResponseWriter
	hook func()
}

func (w *videoHeaderHook) WriteHeader(code int) {
	if code == 200 {
		w.hook()
	}
	w.ResponseWriter.WriteHeader(code)
}

func TestVideoLeavesTextPreviewLimitUnchanged(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	for _, extra := range []int{0, 1} {
		explorerWrite(t, root, "text.txt", bytes.Repeat([]byte("a"), maxTextPreview+extra))
		for _, scope := range []string{"sessions/s1", "projects/p1"} {
			w := request(h, "GET", "/v1/"+scope+"/file?path=text.txt", "secret", "")
			want := 200
			if extra == 1 {
				want = 413
			}
			if w.Code != want || (extra == 0 && w.Body.Len() != maxTextPreview) {
				t.Fatal(scope, extra, w.Code, w.Body.Len())
			}
		}
	}
	if maxTextPreview != 2*1024*1024 {
		t.Fatal("text limit changed", maxTextPreview)
	}
}

func TestVideoClosedService(t *testing.T) {
	h, _, _ := explorerHTTP(t, true)
	h.Close()
	for _, route := range videoTestRoutes() {
		if w := request(h, "GET", route+"?path=clip.mp4", "secret", ""); w.Code != 503 {
			t.Fatal(route, w.Code)
		}
	}
}

func TestVideoDescriptorSurvivesPathReplacement(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	outside := t.TempDir()
	data := videoTestMedia("isom")
	explorerWrite(t, root, "clip.mp4", data)
	explorerWrite(t, outside, "secret.mp4", []byte("outside secret content"))
	w := httptest.NewRecorder()
	hook := &videoHeaderHook{ResponseWriter: w, hook: func() {
		if err := os.Remove(filepath.Join(root, "clip.mp4")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret.mp4"), filepath.Join(root, "clip.mp4")); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest("GET", "/v1/projects/p1/video?path=clip.mp4", nil)
	r.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(hook, r)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("read followed replaced path", w.Code, w.Body.Bytes())
	}
}

// Scale the production write timeout down; a progressing video must still
// complete after that ordinary API deadline, with a bounded replacement.
func TestVideoOverridesOrdinaryWriteDeadline(t *testing.T) {
	h, _, root := explorerHTTP(t, true)
	data := videoTestMedia("mp42")
	explorerWrite(t, root, "clip.mp4", data)
	deadlines := make(chan time.Time, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&videoSlowWriter{ResponseWriter: w, deadlines: deadlines}, r)
	}))
	server.Config.WriteTimeout = 25 * time.Millisecond
	server.Start()
	defer server.Close()
	req, err := http.NewRequest("GET", server.URL+"/v1/sessions/s1/video?path=clip.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("ordinary API deadline cut off video:", err)
	}
	defer response.Body.Close()
	received, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !bytes.Equal(received, data) {
		t.Fatal("incomplete video", response.StatusCode, len(received), err)
	}
	select {
	case deadline := <-deadlines:
		remaining := time.Until(deadline)
		if remaining < 4*time.Minute || remaining > 5*time.Minute {
			t.Fatal("unbounded or too short video deadline", remaining)
		}
	default:
		t.Fatal("video did not override write deadline")
	}
}

type videoSlowWriter struct {
	http.ResponseWriter
	deadlines chan time.Time
}

func (w *videoSlowWriter) Write(data []byte) (int, error) {
	time.Sleep(100 * time.Millisecond)
	return w.ResponseWriter.Write(data)
}
func (w *videoSlowWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines <- deadline
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}
