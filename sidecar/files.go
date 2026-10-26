package sidecar

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const (
	maxFileEntries  = 1000
	maxTextPreview  = 2 << 20
	maxImagePreview = 10 << 20
)

type fileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

type fileDirectory struct {
	Path      string      `json:"path"`
	Entries   []fileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// The HTTP constructor calls registerFiles. All routes use the same mux,
// behind ServeHTTP's authentication and no-store policy.
func (h *HTTP) registerFiles() {
	for _, resource := range []string{"sessions", "projects"} {
		project := resource == "projects"
		h.mux.HandleFunc("GET /v1/"+resource+"/{id}/files", func(w http.ResponseWriter, r *http.Request) {
			h.serveFiles(w, r, project, false)
		})
		h.mux.HandleFunc("GET /v1/"+resource+"/{id}/file", func(w http.ResponseWriter, r *http.Request) {
			h.serveFiles(w, r, project, true)
		})
		h.mux.HandleFunc("GET /v1/"+resource+"/{id}/video", func(w http.ResponseWriter, r *http.Request) {
			h.serveVideo(w, r, project)
		})
	}
	h.mux.HandleFunc("GET /v1/sessions/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		h.serveAttachmentFiles(w, r, false)
	})
	h.mux.HandleFunc("GET /v1/sessions/{id}/attachment", func(w http.ResponseWriter, r *http.Request) {
		h.serveAttachmentFiles(w, r, true)
	})
}

// Paths are canonical relative URL paths. Never clean away traversal or accept
// a client-supplied root, including an unknown or duplicate query parameter.
func fileRequestPath(r *http.Request, preview bool) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	for key, values := range query {
		if key != "path" || len(values) != 1 {
			return "", false
		}
	}
	name := query.Get("path")
	return name, (!preview || name != "") && validFilePath(name)
}

func validFilePath(name string) bool {
	if name == "" {
		return true
	}
	if !utf8.ValidString(name) || strings.Contains(name, "\\") || !fs.ValidPath(name) || name == "." {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func (h *HTTP) filesCheckOpen() error {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	h.service.mu.Lock()
	closed = closed || h.service.closed
	h.service.mu.Unlock()
	if closed {
		return errClosed
	}
	return nil
}

// Production roots come only from metadata. Lab browsing requires an explicit
// configured root, never the socket parent. An archived session may still have
// an existing worktree; browsing never attaches to or resumes it.
func (h *HTTP) filesRoot(ctx context.Context, id string, project bool) (string, error) {
	if err := h.filesCheckOpen(); err != nil {
		return "", err
	}
	if h.service.config.TmuxSocket != "" && h.service.config.LabFilesRoot == "" {
		return "", errUnsupported
	}
	projectID := id
	if !project {
		session, err := h.service.find(ctx, id)
		if err != nil {
			return "", err
		}
		if h.service.config.TmuxSocket != "" {
			return h.service.config.LabFilesRoot, nil
		}
		if session.WorktreePath != "" {
			if !filepath.IsAbs(session.WorktreePath) {
				return "", errUnavailable
			}
			return session.WorktreePath, nil
		}
		projectID = session.ProjectID
	}
	projects, err := h.service.Projects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.ID != projectID {
			continue
		}
		if h.service.config.TmuxSocket != "" {
			return h.service.config.LabFilesRoot, nil
		}
		if !filepath.IsAbs(p.Path) {
			return "", errUnavailable
		}
		return p.Path, nil
	}
	return "", errNotFound
}

func (h *HTTP) serveFiles(w http.ResponseWriter, r *http.Request, project, preview bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, http.StatusBadRequest, "invalid resource ID")
		return
	}
	name, valid := fileRequestPath(r, preview)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid relative path")
		return
	}
	rootPath, err := h.filesRoot(r.Context(), r.PathValue("id"), project)
	if err != nil {
		h.fail(w, err)
		return
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		filesReadError(w, err)
		return
	}
	defer root.Close()
	if name == "" {
		name = "."
	}
	if preview {
		serveFilePreview(w, root, name, false)
		return
	}
	directory, err := readFileDirectory(r.Context(), root, name)
	if err != nil {
		filesReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directory)
}

var errFileKind = errors.New("unsupported file kind")

func filesReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "file or directory not found")
	case errors.Is(err, errFileKind):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported file kind")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "file read unavailable")
	default:
		// Do not serialize PathError: it may contain an absolute root or link target.
		writeError(w, http.StatusForbidden, "path inaccessible or outside root")
	}
}

