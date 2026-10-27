package demoapp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"trill/sidecar/setup"
)

// A bounded global limiter does not allocate a map for attacker-controlled IPs.
type limiter struct {
	mu          sync.Mutex
	tokens      float64
	last        time.Time
	rate, burst float64
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.last.IsZero() {
		l.tokens = l.burst
	} else {
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
func (s *runtimeState) public(api http.Handler, passwordPath string) (http.Handler, error) {
	var review http.Handler
	if passwordPath != "" {
		if err := privateInfo(passwordPath, false); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(passwordPath)
		if err != nil {
			return nil, err
		}
		password := strings.TrimSpace(string(b))
		if len(password) < 24 || len(password) > 256 {
			return nil, errors.New("reviewer password must contain 24-256 characters in a private file")
		}
		review = s.reviewer(password)
	}
	limit := &limiter{rate: 40, burst: 80}
	slots := make(chan struct{}, 32)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !limit.allow() {
			w.Header().Set("Retry-After", "1")
			problem(w, 429, "demo request limit reached")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			problem(w, 503, "demo is busy")
			return
		}
		if r.URL.Path == "/review" {
			if review == nil {
				http.NotFound(w, r)
				return
			}
			review.ServeHTTP(w, r)
			return
		}
		// Never mount private IPC/admin handlers or their aliases on public HTTPS.
		if s.authenticated(r) {
			s.touch()
			if s.fault("api-unavailable") {
				problem(w, 503, "demo API temporarily unavailable")
				return
			}
			if s.fault("upload-rejected") && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/attachments") {
				problem(w, 507, "attachment storage quota reached")
				return
			}
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/terminal") && s.takeFault("bootstrap-delayed") {
				select {
				case <-time.After(3 * time.Second):
				case <-r.Context().Done():
					return
				}
			}
		}
		api.ServeHTTP(w, r)
	}), nil
}
func (s *runtimeState) reviewer(password string) http.Handler {
	expected := sha256.Sum256([]byte(password))
	limit := &limiter{rate: 1.0 / 30, burst: 4}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no-referrer makes ordinary browser form POSTs send Origin: null.
		// Preserve same-origin form provenance while withholding cross-origin referrers.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.TLS == nil {
			problem(w, 400, "HTTPS required")
			return
		}
		username, pw, ok := r.BasicAuth()
		actual := sha256.Sum256([]byte(pw))
		if !ok || username != "reviewer" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trill Demo", charset="UTF-8"`)
			problem(w, 401, "reviewer credentials required")
			return
		}
		if r.Host != strings.TrimPrefix(s.manifest.Address, "https://") {
			problem(w, 400, "unexpected host")
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			w.Header().Set("Allow", "GET, POST")
			problem(w, 405, "method not allowed")
			return
		}
		s.touch()
		var content string
		if r.Method == "POST" {
			if r.Header.Get("Origin") != s.manifest.Address {
				problem(w, 403, "same-origin form required")
				return
			}
			if !limit.allow() {
				w.Header().Set("Retry-After", "30")
				problem(w, 429, "wait before requesting another invitation")
				return
			}
			b, err := s.invitation()
			if err != nil {
				problem(w, 503, "invitation unavailable; contact the reviewer support address")
				return
			}
			png, err := setup.QRPNG(b)
			if err != nil {
				problem(w, 500, "QR unavailable")
				return
			}
			content = fmt.Sprintf(`<p><a href="%s">Pair this iPhone with Trill Demo</a></p><img width="280" height="280" alt="Single-use demo pairing QR" src="data:image/png;base64,%s"><p>This invitation expires in ten minutes and can be used once. Use the link on this iPhone, or scan the QR from another screen.</p>`, html.EscapeString(setup.PairURL(b)), base64.StdEncoding.EncodeToString(png))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>Trill Demo</title><style>body{font:17px system-ui;line-height:1.5;max-width:35rem;margin:8vh auto;padding:0 24px}button,a{display:inline-block;padding:12px}img{max-width:100%%;height:auto}</style><h1>Try Trill</h1><p>This sandbox uses real HTTPS terminal streams with scripted sessions. No Herdr, shell or coding agent runs here.</p>%s<form method="post" action="/review"><button type="submit">Create pairing invitation</button></form><p>Open a session and type <code>help</code> to start. Use sample photos only. This sandbox removes its data after %s of inactivity; active terminals keep it available.</p><p><a href="https://trill.bcook.io/support">Contact support</a></p></html>`, content, html.EscapeString(s.retention.String()))
	})
}
