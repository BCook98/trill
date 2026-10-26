package sidecar

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	previewCookie      = "trill_preview"
	previewTTL         = 30 * time.Minute
	previewLegacyOwner = ":legacy" // Cannot collide with a paired device's hex ID.
	previewPerDevice   = 8
	previewTotal       = 32
	previewConnections = 32
)

// Grants are ephemeral capabilities for exactly one loopback HTTP server. The
// native browser must use a nonpersistent cookie store and inject the returned
// token as HttpOnly; Secure; SameSite=Strict; Path=/ for the companion host.
// No bearer credential or preview token is sent to the development server.
type previewCredentials struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

type previewGrant struct {
	demoSessionID                string // Demo-only upstream selection, fixed by authenticated grant creation.
	id, owner, authority, scheme string
	port                         int
	expires                      time.Time
	hash                         [32]byte
	ctx                          context.Context
	cancel                       context.CancelFunc
	transport                    *http.Transport
	once                         sync.Once
	mu                           sync.Mutex
	connections                  map[*previewConn]struct{}
	pendingDials                 int // Reserved slots; protected by mu with connections.
}

type previewManager struct {
	h               *HTTP
	mu              sync.Mutex
	grants          map[string]*previewGrant
	closed          bool
	done, watchDone chan struct{}
}

func newPreviewManager(h *HTTP) *previewManager {
	m := &previewManager{h: h, grants: make(map[string]*previewGrant), done: make(chan struct{}), watchDone: make(chan struct{})}
	go m.watch()
	return m
}

func previewOwner(r *http.Request) string {
	if id := requestDevice(r); id != "" {
		return id
	}
	return previewLegacyOwner
}

// Parse an authority, never a URL. Do not resolve names or trust forwarding
// headers. The actual listener port is checked too, to defeat a forged Host.
func previewAuthority(r *http.Request) (string, string, int, bool) {
	scheme := "http"
	port := 80
	if r.TLS != nil {
		scheme, port = "https", 443
	}
	authority := strings.ToLower(r.Host)
	if authority == "" || strings.ContainsAny(authority, "@/\\?#% \t\r\n") {
		return "", "", 0, false
	}
	host := authority
	if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = authority[1 : len(authority)-1]
		if net.ParseIP(host) == nil {
			return "", "", 0, false
		}
	} else if strings.Contains(authority, ":") {
		var err error
		var raw string
		host, raw, err = net.SplitHostPort(authority)
		if err != nil {
			return "", "", 0, false
		}
		port, err = strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != raw {
			return "", "", 0, false
		}
	}
	if net.ParseIP(host) == nil {
		if strings.HasPrefix(authority, "[") {
			return "", "", 0, false
		}
		if len(host) > 253 {
			return "", "", 0, false
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", "", 0, false
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return "", "", 0, false
				}
			}
		}
	}
	return authority, scheme, port, true
}

func previewLocalPort(r *http.Request) int {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0
	}
	_, raw, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(raw)
	return port
}

func previewSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Plain HTTP is allowed only on an actual loopback listener, as permitted
	// by trilld's existing --insecure-loopback mode. RemoteAddr alone is not proof.
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	local, _, e1 := net.SplitHostPort(addr.String())
	remote, _, e2 := net.SplitHostPort(r.RemoteAddr)
	return e1 == nil && e2 == nil && net.ParseIP(local).IsLoopback() && net.ParseIP(remote).IsLoopback()
}

