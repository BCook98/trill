// Package demoterm implements the restricted terminal child used by the demo
// companion. It renders parent-owned text and submits literal editor contents;
// it does not interpret commands or access project files.
package demoterm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxLineBytes     = 8 * 1024
	maxTextBytes     = 64 * 1024
	maxResponseBytes = 128 * 1024
	maxSessionBytes  = 256
	requestTimeout   = 2 * time.Second
)

type snapshot struct {
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
	Live     bool   `json:"live"`
	Columns  int    `json:"columns"`
	Rows     int    `json:"rows"`
}

type request struct {
	kind          string
	line          string
	columns, rows int
}

type result struct {
	request  request
	snapshot snapshot
	err      error
}

type ipcClient struct {
	session   string
	http      *http.Client
	transport *http.Transport
}

func newClient(socket, session string) *ipcClient {
	transport := &http.Transport{
		// Deliberately no Proxy, default transport, DNS, or TCP fallback. Even
		// malicious redirect responses cannot change the Unix-socket destination.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: requestTimeout}).DialContext(ctx, "unix", socket)
		},
		MaxConnsPerHost:        1,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        5 * time.Second,
		ResponseHeaderTimeout:  requestTimeout,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	return &ipcClient{session: session, transport: transport, http: &http.Client{
		Transport:     transport,
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *ipcClient) do(ctx context.Context, r request) (snapshot, error) {
	var state snapshot
	method, path := http.MethodGet, "/terminal?"+url.Values{"session": {c.session}}.Encode()
	var body []byte
	switch r.kind {
	case "poll":
	case "submit":
		if len(r.line) > maxLineBytes || !utf8.ValidString(r.line) {
			return state, errors.New("invalid demo terminal input")
		}
		method, path = http.MethodPost, "/terminal"
		body, _ = json.Marshal(struct {
			Session string `json:"session"`
			Line    string `json:"line"`
		}{c.session, r.line})
	case "resize":
		method, path = http.MethodPost, "/resize"
		body, _ = json.Marshal(struct {
			Session string `json:"session"`
			Columns int    `json:"columns"`
			Rows    int    `json:"rows"`
		}{c.session, r.columns, r.rows})
	default:
		return state, errors.New("invalid demo terminal request")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://demo.invalid"+path, bytes.NewReader(body))
	if err != nil {
		return state, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.GetBody = nil // Even a lost response must never replay a mutation.
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return state, fmt.Errorf("demo terminal IPC: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Do not echo arbitrary server errors or redirect locations into the PTY.
		return state, fmt.Errorf("demo terminal IPC status %d", response.StatusCode)
	}
	if response.ContentLength > maxResponseBytes {
		return state, errors.New("demo terminal response too large")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return state, fmt.Errorf("read demo terminal response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return state, errors.New("demo terminal response too large")
	}
	// Require every contract field. In particular a truncated/malformed response
	// must not silently become an empty successful snapshot.
	var wire struct {
		Text     *string `json:"text"`
		Revision *uint64 `json:"revision"`
		Live     *bool   `json:"live"`
		Columns  *int    `json:"columns"`
		Rows     *int    `json:"rows"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return state, errors.New("invalid demo terminal response JSON")
	}
	if wire.Text == nil || wire.Revision == nil || wire.Live == nil || wire.Columns == nil || wire.Rows == nil {
		return state, errors.New("incomplete demo terminal response")
	}
	if len(*wire.Text) > maxTextBytes || !utf8.ValidString(*wire.Text) {
		return state, errors.New("invalid demo terminal text")
	}
	if *wire.Columns < 1 || *wire.Columns > 1000 || *wire.Rows < 1 || *wire.Rows > 1000 {
		return state, errors.New("invalid demo terminal dimensions")
	}
	state = snapshot{*wire.Text, *wire.Revision, *wire.Live, *wire.Columns, *wire.Rows}
	return state, nil
}

// A single worker serializes all IPC. Mutation requests have no automatic
// retries. The HTTP transport cannot retry their bodies: no idempotency key is
// set, and http.NewRequest's GetBody hook is removed before sending.
func (c *ipcClient) worker(ctx context.Context, requests <-chan request, results chan<- result) {
	defer c.transport.CloseIdleConnections()
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-requests:
			state, err := c.do(ctx, r)
			select {
			case results <- result{r, state, err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func validSession(session string) bool {
	return session != "" && len(session) <= maxSessionBytes && utf8.ValidString(session) && !strings.ContainsAny(session, "\x00\r\n")
}
