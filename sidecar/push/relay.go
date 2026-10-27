package push

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// Each manually provisioned companion gets a separate revocable credential.
// No registration or payload is logged or persisted by the relay.
type Relay struct {
	Sender      Sender
	Credentials []string
	mu          sync.Mutex
	windows     map[[32]byte]relayWindow
	attempted   map[[32]byte]time.Time
}
type relayWindow struct {
	started time.Time
	count   int
}

func (relay *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "POST" || r.URL.Path != "/v1/alerts" {
		http.NotFound(w, r)
		return
	}
	supplied := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	authorized := false
	for _, credential := range relay.Credentials {
		expected := sha256.Sum256([]byte("Bearer " + credential))
		authorized = subtle.ConstantTimeCompare(supplied[:], expected[:]) == 1 || authorized
	}
	if !authorized {
		w.WriteHeader(401)
		return
	}
	relay.mu.Lock()
	if relay.windows == nil {
		relay.windows = map[[32]byte]relayWindow{}
	}
	window := relay.windows[supplied]
	if time.Since(window.started) >= time.Minute {
		window = relayWindow{started: time.Now()}
	}
	window.count++
	relay.windows[supplied] = window
	relay.mu.Unlock()
	if window.count > 60 {
		w.WriteHeader(429)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(415)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var alert Alert
	if decoder.Decode(&alert) != nil || !alert.Valid() {
		w.WriteHeader(400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		w.WriteHeader(400)
		return
	}
	// Reserve before forwarding, including ambiguous failures. This bounded
	// one-hour replay guard complements the companion's no-retry policy.
	identity := sha256.Sum256([]byte(alert.Environment + "\x00" + alert.Token + "\x00" + alert.EventID))
	relay.mu.Lock()
	if relay.attempted == nil {
		relay.attempted = map[[32]byte]time.Time{}
	}
	for identifier, expiry := range relay.attempted {
		if time.Now().After(expiry) {
			delete(relay.attempted, identifier)
		}
	}
	_, duplicate := relay.attempted[identity]
	full := len(relay.attempted) >= 8192
	if !duplicate && !full {
		relay.attempted[identity] = time.Now().Add(time.Hour)
	}
	relay.mu.Unlock()
	if duplicate {
		w.WriteHeader(200)
		return
	}
	if full {
		w.WriteHeader(429)
		return
	}
	err := relay.Sender.Send(r.Context(), alert)
	if errors.Is(err, ErrInvalidToken) {
		w.WriteHeader(410)
		return
	}
	if err != nil {
		w.WriteHeader(502)
		return
	}
	w.WriteHeader(200)
}
