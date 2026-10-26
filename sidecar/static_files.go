package sidecar

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"syscall"
)

func (h *HTTP) registerStaticFiles() {
	h.mux.HandleFunc("GET /v1/sessions/{id}/static-file", h.serveStaticFile)
}

// Both names are relative to descriptor-backed roots. Never clean a pathname:
// traversal, hidden components and ambiguous encodings must fail closed.
func validStaticPath(name string, empty bool) bool {
	if name == "" {
		return empty
	}
	if !validFilePath(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") {
			return false
		}
	}
	return true
}

func staticAssetAllowed(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".css", ".js", ".mjs", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func staticFileQuery(r *http.Request) (folder, asset string, valid bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", false
	}
	for key, values := range query {
		if (key != "root" && key != "path") || len(values) != 1 {
			return "", "", false
		}
	}
	folder, asset = query.Get("root"), query.Get("path")
	return folder, asset, validStaticPath(folder, true) && validStaticPath(asset, false)
}

// Lstat rejects a symlink at the authoritative root itself. Its absolute
// ancestors come from server metadata, never from the request.
func openStaticProjectRoot(name string) (*os.Root, error) {
	expected, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fs.ErrPermission
	}
	root, err := os.OpenRoot(name)
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

// Each step is one component relative to an already-open parent. OpenRoot may
// follow a raced-in link, but SameFile must still match the directory observed
// by Lstat; a link cannot redirect the read to another directory.
func openStaticDirectory(parent *os.Root, name string) (*os.Root, error) {
	expected, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fs.ErrPermission
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := child.Stat(".")
	if err == nil && !os.SameFile(expected, actual) {
		err = fs.ErrPermission
	}
	if err != nil {
		child.Close()
		return nil, err
	}
	return child, nil
}

func openStaticAsset(project, folder, asset string) (*os.File, error) {
	if !validStaticPath(folder, true) || !validStaticPath(asset, false) || !staticAssetAllowed(asset) {
		return nil, fs.ErrPermission
	}
	root, err := openStaticProjectRoot(project)
	if err != nil {
		return nil, err
	}
	// Close the final descriptor even as ownership moves down the tree.
	defer func() { root.Close() }()
	components := strings.Split(asset, "/")
	directories := components[:len(components)-1]
	if folder != "" {
		directories = append(strings.Split(folder, "/"), directories...)
	}
	for _, component := range directories {
		child, err := openStaticDirectory(root, component)
		if err != nil {
			return nil, err
		}
		root.Close()
		root = child
	}
	leaf := components[len(components)-1]
	expected, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if expected.Mode()&os.ModeSymlink != 0 {
		return nil, fs.ErrPermission
	}
	if !expected.Mode().IsRegular() {
		return nil, errFileKind
	}
	file, err := root.OpenFile(leaf, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err == nil && (!actual.Mode().IsRegular() || !os.SameFile(expected, actual)) {
		err = fs.ErrPermission
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (h *HTTP) serveStaticFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, http.StatusBadRequest, "invalid resource ID")
		return
	}
	folder, asset, valid := staticFileQuery(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid static preview path")
		return
	}
	if !staticAssetAllowed(asset) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported static asset type")
		return
	}
	project, err := h.filesRoot(r.Context(), r.PathValue("id"), false)
	if err != nil {
		h.fail(w, err)
		return
	}
	file, err := openStaticAsset(project, folder, asset)
	if err != nil {
		filesReadError(w, err)
		return
	}
	defer file.Close()
	// Keep the existing bounded text/image contract. Native WebKit code supplies
	// the allowlisted presentation MIME; this route never serves active HTML.
	data, contentType, err := readFilePreview(file)
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
