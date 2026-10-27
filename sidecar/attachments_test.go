package sidecar

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func photoHTTP(t *testing.T, lab, control bool) (*HTTP, *fakeRunner, *Service) {
	t.Helper()
	h, f, svc := setup(t, lab, control)
	h.attachmentRoot = filepath.Join(t.TempDir(), "attachments")
	t.Cleanup(h.Close)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.calls {
			t.Errorf("upload invoked an executable: %+v", c)
		}
		if f.proc != nil || len(f.terminalArgs) != 0 || len(svc.leases) != 0 {
			t.Error("upload launched a process or created a lease")
		}
	})
	return h, f, svc
}

func photoBytes(t *testing.T, kind string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Pix[0], img.Pix[3] = 255, 255
	var b bytes.Buffer
	var err error
	if kind == "jpeg" {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func uploadPhoto(h *HTTP, id, token, contentType string, data []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/sessions/"+id+"/attachments", bytes.NewReader(data))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requireNoPhotos(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), "photo-") || strings.HasPrefix(entry.Name(), ".trill-") {
			t.Errorf("rejected upload left a file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPhotoAttachmentCreatedPrivately(t *testing.T) {
	for _, lab := range []bool{false, true} {
		for _, kind := range []string{"jpeg", "png"} {
			t.Run(kind+"/lab="+strconv.FormatBool(lab), func(t *testing.T) {
				h, f, _ := photoHTTP(t, lab, true)
				before := append([]rawSession(nil), f.sessions...)
				id := "s1"
				if lab {
					id = "lab-1"
				}
				data := photoBytes(t, kind)
				seen := map[string]bool{}
				for i := 0; i < 2; i++ {
					w := uploadPhoto(h, id, "secret", "image/"+kind, data)
					if w.Code != 201 {
						t.Fatal(w.Code, w.Body.String())
					}
					var fields map[string]json.RawMessage
					var result photoAttachment
					if json.Unmarshal(w.Body.Bytes(), &result) != nil || json.Unmarshal(w.Body.Bytes(), &fields) != nil || len(fields) != 4 {
						t.Fatal("invalid response", w.Body.String())
					}
					ext := "png"
					if kind == "jpeg" {
						ext = "jpg"
					}
					if !regexp.MustCompile(`^photo-[0-9a-f]{32}\.`+ext+`$`).MatchString(result.FileName) || seen[result.FileName] {
						t.Fatal("invalid or reused filename", result.FileName)
					}
					seen[result.FileName] = true
					wantDir := filepath.Join(h.attachmentRoot, attachmentSessionDir(id))
					if filepath.Dir(result.Path) != wantDir || filepath.Base(result.Path) != result.FileName || result.ContentType != "image/"+kind || result.ByteCount != len(data) {
						t.Fatalf("invalid attachment: %+v", result)
					}
					stored, err := os.ReadFile(result.Path)
					if err != nil || !bytes.Equal(stored, data) {
						t.Fatal("stored photo differs", err)
					}
					for _, path := range []string{h.attachmentRoot, wantDir, result.Path} {
						info, err := os.Lstat(path)
						if err != nil {
							t.Fatal(err)
						}
						wantMode := os.FileMode(0700)
						if path == result.Path {
							wantMode = 0600
						}
						if info.Mode().Perm() != wantMode || info.Mode()&os.ModeSymlink != 0 {
							t.Fatalf("unsafe mode on %s: %s", path, info.Mode())
						}
					}
					entries, err := os.ReadDir(wantDir)
					if err != nil || len(entries) != i+1 {
						t.Fatal("temporary files left behind", entries, err)
					}
					if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
						t.Fatal("wrong response headers", w.Header())
					}
				}
				if !reflect.DeepEqual(before, f.sessions) {
					t.Fatal("upload changed sessions")
				}
			})
		}
	}
}

func TestPhotoAttachmentDefaultStorage(t *testing.T) {
	h, _, _ := photoHTTP(t, false, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	h.attachmentRoot = ""
	w := uploadPhoto(h, "s1", "secret", "image/png", photoBytes(t, "png"))
	var result photoAttachment
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	want := filepath.Join(home, ".local", "share", "trill", "attachments", attachmentSessionDir("s1"))
	if filepath.Dir(result.Path) != want {
		t.Fatal("wrong default storage", result.Path)
	}
}

func TestPhotoAttachmentAuthorizationAndReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		control     bool
		status      int
	}{
		{"missing", "", true, 401}, {"wrong", "wrong", true, 401}, {"read-only", "secret", false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := photoHTTP(t, false, tc.control)
			w := uploadPhoto(h, "s1", tc.token, "image/png", photoBytes(t, "png"))
			if w.Code != tc.status || len(f.calls) != 0 {
				t.Fatal(w.Code, w.Body.String(), f.calls)
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
	for _, control := range []bool{false, true} {
		t.Run("paired-control="+strconv.FormatBool(control), func(t *testing.T) {
			h, _, _, store, _ := pairedHTTP(t, control, "")
			h.attachmentRoot = filepath.Join(t.TempDir(), "attachments")
			id, token := pairPhone(t, h, store, "Phone")
			want := 403
			if control {
				want = 201
			}
			data := photoBytes(t, "png")
			if w := uploadPhoto(h, "lab-1", token, "image/png", data); w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if err := store.Revoke(id); err != nil {
				t.Fatal(err)
			}
			if w := uploadPhoto(h, "lab-1", token, "image/png", data); w.Code != 401 {
				t.Fatal("revoked device uploaded", w.Code)
			}
		})
	}
}

func TestPhotoAttachmentContentTypeAndLimits(t *testing.T) {
	for _, contentType := range []string{"", "application/json", "image/gif", "image/webp", "multipart/form-data; boundary=x", "image/png; name=evil.png"} {
		t.Run(contentType, func(t *testing.T) {
			h, f, _ := photoHTTP(t, false, true)
			w := uploadPhoto(h, "s1", "secret", contentType, photoBytes(t, "png"))
			if w.Code != 415 || len(f.calls) != 0 {
				t.Fatal(w.Code, w.Body.String(), f.calls)
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
	for _, mode := range []string{"length", "stream", "duplicate type", "encoding", "read error"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := photoHTTP(t, false, true)
			r := httptest.NewRequest("POST", "/v1/sessions/s1/attachments", bytes.NewReader(photoBytes(t, "png")))
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Content-Type", "image/png")
			want := 413
			switch mode {
			case "length":
				r.ContentLength = maxPhotoBytes + 1
			case "stream":
				r.ContentLength = -1
				r.Body = io.NopCloser(io.LimitReader(zeroPhotoReader{}, maxPhotoBytes+1))
			case "duplicate type":
				r.Header.Add("Content-Type", "image/png")
				want = 415
			case "encoding":
				r.Header.Set("Content-Encoding", "gzip")
				want = 415
			case "read error":
				r.Body = io.NopCloser(errorPhotoReader{})
				want = 400
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
	h, _, _ := photoHTTP(t, false, true)
	data := append(photoBytes(t, "png"), make([]byte, maxPhotoBytes)...)
	if w := uploadPhoto(h, "s1", "secret", "image/png", data[:maxPhotoBytes]); w.Code != 201 {
		t.Fatal("exactly 10 MiB rejected", w.Code, w.Body.String())
	}
}

type zeroPhotoReader struct{}

func (zeroPhotoReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type errorPhotoReader struct{}

func (errorPhotoReader) Read([]byte) (int, error) { return 0, errors.New("failed body") }

func TestPhotoAttachmentValidatesImageData(t *testing.T) {
	pngData, jpegData := photoBytes(t, "png"), photoBytes(t, "jpeg")
	bomb := append([]byte(nil), pngData...)
	binary.BigEndian.PutUint32(bomb[16:20], 100_000)
	binary.BigEndian.PutUint32(bomb[20:24], 100_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	if config, _, err := image.DecodeConfig(bytes.NewReader(bomb)); err != nil || config.Width != 100_000 {
		t.Fatal("invalid dimension fixture", config, err)
	}
	corrupt := append([]byte(nil), pngData...)
	corrupt[len(corrupt)-1] ^= 1
	for _, tc := range []struct {
		name, contentType string
		data              []byte
	}{
		{"empty", "image/png", nil}, {"text", "image/png", []byte("not an image")},
		{"claimed-png", "image/png", jpegData}, {"claimed-jpeg", "image/jpeg", pngData},
		{"truncated-png", "image/png", pngData[:33]}, {"truncated-jpeg", "image/jpeg", jpegData[:len(jpegData)/2]},
		{"corrupt", "image/png", corrupt}, {"dimensions", "image/png", bomb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := photoHTTP(t, false, true)
			w := uploadPhoto(h, "s1", "secret", tc.contentType, tc.data)
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
}

func TestPhotoAttachmentSessionEligibility(t *testing.T) {
	for _, mode := range []string{"missing", "hidden", "completed", "failed", "stopped", "unknown", "dead", "agent-mode", "no-terminal", "bad-terminal", "closed-http", "closed-service", "running", "waiting", "idle", "finished"} {
		t.Run(mode, func(t *testing.T) {
			h, f, svc := photoHTTP(t, false, true)
			want, id := 409, "s1"
			switch mode {
			case "missing":
				id, want = "absent", 404
			case "hidden":
				f.sessions[0].HiddenFromSidebar = true
			case "completed", "failed", "stopped", "unknown":
				f.sessions[0].Status = mode
			case "dead":
				f.sessions[0].TerminalLive = false
			case "agent-mode":
				f.sessions[0].Mode = "agent"
			case "no-terminal":
				f.sessions[0].HerdrTerminal = ""
			case "bad-terminal":
				f.sessions[0].HerdrTerminal = "bad/session"
			case "closed-http":
				h.Close()
				want = 503
			case "closed-service":
				svc.Close()
				want = 503
			default:
				f.sessions[0].Status = mode
				want = 201
			}
			w := uploadPhoto(h, id, "secret", "image/png", photoBytes(t, "png"))
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if want != 201 {
				requireNoPhotos(t, h.attachmentRoot)
			}
		})
	}
}

func TestPhotoAttachmentRejectsTraversalAndIgnoresClientNames(t *testing.T) {
	for _, id := range []string{"..", "%2e%2e", "s1%2f..%2fs2", "%2ftmp%2fphoto", "s1%00", strings.Repeat("x", 129)} {
		t.Run(id, func(t *testing.T) {
			h, f, _ := photoHTTP(t, false, true)
			w := uploadPhoto(h, id, "secret", "image/png", photoBytes(t, "png"))
			if w.Code < 300 || w.Code >= 500 || len(f.calls) != 0 {
				t.Fatal("invalid ID not rejected", w.Code, f.calls)
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
	h, _, _ := photoHTTP(t, false, true)
	r := httptest.NewRequest("POST", "/v1/sessions/s1/attachments?path=/tmp/evil&fileName=evil.png", bytes.NewReader(photoBytes(t, "png")))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "image/png")
	r.Header.Set("Content-Disposition", `attachment; filename="../../evil.png"`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result photoAttachment
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || strings.Contains(result.Path, "evil") || filepath.Dir(result.Path) != filepath.Join(h.attachmentRoot, attachmentSessionDir("s1")) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPhotoAttachmentPrivateStorageFailures(t *testing.T) {
	for _, mode := range []string{"root-symlink", "session-symlink", "unsafe-parent", "unsafe-parent-alias", "root-mode", "session-mode", "file-mode", "file-symlink", "hardlink", "fifo", "relative-root"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := photoHTTP(t, false, true)
			base := filepath.Dir(h.attachmentRoot)
			outside := filepath.Join(base, "outside")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.Mkdir(outside, 0700))
			sentinel := filepath.Join(outside, "sentinel")
			must(os.WriteFile(sentinel, []byte("unchanged"), 0600))
			if mode != "root-symlink" && mode != "relative-root" {
				must(os.Mkdir(h.attachmentRoot, 0700))
			}
			dir := filepath.Join(h.attachmentRoot, attachmentSessionDir("s1"))
			switch mode {
			case "root-symlink":
				must(os.Symlink(outside, h.attachmentRoot))
			case "session-symlink":
				must(os.Symlink(outside, dir))
			case "unsafe-parent", "unsafe-parent-alias":
				must(os.Chmod(outside, 0777))
				parent := outside
				if mode == "unsafe-parent-alias" {
					parent = filepath.Join(base, "alias")
					must(os.Symlink(outside, parent))
				}
				h.attachmentRoot = filepath.Join(parent, "new-parent", "attachments")
			case "root-mode":
				must(os.Chmod(h.attachmentRoot, 0755))
			case "session-mode":
				must(os.Mkdir(dir, 0755))
			case "file-mode":
				must(os.WriteFile(filepath.Join(h.attachmentRoot, "unsafe"), []byte("x"), 0644))
			case "file-symlink":
				must(os.Symlink(sentinel, filepath.Join(h.attachmentRoot, "unsafe")))
			case "hardlink":
				must(os.Link(sentinel, filepath.Join(h.attachmentRoot, "unsafe")))
			case "fifo":
				must(syscall.Mkfifo(filepath.Join(h.attachmentRoot, "unsafe"), 0600))
			case "relative-root":
				h.attachmentRoot = "relative-attachments"
			}
			w := uploadPhoto(h, "s1", "secret", "image/png", photoBytes(t, "png"))
			if w.Code != 503 || strings.Contains(w.Body.String(), base) {
				t.Fatal("unsafe storage accepted or leaked a path", w.Code, w.Body.String())
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != "unchanged" {
				t.Fatal("outside file changed", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatal("wrote outside storage", entries, err)
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
}

func TestPhotoAttachmentQuota(t *testing.T) {
	for _, mode := range []string{"full", "boundary", "entries", "other-session", "locked"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := photoHTTP(t, false, true)
			data := photoBytes(t, "png")
			if err := os.Mkdir(h.attachmentRoot, 0700); err != nil {
				t.Fatal(err)
			}
			want := 507
			if mode == "locked" {
				lock, err := os.Open(h.attachmentRoot)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				want = 503
			} else if mode == "entries" {
				for i := 0; i < maxAttachmentEntries; i++ {
					if err := os.WriteFile(filepath.Join(h.attachmentRoot, strconv.Itoa(i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				dir := h.attachmentRoot
				if mode == "other-session" {
					dir = filepath.Join(dir, attachmentSessionDir("s2"))
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				file, err := os.OpenFile(filepath.Join(dir, "existing"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				size := int64(maxAttachmentBytes)
				if mode == "boundary" {
					size -= int64(len(data))
					want = 201
				}
				if err := file.Truncate(size); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			w := uploadPhoto(h, "s1", "secret", "image/png", data)
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if want != 201 {
				requireNoPhotos(t, h.attachmentRoot)
			}
			if mode == "boundary" {
				if w := uploadPhoto(h, "s1", "secret", "image/png", data); w.Code != 507 {
					t.Fatal("quota exceeded", w.Code)
				}
			}
		})
	}
}

func TestPhotoAttachmentConcurrentQuota(t *testing.T) {
	h, _, _ := photoHTTP(t, false, true)
	data := photoBytes(t, "png")
	if err := os.Mkdir(h.attachmentRoot, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(h.attachmentRoot, "existing"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(int64(maxAttachmentBytes - len(data))); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	other := NewHTTP(h.service, "secret")
	other.attachmentRoot = h.attachmentRoot
	defer other.Close()
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := h
			if i%2 != 0 {
				target = other
			}
			codes <- uploadPhoto(target, "s1", "secret", "image/png", data).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code != 507 {
			t.Fatal("unexpected status", code)
		}
	}
	if created != 1 {
		t.Fatal("concurrent uploads bypassed quota", created)
	}
}

type changingPhotoReader struct {
	reader io.Reader
	change func()
}

func (r *changingPhotoReader) Read(p []byte) (int, error) {
	if r.change != nil {
		r.change()
		r.change = nil
	}
	return r.reader.Read(p)
}

func TestPhotoAttachmentRechecksSessionAndCancellation(t *testing.T) {
	for _, mode := range []string{"stopped", "hidden", "missing", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			h, f, _ := photoHTTP(t, false, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &changingPhotoReader{reader: bytes.NewReader(photoBytes(t, "png")), change: func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				switch mode {
				case "stopped":
					f.sessions[0].Status = "stopped"
				case "hidden":
					f.sessions[0].HiddenFromSidebar = true
				case "missing":
					f.sessions = []rawSession{}
				case "cancelled":
					cancel()
				}
			}}
			r := httptest.NewRequest("POST", "/v1/sessions/s1/attachments", body).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Content-Type", "image/png")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 409
			if mode == "missing" {
				want = 404
			}
			if mode == "cancelled" {
				want = 503
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			requireNoPhotos(t, h.attachmentRoot)
		})
	}
}

func TestPhotoAttachmentRechecksRevocationAfterUpload(t *testing.T) {
	h, _, _, store, _ := pairedHTTP(t, true, "")
	h.attachmentRoot = filepath.Join(t.TempDir(), "attachments")
	id, token := pairPhone(t, h, store, "Phone")
	body := &changingPhotoReader{reader: bytes.NewReader(photoBytes(t, "png")), change: func() {
		if err := store.Revoke(id); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest("POST", "/v1/sessions/lab-1/attachments", body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("device revoked during upload persisted photo", w.Code, w.Body.String())
	}
	requireNoPhotos(t, h.attachmentRoot)
}

func TestHerdrPhotoUploadUsesTerminalIdentityWithNativeController(t *testing.T) {
	r := &herdrRunner{snapshot: herdrFixture}
	s := herdrService(t, r)
	rows, err := s.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTP(s, "secret")
	t.Cleanup(h.Close)
	h.attachmentRoot = filepath.Join(t.TempDir(), "attachments")
	response := uploadPhoto(h, rows[0].ID, "secret", "image/jpeg", photoBytes(t, "jpeg"))
	if response.Code != 201 {
		t.Fatalf("Herdr photo upload: %d %s", response.Code, response.Body.String())
	}
	var attachment photoAttachment
	if err = json.Unmarshal(response.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(attachment.Path); err != nil {
		t.Fatal("photo was not stored", err)
	}
	if len(r.terminalArgs) != 0 || len(s.leases) != 0 {
		t.Fatal("upload started terminal transport")
	}
	for _, command := range r.commands {
		if command.name != "/tmp/herdr" {
			t.Fatal("upload invoked a different backend")
		}
	}
}

// Stream a constant PNG raster into zlib, avoiding a full source image allocation.
// RGBA16 is deliberately eight bytes/pixel, even though its compressed body is tiny.
func compressedPhotoPNG(t *testing.T, width, height, depth, colourType, bytesPerPixel int) []byte {
	t.Helper()
	var raster bytes.Buffer
	compressor := zlib.NewWriter(&raster)
	row := make([]byte, 1+width*bytesPerPixel) // filter type zero followed by zero pixels
	for y := 0; y < height; y++ {
		if _, err := compressor.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(data)))
		out.Write(size[:])
		out.WriteString(kind)
		out.Write(data)
		hash := crc32.NewIEEE()
		_, _ = hash.Write([]byte(kind))
		_, _ = hash.Write(data)
		binary.BigEndian.PutUint32(size[:], hash.Sum32())
		out.Write(size[:])
	}
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	header[8] = byte(depth)
	header[9] = byte(colourType)
	chunk("IHDR", header)
	chunk("IDAT", raster.Bytes())
	chunk("IEND", nil)
	return out.Bytes()
}

func TestPhotoPixelLimitRejectsCompressedRGBA16BeforeDecode(t *testing.T) {
	data := compressedPhotoPNG(t, 4096, 2049, 16, 6, 8) // 8,392,704 pixels, ~64 MiB decoded.
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width != 4096 || config.Height != 2049 || len(data) >= maxPhotoBytes {
		t.Fatal("bad compressed dimension fixture", config, format, err, len(data))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	valid := validPhotoWithPixelLimit(data, "image/png", 8_000_000)
	runtime.ReadMemStats(&after)
	if valid {
		t.Fatal("oversized 16-bit image accepted")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 8<<20 {
		t.Fatalf("dimension rejection allocated %d bytes; full raster must never be decoded", allocated)
	}
	if !validPhotoWithPixelLimit(compressedPhotoPNG(t, 2, 2, 16, 6, 8), "image/png", 8_000_000) {
		t.Fatal("valid small 16-bit PNG rejected")
	}
}

func TestPhotoProductionPixelDefaultStillAcceptsAboveDemoLimit(t *testing.T) {
	data := compressedPhotoPNG(t, 3000, 3000, 8, 0, 1) // 9 MP grayscale, above demo's limit.
	if !validPhoto(data, "image/png") {
		t.Fatal("production wrapper no longer accepts a valid photo below 40 MP")
	}
	h, _, _ := photoHTTP(t, false, true)
	if h.photoPixelLimit != 0 {
		t.Fatal("production HTTP unexpectedly sets a custom pixel limit")
	}
	if w := uploadPhoto(h, "s1", "secret", "image/png", data); w.Code != 201 {
		t.Fatal("production default changed", w.Code, w.Body.String())
	}
}