func (h *HTTP) createPreview(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	if !previewSecure(r) {
		writeError(w, 403, "preview requires TLS")
		return
	}
	authority, scheme, listener, valid := previewAuthority(r)
	var v struct {
		SessionID string `json:"sessionId"`
		Port      int    `json:"port"`
	}
	if !decodeBody(w, r, &v) {
		return
	}
	if !valid || !idPattern.MatchString(v.SessionID) || v.Port < 1024 || v.Port > 65535 || v.Port == listener || v.Port == previewLocalPort(r) || (h.previewPort != 0 && v.Port != h.previewPort) {
		writeError(w, 400, "invalid preview target")
		return
	}
	session, err := h.service.find(r.Context(), v.SessionID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if session.HiddenFromSidebar || !activeStatus(session.Status) || !session.TerminalLive {
		writeError(w, 409, "session unavailable for preview")
		return
	}
	m := h.previews
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.sweepLocked()
	if m.closed {
		h.fail(w, errClosed)
		return
	}
	owner := previewOwner(r)
	// Revalidate after find's backend I/O, before publishing a capability.
	if !active[owner] {
		writeError(w, 401, "unauthorized")
		return
	}
	count := 0
	for _, g := range m.grants {
		if g.owner == owner {
			count++
		}
	}
	if count >= previewPerDevice || len(m.grants) >= previewTotal {
		writeError(w, 429, "preview capacity reached")
		return
	}
	var secret [32]byte
	var id [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		writeError(w, 503, "preview unavailable")
		return
	}
	if _, err := rand.Read(id[:]); err != nil {
		writeError(w, 503, "preview unavailable")
		return
	}
	token := hex.EncodeToString(secret[:])
	expires := time.Now().UTC().Add(previewTTL)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	g := &previewGrant{id: hex.EncodeToString(id[:]), owner: owner, authority: authority, scheme: scheme, port: v.Port, expires: expires, hash: sha256.Sum256([]byte(token)), ctx: ctx, cancel: cancel, connections: make(map[*previewConn]struct{})}
	if h.service.demo != nil {
		g.demoSessionID = session.ID
	}
	// A dedicated transport never uses environment proxies, DNS, redirects,
	// HTTP/2, or another grant's pooled connections.
	g.transport = &http.Transport{DialContext: g.dial, MaxConnsPerHost: previewConnections, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 1 << 20}
	context.AfterFunc(ctx, g.close)
	m.grants[g.id] = g
	writeJSON(w, 201, previewCredentials{g.id, token, expires.Format(time.RFC3339)})
}

func (h *HTTP) deletePreview(w http.ResponseWriter, r *http.Request) {
	m := h.previews
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.sweepLocked()
	owner := previewOwner(r)
	if !active[owner] {
		writeError(w, 401, "unauthorized")
		return
	}
	g := m.grants[r.PathValue("id")]
	if g == nil || g.owner != owner {
		writeError(w, 404, "not found")
		return
	}
	delete(m.grants, g.id)
	g.close()
	ok(w)
}

// A malformed, duplicate, expired, or revoked preview cookie always fails
// closed, including when an otherwise valid API bearer is also supplied.
func previewCookieToken(r *http.Request) (string, bool) {
	var token string
	found, valid := false, true
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, value, hasValue := strings.Cut(strings.TrimSpace(part), "=")
			if strings.TrimSpace(name) != previewCookie {
				continue
			}
			if found || !hasValue || len(value) != 64 {
				valid = false
			}
			found, token = true, value
			for _, c := range value {
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
					valid = false
				}
			}
		}
	}
	if !valid {
		token = ""
	}
	return token, found
}

func (m *previewManager) serve(w http.ResponseWriter, r *http.Request) bool {
	token, present := previewCookieToken(r)
	if !present {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	m.mu.Lock()
	m.sweepLocked()
	var grant *previewGrant
	hash := sha256.Sum256([]byte(token))
	for _, g := range m.grants {
		if subtle.ConstantTimeCompare(g.hash[:], hash[:]) == 1 && token != "" {
			grant = g
			break
		}
	}
	m.mu.Unlock()
	if grant == nil {
		writeError(w, 401, "unauthorized")
		return true
	}
	authority, scheme, listener, valid := previewAuthority(r)
	if !previewSecure(r) || !valid || authority != grant.authority || scheme != grant.scheme || listener == grant.port || previewLocalPort(r) == grant.port {
		writeError(w, 401, "unauthorized")
		return true
	}
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		writeError(w, 405, "preview method unavailable")
		return true
	}
	upgrade := r.Header.Values("Upgrade")
	if len(upgrade) > 1 || len(upgrade) == 1 && !strings.EqualFold(upgrade[0], "websocket") || len(r.Trailer) != 0 {
		writeError(w, 400, "preview protocol unavailable")
		return true
	}
	// Replace the API/server deadlines with the grant deadline. Cancellation
	// also interrupts blocked frontend reads/writes, including a slow client.
	rc := http.NewResponseController(w)
	deadline, _ := grant.ctx.Deadline()
	for _, err := range []error{rc.SetReadDeadline(deadline), rc.SetWriteDeadline(deadline)} {
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, 502, "preview unavailable")
			return true
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	interrupted := make(chan struct{})
	stop := context.AfterFunc(grant.ctx, func() {
		cancel()
		_ = rc.SetReadDeadline(time.Now())
		_ = rc.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		// A ResponseController cannot be used after ServeHTTP returns.
		if !stop() {
			<-interrupted
		}
		cancel()
	}()
	if grant.ctx.Err() != nil {
		writeError(w, 401, "unauthorized")
		return true
	}
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(grant.port))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme, p.Out.URL.Host, p.Out.URL.User = "http", target, nil
			p.Out.URL.Opaque, p.Out.URL.Fragment = "", ""
			p.Out.URL.RawQuery = p.In.URL.RawQuery
			p.Out.Host = target
			if p.Out.Header.Get("Origin") == grant.scheme+"://"+grant.authority {
				p.Out.Header.Set("Origin", "http://"+target)
			}
			stripPreviewHeaders(p.Out.Header)
			if grant.demoSessionID != "" {
				// Only the demo fixture consumes this non-secret selection. Overwrite
				// client headers after hop-by-hop stripping for every page/asset/upgrade.
				p.Out.Header.Set(demoPreviewSessionHeader, grant.demoSessionID)
			}
			p.Out.Trailer = nil
			p.Out.Header.Del("Cookie")
			for _, c := range p.In.Cookies() {
				if c.Name != previewCookie {
					p.Out.AddCookie(c)
				}
			}
		},
		Transport:     grant.transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0), // Errors must never log capabilities or paths.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			previewUpstreamError(w, r, grant.port, err)
		},
		ModifyResponse: func(res *http.Response) error { return previewResponse(res, grant, target) },
	}
	proxy.ServeHTTP(&previewResponseWriter{ResponseWriter: w, grant: grant, target: target}, r.WithContext(ctx))
	return true
}

