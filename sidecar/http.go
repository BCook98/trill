package sidecar

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"trill/sidecar/pairing"
)

const maxBody = 16 << 10

var (
	leasePattern  = regexp.MustCompile(`^[a-f0-9]{48}$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
)

type HTTP struct {
	service   *Service
	tokenHash [32]byte
	mux       *http.ServeMux
	legacy    bool
	store     *pairing.Store
	mu        sync.Mutex
	owners    map[string]string
	closed    bool
	closeOnce sync.Once
	done      chan struct{}
	watchDone chan struct{}

	previews        *previewManager
	attachmentRoot  string
	attachmentLimit int64 // Optional aggregate quota for demo storage; zero preserves production.
	photoPixelLimit int64 // Optional pre-decode limit; zero preserves the production 40 MP limit.
	previewPort     int   // Demo-only exact target restriction; zero preserves production policy.
}

func NewHTTP(service *Service, token string) *HTTP {
	return NewHTTPWithPairing(service, token, nil)
}

// NewHTTPWithPairing enables device authentication. An empty legacy token disables
// master-token authentication; pairing does not enable session control.
func NewHTTPWithPairing(service *Service, token string, store *pairing.Store) *HTTP {
	h := &HTTP{service: service, tokenHash: sha256.Sum256([]byte(token)), legacy: token != "", store: store, mux: http.NewServeMux(), owners: map[string]string{}, done: make(chan struct{}), watchDone: make(chan struct{})}
	h.previews = newPreviewManager(h)
	h.mux.HandleFunc("POST /v1/previews", h.createPreview)
	h.mux.HandleFunc("DELETE /v1/previews/{id}", h.deletePreview)
	if store != nil {
		h.mux.HandleFunc("POST /v1/pair", h.pair)
		go h.watchDevices()
	} else {
		close(h.watchDone)
	}
	h.registerFiles()
	h.registerStaticFiles()
	h.mux.HandleFunc("GET /v1/health", h.health)
	h.mux.HandleFunc("GET /v1/sessions", h.sessions)
	h.mux.HandleFunc("GET /v1/projects", h.projects)
	h.mux.HandleFunc("POST /v1/sessions", h.create)
	h.mux.HandleFunc("POST /v1/spaces", h.createSpace)
	h.mux.HandleFunc("PATCH /v1/sessions/{id}", h.rename)
	h.mux.HandleFunc("POST /v1/sessions/{id}/stop", h.stop)
	h.mux.HandleFunc("POST /v1/sessions/{id}/resume", h.resume)
	h.mux.HandleFunc("POST /v1/sessions/{id}/archive", h.archive)
	h.mux.HandleFunc("POST /v1/sessions/{id}/mosh", h.mosh)
	h.mux.HandleFunc("POST /v1/sessions/{id}/attachments", h.attachPhoto)
	h.mux.HandleFunc("DELETE /v1/leases/{id}", h.deleteLease)
	return h
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.previews.serve(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// Pairing is deliberately unauthenticated: only a locally issued invitation
	// grants authority. It never creates a pending approval or controls a session.
	if h.store != nil && r.Method == http.MethodPost && r.URL.Path == "/v1/pair" {
		h.mux.ServeHTTP(w, r)
		return
	}
	values := r.Header.Values("Authorization")
	deviceID := ""
	authorized := false
	if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
		token := strings.TrimPrefix(values[0], "Bearer ")
		authorized = h.legacy && subtle.ConstantTimeCompare(h.tokenHash[:], hashToken(token)) == 1
		if !authorized && h.store != nil {
			var err error
			deviceID, err = h.store.Authenticate(token)
			authorized = err == nil
		}
	}
	if !authorized {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, deviceContextKey{}, deviceID)
	h.mux.ServeHTTP(w, r.WithContext(ctx))
}

func hashToken(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func ok(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

func (h *HTTP) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, 404, "not found")
	case errors.Is(err, errUnsupported):
		writeError(w, 409, "operation unavailable")
	case errors.Is(err, errTerminalBusy):
		writeError(w, 409, errTerminalBusy.Error())
	case errors.Is(err, errNotAttachable):
		writeError(w, 409, "session unavailable for attachment")
	case errors.Is(err, errCapacity):
		writeError(w, 429, "mosh capacity reached")
	case errors.Is(err, errOutcomeUnknown):
		writeError(w, 409, "operation outcome unconfirmed; refresh sessions before retrying")
	case errors.Is(err, errClosed):
		writeError(w, 503, "sidecar is shutting down")
	default:
		writeError(w, 502, "local backend unavailable")
	}
}

func (h *HTTP) control(w http.ResponseWriter) bool {
	if !h.service.config.AllowControl {
		writeError(w, 403, "control disabled")
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, 415, "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON body")
		return false
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		writeError(w, 400, "invalid JSON body")
		return false
	}
	return true
}

func validText(s string, max int) bool {
	return s != "" && utf8.ValidString(s) && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}

func validGoal(s string) bool {
	if len(s) > 8000 || !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r == 0 || r == 127 || (r < 32 && r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	return true
}

func validBranch(s string) bool {
	if len(s) > 200 || !branchPattern.MatchString(s) || strings.Contains(s, "..") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func optionalCreateText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var value string
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func (h *HTTP) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "trilld", "version": "1", "readOnly": !h.service.config.AllowControl, "sessionManagement": h.service.sessionManagement(), "backend": h.service.backendName(), "terminalLab": h.service.config.TmuxSocket != "" || h.service.config.HerdrLabRoot != ""})
}
func (h *HTTP) sessions(w http.ResponseWriter, r *http.Request) {
	c, err := h.service.catalog(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": c.sessions(), "providers": c.Providers})
}
func (h *HTTP) projects(w http.ResponseWriter, r *http.Request) {
	c, err := h.service.catalog(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"projects": c.Projects, "providers": c.Providers})
}
func (h *HTTP) create(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	var v struct {
		ProjectID   string          `json:"projectId"`
		Name        json.RawMessage `json:"name"`
		Goal        json.RawMessage `json:"goal"`
		Kind        json.RawMessage `json:"kind"`
		UseWorktree json.RawMessage `json:"useWorktree"`
		Branch      json.RawMessage `json:"branch"`
	}
	if !decodeBody(w, r, &v) {
		return
	}
	kind, kindOK := optionalCreateText(v.Kind)
	name, nameOK := optionalCreateText(v.Name)
	goal, goalOK := optionalCreateText(v.Goal)
	branch, branchOK := optionalCreateText(v.Branch)
	options := CreateOptions{Kind: kind, UseWorktree: true, Branch: branch}
	if len(v.Kind) == 0 {
		options.Kind = "agent"
	}
	if len(v.UseWorktree) != 0 {
		switch string(v.UseWorktree) {
		case "true":
		case "false":
			options.UseWorktree = false
		default:
			writeError(w, 400, "useWorktree must be a boolean")
			return
		}
	}
	valid := idPattern.MatchString(v.ProjectID) && kindOK && nameOK && goalOK && branchOK
	switch options.Kind {
	case "agent":
		valid = valid && (name == "" || validText(name, 200)) && validGoal(goal) && (v.Branch == nil || (options.UseWorktree && validBranch(options.Branch)))
	case "worktree":
		valid = valid && options.UseWorktree && validBranch(branch) && (name == "" || validText(name, 200)) && v.Goal == nil
	case "terminal":
		valid = valid && !options.UseWorktree && v.Name == nil && v.Goal == nil && v.Branch == nil
	default:
		valid = false
	}
	if !valid {
		writeError(w, 400, "invalid session fields")
		return
	}
	if err := h.service.Create(r.Context(), v.ProjectID, name, goal, options); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
func (h *HTTP) rename(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	var v struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &v) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) || !validText(v.Name, 200) {
		writeError(w, 400, "invalid session fields")
		return
	}
	if err := h.service.Rename(r.Context(), r.PathValue("id"), v.Name); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
func (h *HTTP) stop(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, 400, "invalid session ID")
		return
	}
	if err := h.service.Stop(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
func (h *HTTP) resume(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, 400, "invalid session ID")
		return
	}
	if err := h.service.Resume(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
func (h *HTTP) archive(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, 400, "invalid session ID")
		return
	}
	if err := h.service.Archive(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
func (h *HTTP) mosh(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	var v struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if !decodeBody(w, r, &v) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, 400, "invalid session ID")
		return
	}
	if v.Cols < 20 || v.Cols > 500 || v.Rows < 5 || v.Rows > 200 {
		writeError(w, 400, "invalid terminal dimensions")
		return
	}
	info, err := h.service.bootstrapDevice(r.Context(), r.PathValue("id"), v.Cols, v.Rows, requestDevice(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	if deviceID := requestDevice(r); deviceID != "" {
		// Recheck after the potentially slow bootstrap, before publishing credentials.
		h.mu.Lock()
		values := r.Header.Values("Authorization")
		_, authErr := h.store.Authenticate(strings.TrimPrefix(values[0], "Bearer "))
		h.service.mu.Lock()
		_, live := h.service.leases[info.LeaseID]
		serviceClosed := h.service.closed
		h.service.mu.Unlock()
		if authErr != nil || h.closed || serviceClosed || !live {
			h.mu.Unlock()
			h.service.DeleteLease(info.LeaseID)
			if authErr != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
			} else {
				h.fail(w, errClosed)
			}
			return
		}
		h.owners[info.LeaseID] = deviceID
		h.mu.Unlock()
	}
	writeJSON(w, 200, info)
}
func (h *HTTP) deleteLease(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	if !leasePattern.MatchString(r.PathValue("id")) {
		writeError(w, 404, "not found")
		return
	}
	h.mu.Lock()
	id := r.PathValue("id")
	if deviceID := requestDevice(r); deviceID != "" && h.owners[id] != deviceID {
		h.mu.Unlock()
		writeError(w, 404, "not found")
		return
	}
	delete(h.owners, id)
	h.mu.Unlock()
	if !h.service.DeleteLease(id) {
		writeError(w, 404, "not found")
		return
	}
	ok(w)
}

type deviceContextKey struct{}

func requestDevice(r *http.Request) string {
	id, _ := r.Context().Value(deviceContextKey{}).(string)
	return id
}

func (h *HTTP) pair(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &v) {
		return
	}
	if !pairing.ValidCode(v.Code) || !pairing.ValidName(v.Name) {
		writeError(w, 400, "invalid pairing fields")
		return
	}
	d, token, err := h.store.Redeem(v.Code, v.Name)
	switch {
	case errors.Is(err, pairing.ErrInvitation):
		writeError(w, 403, "invalid or expired invitation")
	case errors.Is(err, pairing.ErrFields):
		writeError(w, 400, "invalid pairing fields")
	case err != nil:
		writeError(w, 503, "pairing unavailable")
	default:
		writeJSON(w, 200, map[string]string{"deviceId": d.ID, "token": token})
	}
}

// Device revocation is checked on every API request. Polling the shared state
// also closes idle mosh leases after revocation by a separate local CLI process.
// No credential cache is used for authentication.
func (h *HTTP) watchDevices() {
	defer close(h.watchDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-h.service.closeDone:
			h.mu.Lock()
			clear(h.owners)
			h.mu.Unlock()
			return
		case <-ticker.C:
			h.sweepDevices()
		}
	}
}
func (h *HTTP) sweepDevices() {
	h.mu.Lock()
	// Prune expired, naturally exited, deleted, or shutdown leases. The Service
	// remains the sole owner of process lifetime and terminal semantics.
	h.service.mu.Lock()
	for id := range h.owners {
		if _, ok := h.service.leases[id]; !ok {
			delete(h.owners, id)
		}
	}
	h.service.mu.Unlock()
	if len(h.owners) == 0 {
		h.mu.Unlock()
		return
	}
	devices, err := h.store.Devices()
	active := make(map[string]bool, len(devices))
	if err == nil {
		for _, d := range devices {
			active[d.ID] = d.RevokedAt == nil
		}
	}
	// Unreadable state fails closed for paired leases, leaving legacy leases alone.
	ids := make([]string, 0)
	for id, owner := range h.owners {
		if !active[owner] {
			delete(h.owners, id)
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.service.DeleteLease(id)
	}
}

// Close stops the watcher and drains paired leases. Service.Close still drains
// every lease and independently terminates the watcher, for existing callers.
func (h *HTTP) Close() {
	h.closeOnce.Do(func() {
		h.previews.Close()
		h.mu.Lock()
		h.closed = true
		close(h.done)
		ids := make([]string, 0, len(h.owners))
		for id := range h.owners {
			ids = append(ids, id)
			delete(h.owners, id)
		}
		h.mu.Unlock()
		<-h.watchDone
		for _, id := range ids {
			h.service.DeleteLease(id)
		}
	})
}

func (h *HTTP) createSpace(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	var fields struct {
		ProviderID string `json:"providerId"`
		Name       string `json:"name"`
		Directory  string `json:"directory"`
	}
	if !decodeBody(w, r, &fields) {
		return
	}
	if !validText(fields.ProviderID, 200) || !validText(fields.Name, 200) || !filepath.IsAbs(fields.Directory) || !validText(fields.Directory, 4096) {
		writeError(w, 400, "invalid space fields")
		return
	}
	if err := h.service.CreateSpace(r.Context(), fields.ProviderID, fields.Name, fields.Directory); err != nil {
		h.fail(w, err)
		return
	}
	ok(w)
}
