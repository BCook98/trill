package sidecar

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed demo/assets/sample.mp4
var demoAssets embed.FS

// The loopback fixture trusts this header only because the demo proxy overwrites
// it from its authenticated grant. It is not an authentication credential.
const demoPreviewSessionHeader = "X-Trill-Demo-Session"

const demoHelp = `Demo commands:
  help                         Show this list
  ls [folder]                  Browse the sample project
  cat README.md                Read a sample file
  demo heading <text>          Update web/index.html and the live preview
  demo task                    Run a short simulated task
  demo photo '<uploaded-path>'  Acknowledge your session's uploaded photo
  demo links                   Show local preview and external links
  demo keys                    Explain keyboard diagnostics
  clear                        Clear this session's transcript
This is a scripted demo, with no shell, coding agent, or network commands.
`

func (d *Demo) ensureFixtures() error {
	for _, p := range d.state.Projects {
		if err := d.fixture("fixtures/" + p.ID); err != nil {
			return err
		}
	}
	for _, v := range d.state.Sessions {
		if err := d.fixture(v.Directory); err != nil {
			return err
		}
	}
	if err := d.renderHeadings(); err != nil {
		return err
	}
	return d.persist()
}
func (d *Demo) fixture(directory string) error {
	for _, suffix := range []string{"", "/web", "/web/assets", "/web/media", "/src", "/media", "/nested/examples"} {
		if err := d.privateDir(directory + suffix); err != nil {
			return err
		}
	}
	if _, ok := d.state.Headings[directory]; !ok {
		d.state.Headings[directory] = "A little progress, anywhere."
	}
	files := map[string][]byte{
		"README.md":                 []byte("# Trill Demo\n\nA synthetic workspace for testing real Trill connections.\n\nType `demo heading Hello from my phone` to change web/index.html, then open\nits static preview or the live browser preview. Try `demo task` and upload a photo.\n\nFiles, terminal state and preview changes survive reconnects and server restarts.\nNo shell or AI agent runs here.\n\nUnicode: café, 日本語, 👋\n"),
		"src/main.swift":            []byte("import SwiftUI\n\nstruct Welcome: View {\n    var body: some View {\n        Text(\"Hello from Trill 👋\")\n    }\n}\n"),
		"nested/examples/notes.txt": []byte("A nested UTF-8 fixture.\nRésumé · こんにちは · 🚀\n"),
		"unsupported.bin":           {0, 1, 2, 3, 0, 255},
		"web/assets/style.css":      []byte("html{color-scheme:light dark}body{font:18px system-ui;line-height:1.5;margin:0;padding:32px}main{max-width:44rem;margin:auto}h1{font-size:clamp(2rem,8vw,4rem);line-height:1.1}nav{display:flex;gap:24px}img,video{max-width:100%;border-radius:18px}a{color:#747aff}.badge{font-size:14px;opacity:.7}"),
		"web/assets/mark.svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 40"><path stroke="#747aff" stroke-width="6" fill="none" d="M4 20l10-12 12 24L38 8l12 24L62 8l12 24 12-12"/></svg>`),
		"web/assets/live.js":        []byte(`(()=>{const q=new URLSearchParams(location.search);let ws;function connect(){const u=new URL('/ws',location.href);u.protocol=location.protocol==='https:'?'wss:':'ws:';u.search=q.toString();ws=new WebSocket(u);ws.onmessage=e=>{const v=JSON.parse(e.data);document.querySelector('h1').textContent=v.heading;document.querySelector('#revision').textContent='Live revision '+v.revision};ws.onclose=()=>setTimeout(connect,1500)}connect();document.querySelector('#count')?.addEventListener('click',e=>{e.target.textContent='Tapped '+(++window.demoCount)+' times'});window.demoCount=0})();`),
	}
	video, err := demoAssets.ReadFile("demo/assets/sample.mp4")
	if err != nil {
		return err
	}
	files["media/sample.mp4"] = video
	files["web/media/sample.mp4"] = video
	img := image.NewRGBA(image.Rect(0, 0, 240, 144))
	for y := 0; y < 144; y++ {
		for x := 0; x < 240; x++ {
			c := color.RGBA{91, 99, 211, 255}
			if (x/24+y/24)%2 == 0 {
				c = color.RGBA{173, 191, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return err
	}
	files["media/sample.png"] = out.Bytes()
	files["web/media/sample.png"] = out.Bytes()
	for name, data := range files {
		if err := d.atomicWrite(directory+"/"+name, data); err != nil {
			return err
		}
	}
	return d.atomicWrite(directory+"/web/index.html", d.html(d.state.Headings[directory], "", 0, ""))
}
func (d *Demo) renderHeadings() error {
	for directory, heading := range d.state.Headings {
		if err := d.atomicWrite(directory+"/web/index.html", d.html(heading, "", d.state.Revision, "")); err != nil {
			return err
		}
	}
	return nil
}

var demoPage = template.Must(template.New("demo").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Trill Demo</title><link rel="stylesheet" href="assets/style.css"></head><body><main><p class="badge">Trill Demo · synthetic sample project</p><img width="120" height="40" src="assets/mark.svg" alt="Trill wave"><h1>{{.Heading}}</h1><p id="revision">Live revision {{.Revision}}</p><nav><a href="./?{{.Query}}">Home</a><a href="about?{{.Query}}">About</a><a href="gallery?{{.Query}}">Gallery</a></nav><p>{{.Route}}</p><button id="count">Tap to interact</button><p>Change this heading from your terminal with <code>demo heading Hello from my phone</code>.</p><p><img src="media/sample.png" alt="Generated purple checkerboard"></p><video controls muted playsinline src="media/sample.mp4"></video><script src="assets/live.js"></script></main></body></html>`))