// Show a useful document on older clients too; API/assets retain JSON errors.
// Neither response includes the underlying error, requested path, or credentials.
func previewUpstreamError(w http.ResponseWriter, r *http.Request, port int, err error) {
	code := "response-rejected"
	message := "The development server returned a response that cannot be previewed. Check its redirects and reload."
	var networkError net.Error
	if errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		code = "upstream-unavailable"
		message = fmt.Sprintf("Could not reach localhost:%d on your Mac. Check that the development server is running on this port, then reload.", port)
	}
	w.Header().Set("X-Trill-Preview-Error", code)
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		w.WriteHeader(http.StatusBadGateway)
		// message contains only fixed text and a validated numeric port.
		fmt.Fprintf(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark"><title>Preview unavailable</title><style>body{font:17px system-ui;margin:32px;line-height:1.5}main{max-width:32rem;margin:12vh auto}h1{font-size:1.4rem}a{display:inline-block;padding:12px 0}</style></head><body><main><h1>Preview unavailable</h1><p>%s</p><a href="">Reload</a></main></body></html>`, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeError(w, http.StatusBadGateway, message)
}

func stripPreviewHeaders(h http.Header) {
	for k := range h {
		name := strings.ToLower(k)
		if name == "authorization" || name == "proxy-authorization" || name == "forwarded" || strings.HasPrefix(name, "x-forwarded") || name == "x-real-ip" || strings.HasPrefix(name, "x-original-") || name == "x-rewrite-url" || name == "x-http-method-override" {
			delete(h, k)
		}
	}
}

func previewResponse(res *http.Response, g *previewGrant, target string) error {
	// Only the companion may identify its own proxy failures to the native UI.
	res.Header.Del("X-Trill-Preview-Error")
	cookies := res.Header.Values("Set-Cookie")
	res.Header.Del("Set-Cookie")
	for _, raw := range cookies {
		// Filter by raw name too: an invalid reserved cookie must not slip past
		// net/http's cookie parser. Other application cookies are preserved.
		name, _, _ := strings.Cut(raw, "=")
		if strings.TrimSpace(name) != previewCookie {
			res.Header.Add("Set-Cookie", raw)
		}
	}
	res.Header.Del("Alt-Svc")         // Upstream cannot select a companion transport endpoint.
	res.Header.Del("Clear-Site-Data") // Cannot clear the native capability cookie.
	res.Header.Del("Trailer")
	res.Trailer = nil
	if res.Body != nil && res.StatusCode != http.StatusSwitchingProtocols {
		res.Body = &previewResponseBody{ReadCloser: res.Body, response: res}
	}
	res.Header.Set("Cache-Control", "no-store")
	locations := res.Header.Values("Location")
	if len(locations) > 1 {
		return errors.New("unsafe preview redirect")
	}
	for _, raw := range locations {
		if raw != strings.TrimSpace(raw) || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\t") {
			return errors.New("unsafe preview redirect")
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Opaque != "" {
			return errors.New("unsafe preview redirect")
		}
		if u.Host != "" {
			if u.Host == target && (u.Scheme == "" || u.Scheme == "http") {
				u.Host, u.Scheme = g.authority, g.scheme
			} else if u.Host != g.authority || u.Scheme != "" && u.Scheme != g.scheme {
				return errors.New("unsafe preview redirect")
			}
		} else if u.Scheme != "" {
			return errors.New("unsafe preview redirect")
		}
		res.Header.Set("Location", u.String())
	}
	if res.StatusCode == http.StatusSwitchingProtocols {
		// ReverseProxy's rejected-upgrade paths do not all close the transferred
		// body. Reject here so ModifyResponse's error path closes it before any
		// handoff, including an unsolicited 101 on an ordinary HTTP request.
		if res.Request == nil || res.Request.Method != http.MethodGet || !previewWebSocketUpgrade(res.Request.Header) || !previewWebSocketUpgrade(res.Header) {
			return errors.New("unsafe preview upgrade")
		}
	}
	return nil
}