// Check before opening to avoid ordinary device opens; O_NONBLOCK also prevents
// a file swapped for a FIFO from hanging. Recheck the opened descriptor before
// reading so no special file can be read through a stat/open race.
func openExplorerFile(root *os.Root, name string, directory bool) (*os.File, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	matches := func(info fs.FileInfo) bool {
		if directory {
			return info.IsDir()
		}
		return info.Mode().IsRegular()
	}
	if !matches(info) {
		return nil, errFileKind
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err == nil && !matches(actual) {
		err = errFileKind
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func fileEntryLess(a, b fileEntry) bool {
	if (a.Kind == "directory") != (b.Kind == "directory") {
		return a.Kind == "directory"
	}
	return a.Name < b.Name
}

func readFileDirectory(ctx context.Context, root *os.Root, name string) (fileDirectory, error) {
	result := fileDirectory{Path: name, Entries: []fileEntry{}}
	if name == "." {
		result.Path = ""
	}
	file, err := openExplorerFile(root, name, true)
	if err != nil {
		return result, err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		batch, readErr := file.ReadDir(128)
		for _, entry := range batch {
			child := path.Join(result.Path, entry.Name())
			if !validFilePath(child) {
				continue
			}
			v := fileEntry{Name: entry.Name(), Path: child, Kind: "unsupported"}
			// Stat is confined even for links: escaping/dangling links stay unsupported.
			info, err := root.Stat(child)
			if err == nil {
				switch {
				case info.IsDir():
					v.Kind = "directory"
				case info.Mode().IsRegular():
					v.Kind = "file"
				}
				if v.Kind != "unsupported" {
					v.Size = info.Size()
				}
			}
			result.Entries = append(result.Entries, v)
		}
		// Retain the globally first 1000 entries, not whichever the filesystem
		// returned first. Memory stays bounded even for very large directories.
		sort.Slice(result.Entries, func(i, j int) bool { return fileEntryLess(result.Entries[i], result.Entries[j]) })
		if len(result.Entries) > maxFileEntries {
			result.Truncated = true
			result.Entries = result.Entries[:maxFileEntries]
		}
		if readErr == io.EOF {
			return result, nil
		}
		if readErr != nil {
			return result, readErr
		}
	}
}

var (
	errPreviewSize   = errors.New("preview exceeds size limit")
	errPreviewBinary = errors.New("unsupported binary preview")
)

func readFilePreview(reader io.Reader) ([]byte, string, error) {
	buffered := bufio.NewReaderSize(reader, 512)
	header, err := buffered.Peek(512)
	if err != nil && err != io.EOF {
		return nil, "", err
	}
	contentType := http.DetectContentType(header)
	limit := int64(maxImagePreview)
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		if !strings.HasPrefix(contentType, "text/") {
			return nil, "", errPreviewBinary
		}
		contentType = "text/plain; charset=utf-8"
		limit = maxTextPreview
	}
	data, err := io.ReadAll(io.LimitReader(buffered, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", errPreviewSize
	}
	if contentType == "text/plain; charset=utf-8" {
		if !utf8.Valid(data) {
			return nil, "", errPreviewBinary
		}
		for _, c := range string(data) {
			if unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' {
				return nil, "", errPreviewBinary
			}
		}
	}
	return data, contentType, nil
}

func serveFilePreview(w http.ResponseWriter, root *os.Root, name string, imageOnly bool) {
	file, err := openExplorerFile(root, name, false)
	if err != nil {
		filesReadError(w, err)
		return
	}
	defer file.Close()
	// The bounded reader, rather than a pre-open size, is authoritative if a
	// regular file grows or shrinks while being read. Buffer before writing 200.
	data, contentType, err := readFilePreview(file)
	if err == nil && imageOnly && !strings.HasPrefix(contentType, "image/") {
		err = errPreviewBinary
	}
	switch {
	case errors.Is(err, errPreviewSize):
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds preview size limit")
	case errors.Is(err, errPreviewBinary):
		writeError(w, http.StatusUnsupportedMediaType, "binary preview unsupported")
	case err != nil:
		filesReadError(w, err)
	default:
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

// Attachment roots are independent of worktrees and remain readable for archived
// sessions. Opening the per-session directory from a parent Root, then comparing
// its descriptor with Lstat, prevents a link to another session's photo directory.
func (h *HTTP) openAttachmentFiles(id string) (*os.Root, error) {
	base := h.attachmentRoot
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "share", "trill", "attachments")
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := attachmentSessionDir(id)
	expected, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, errFileKind
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err == nil && !os.SameFile(expected, actual) {
		err = fs.ErrPermission
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func (h *HTTP) serveAttachmentFiles(w http.ResponseWriter, r *http.Request, preview bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.control(w) {
		return
	}
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid session ID")
		return
	}
	name, valid := fileRequestPath(r, preview)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid relative path")
		return
	}
	if err := h.filesCheckOpen(); err != nil {
		h.fail(w, err)
		return
	}
	if _, err := h.service.find(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	root, err := h.openAttachmentFiles(id)
	if err != nil {
		if !preview && errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, fileDirectory{Path: name, Entries: []fileEntry{}})
		} else {
			filesReadError(w, err)
		}
		return
	}
	defer root.Close()
	if preview {
		serveFilePreview(w, root, name, true)
		return
	}
	if name == "" {
		name = "."
	}
	directory, err := readFileDirectory(r.Context(), root, name)
	if err != nil {
		filesReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directory)
}