func (d *Demo) html(heading, id string, revision uint64, route string) []byte {
	var b bytes.Buffer
	// Session IDs are validated opaque ASCII; template also escapes every field.
	_ = demoPage.Execute(&b, struct {
		Heading, Query, Route string
		Revision              uint64
	}{heading, "session=" + id + "&source=demo", route, revision})
	return b.Bytes()
}

func (d *Demo) Command(id, line string) (DemoSnapshot, error) {
	if len(line) > demoMaxInput || !utf8.ValidString(line) {
		return DemoSnapshot{}, errUnsupported
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.session(id)
	if err != nil {
		return DemoSnapshot{}, err
	}
	if !d.allowControl || !v.Screen.Live || v.Session.HiddenFromSidebar {
		return DemoSnapshot{}, errUnsupported
	}
	before := d.copyState()
	// Only printable text/newlines/tabs can enter the transcript. Input is never
	// parsed by a shell; quoted photos are the sole specially parsed argument.
	clean := demoSafeText(line)
	command := strings.TrimSpace(clean)
	if command == "clear" {
		v.Screen.Text = ""
		v.Screen.Revision++
	} else {
		d.append(v, "› "+clean+"\n")
		switch {
		case command == "help" || command == "":
			d.append(v, demoHelp)
		case command == "pwd":
			d.append(v, "/demo/"+v.Session.ProjectID+"\n")
		case command == "ls" || strings.HasPrefix(command, "ls "):
			d.append(v, d.list(v.Directory, strings.TrimSpace(strings.TrimPrefix(command, "ls"))))
		case strings.HasPrefix(command, "cat "):
			d.append(v, d.cat(v.Directory, strings.TrimSpace(strings.TrimPrefix(command, "cat "))))
		case strings.HasPrefix(command, "demo heading "):
			heading := strings.TrimSpace(strings.TrimPrefix(command, "demo heading "))
			if !validText(heading, 200) {
				d.append(v, "Use a heading of 1–200 bytes on one line.\n")
			} else {
				d.state.Headings[v.Directory] = heading
				d.append(v, "Updated web/index.html. Open its preview to see the change.\n")
			}
		case command == "demo task":
			if !v.TaskDue.IsZero() {
				d.append(v, "A demo task is already running.\n")
			} else {
				v.TaskPhase = 0
				v.TaskDue = time.Now().Add(500 * time.Millisecond)
				v.Session.ActivityState = "working"
				d.append(v, "Starting sample task…\n")
			}
		case strings.HasPrefix(command, "demo photo "):
			name := strings.TrimSpace(strings.TrimPrefix(command, "demo photo "))
			if len(name) >= 2 && ((name[0] == '\'' && name[len(name)-1] == '\'') || (name[0] == '"' && name[len(name)-1] == '"')) {
				name = name[1 : len(name)-1]
			}
			if d.ownPhoto(id, name) {
				d.append(v, "Photo received. Find it in this session’s Photos.\n")
			} else {
				d.append(v, "Choose a photo uploaded to this session. Other paths are unavailable.\n")
			}
		case command == "demo links":
			port := d.previewPortLocked()
			d.append(v, fmt.Sprintf("Live demo: http://localhost:%d/?session=%s\nExternal link: https://github.com/BCook98/trill\n", port, id))
		case command == "demo keys":
			d.append(v, "Keyboard diagnostics are handled by the demo terminal. Use its diagnostic mode to inspect keys and dimensions.\n")
		default:
			d.append(v, "Unknown demo command. Type help for supported commands.\n")
		}
	}
	v.Session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := d.commit(before); err != nil {
		return DemoSnapshot{}, err
	}
	return v.Screen, nil
}
func (d *Demo) previewPortLocked() int {
	if d.previewPort != 0 {
		return d.previewPort
	}
	return 3000
}
func (d *Demo) list(directory, name string) string {
	if name == "" {
		name = "."
	} else if !validFilePath(name) {
		return "Use a relative sample folder.\n"
	}
	root, err := d.root.OpenRoot(directory)
	if err != nil {
		return "Sample folder unavailable.\n"
	}
	defer root.Close()
	file, err := openExplorerFile(root, name, true)
	if err != nil {
		return "Sample folder unavailable.\n"
	}
	defer file.Close()
	entries, err := file.ReadDir(100)
	if err != nil && err != io.EOF {
		return "Sample folder unavailable.\n"
	}
	names := []string{}
	for _, entry := range entries {
		n := entry.Name()
		if entry.IsDir() {
			n += "/"
		}
		names = append(names, demoSafeText(n))
	}
	sort.Strings(names)
	return strings.Join(names, "  ") + "\n"
}
func (d *Demo) cat(directory, name string) string {
	if !validFilePath(name) || name == "" {
		return "Use a relative sample filename.\n"
	}
	root, err := d.root.OpenRoot(directory)
	if err != nil {
		return "Sample file unavailable.\n"
	}
	defer root.Close()
	file, err := openExplorerFile(root, name, false)
	if err != nil {
		return "Sample file unavailable.\n"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "Use the file viewer for binary or large files.\n"
	}
	return demoSafeText(string(data)) + "\n"
}
func (d *Demo) ownPhoto(id, name string) bool {
	parent := filepath.Join(d.rootPath, "attachments", attachmentSessionDir(id))
	if filepath.Dir(name) != parent || filepath.Clean(name) != name || (!strings.HasPrefix(filepath.Base(name), "photo-")) {
		return false
	}
	ext := filepath.Ext(name)
	if ext != ".jpg" && ext != ".png" {
		return false
	}
	rel, err := filepath.Rel(d.rootPath, name)
	if err != nil {
		return false
	}
	info, err := d.root.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPhotoBytes {
		return false
	}
	// Reject a redirected session directory, including an in-root symlink to
	// another session. Only production storePhoto creates accepted files.
	dirInfo, err := d.root.Lstat(filepath.Dir(rel))
	return err == nil && dirInfo.IsDir()
}

// PreviewHandler is the fixture upstream only. Bind it to its dedicated private
// loopback port; NewDemoHTTP's normal grant/proxy is the public access path.
func (d *Demo) PreviewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method unavailable", 405)
			return
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			http.Error(w, "demo unavailable", 503)
			return
		}
		id := r.Header.Get(demoPreviewSessionHeader)
		// Private operator access retains its explicit fixture selector.
		if id == "" {
			id = r.URL.Query().Get("session")
		}
		if id == "" {
			for _, v := range d.state.Sessions {
				if v.Screen.Live && !v.Session.HiddenFromSidebar {
					id = v.Session.ID
					break
				}
			}
		}
		v, err := d.session(id)
		if err != nil {
			d.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		directory, heading, revision := v.Directory, d.state.Headings[v.Directory], d.state.Revision
		d.mu.Unlock()
		if r.URL.Path == "/ws" {
			d.serveDemoWS(w, r, id)
			return
		}
		switch r.URL.Path {
		case "/", "/about", "/gallery":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(d.html(heading, id, revision, strings.TrimPrefix(r.URL.Path, "/")))
			return
		case "/state":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"heading": heading, "revision": revision})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if !validFilePath(name) || (!strings.HasPrefix(name, "assets/") && !strings.HasPrefix(name, "media/")) {
			http.NotFound(w, r)
			return
		}
		relative := path.Join(directory, "web", name)
		if strings.HasPrefix(name, "media/") {
			relative = path.Join(directory, name)
		}
		root, err := d.openPreviewRoot()
		if err != nil {
			http.Error(w, "demo unavailable", 503)
			return
		}
		defer root.Close()
		f, err := openExplorerFile(root, relative, false)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.Size() > 2<<20 {
			http.Error(w, "fixture too large", 413)
			return
		}
		http.ServeContent(w, r, filepath.Base(name), time.Time{}, f)
	})
}
func (d *Demo) openPreviewRoot() (*os.Root, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errClosed
	}
	return d.root.OpenRoot(".")
}