func previewWebSocketUpgrade(header http.Header) bool {
	upgrade := header.Values("Upgrade")
	if len(upgrade) != 1 || !strings.EqualFold(upgrade[0], "websocket") {
		return false
	}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// ReverseProxy forwards informational responses before ModifyResponse runs.
// Filter those headers at the writer too, and preserve controller unwrapping
// for WebSocket hijacks and streaming flushes.
type previewResponseWriter struct {
	http.ResponseWriter
	grant  *previewGrant
	target string
}

func (w *previewResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *previewResponseWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *previewResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		if err := previewResponse(&http.Response{StatusCode: status, Header: w.Header()}, w.grant, w.target); err != nil {
			clear(w.Header())
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

// net/http populates Response.Trailer only on body EOF, after ModifyResponse.
// Never let late headers bypass capability-cookie or redirect filtering.
type previewResponseBody struct {
	io.ReadCloser
	response *http.Response
}

func (b *previewResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.response.Trailer = nil
	}
	return n, err
}
func (b *previewResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.response.Trailer = nil
	return err
}

// Caller holds m.mu. Store errors fail closed, also for existing streams.
func (m *previewManager) sweepLocked() map[string]bool {
	active := map[string]bool{previewLegacyOwner: m.h.legacy}
	if m.h.store != nil {
		devices, err := m.h.store.Devices()
		if err == nil {
			for _, d := range devices {
				active[d.ID] = d.RevokedAt == nil
			}
		}
	}
	select {
	case <-m.h.service.closeDone:
		m.closed = true
	default:
	}
	for id, g := range m.grants {
		if m.closed || g.ctx.Err() != nil || !time.Now().Before(g.expires) || !active[g.owner] {
			delete(m.grants, id)
			g.close()
		}
	}
	return active
}

func (m *previewManager) watch() {
	defer close(m.watchDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-m.h.service.closeDone:
			m.mu.Lock()
			m.closed = true
			m.sweepLocked()
			m.mu.Unlock()
			return
		case <-ticker.C:
			m.mu.Lock()
			if len(m.grants) != 0 {
				m.sweepLocked()
			}
			m.mu.Unlock()
		}
	}
}

func (m *previewManager) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
	}
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	for id, g := range m.grants {
		delete(m.grants, id)
		g.close()
	}
	m.mu.Unlock()
	<-m.watchDone
}

func (g *previewGrant) close() {
	g.once.Do(func() {
		g.cancel()
		g.transport.CloseIdleConnections()
		g.mu.Lock()
		connections := make([]*previewConn, 0, len(g.connections))
		for c := range g.connections {
			connections = append(connections, c)
		}
		g.mu.Unlock()
		for _, c := range connections {
			_ = c.Close()
		}
	})
}

type previewConn struct {
	net.Conn
	grant *previewGrant
}

func (c *previewConn) Close() error {
	err := c.Conn.Close()
	c.grant.mu.Lock()
	delete(c.grant.connections, c)
	c.grant.mu.Unlock()
	return err
}

var errPreviewConnections = errors.New("preview connection capacity reached")

func (g *previewGrant) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return g.dialBounded(ctx, network, addr, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}

func (g *previewGrant) dialBounded(ctx context.Context, network, addr string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if network != "tcp" || addr != net.JoinHostPort("127.0.0.1", strconv.Itoa(g.port)) {
		return nil, errors.New("invalid preview target")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()
	defer cancel()
	g.mu.Lock()
	if g.ctx.Err() != nil {
		g.mu.Unlock()
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return nil, err
	}
	// Transport relinquishes its connection count on a 101 upgrade. Reserve
	// our own slot before dialing, and retain it until the physical socket
	// closes, so concurrent pending dials and WebSockets share one hard cap.
	if g.pendingDials+len(g.connections) >= previewConnections {
		g.mu.Unlock()
		return nil, errPreviewConnections
	}
	g.pendingDials++
	g.mu.Unlock()

	conn, err := dial(ctx, "tcp4", addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pendingDials--
	if err == nil {
		if g.ctx.Err() != nil {
			err = context.Canceled
		} else {
			err = ctx.Err()
		}
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	c := &previewConn{Conn: conn, grant: g}
	g.connections[c] = struct{}{}
	return c, nil
}
