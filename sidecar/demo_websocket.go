package sidecar

// A small RFC 6455 server for read-only fixture updates. No extensions or
// application messages are accepted. Control frames are bounded and masked.
import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func (d *Demo) serveDemoWS(w http.ResponseWriter, r *http.Request, id string) {
	key := r.Header.Get("Sec-WebSocket-Key")
	decoded, err := base64.StdEncoding.DecodeString(key)
	token := false
	for _, v := range strings.Split(r.Header.Get("Connection"), ",") {
		token = token || strings.EqualFold(strings.TrimSpace(v), "upgrade")
	}
	if r.Method != "GET" || !token || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || err != nil || len(decoded) != 16 || base64.StdEncoding.EncodeToString(decoded) != key || r.Header.Get("Sec-WebSocket-Protocol") != "" {
		http.Error(w, "invalid websocket upgrade", 400)
		return
	}
	select {
	case d.wsSlots <- struct{}{}:
		defer func() { <-d.wsSlots }()
	default:
		http.Error(w, "demo connection limit", 429)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade unavailable", 500)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err != nil || rw.Flush() != nil {
		return
	}
	var writeMu sync.Mutex
	send := func(op byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return demoWSWrite(conn, op, payload)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-d.done:
			_ = conn.Close()
		case <-r.Context().Done():
			_ = conn.Close()
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer conn.Close()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
			op, payload, err := demoWSRead(rw.Reader)
			if err != nil {
				_ = send(8, []byte{3, 234})
				return
			}
			switch op {
			case 8:
				_ = send(8, payload)
				return
			case 9:
				if send(10, payload) != nil {
					return
				}
			case 10:
			default:
				return
			}
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	var previous uint64
	for {
		d.mu.Lock()
		v, err := d.session(id)
		revision := d.state.Revision
		heading := ""
		if err == nil {
			heading = d.state.Headings[v.Directory]
		}
		d.mu.Unlock()
		if err != nil {
			return
		}
		if revision != previous {
			data, _ := json.Marshal(map[string]any{"heading": heading, "revision": revision})
			if send(1, data) != nil {
				return
			}
			previous = revision
		}
		select {
		case <-ticker.C:
		case <-ping.C:
			if send(9, nil) != nil {
				return
			}
		case <-readDone:
			return
		case <-d.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}
func demoWSWrite(w io.Writer, op byte, payload []byte) error {
	if len(payload) > 65535 {
		return errors.New("websocket payload too large")
	}
	header := []byte{0x80 | op}
	if len(payload) < 126 {
		header = append(header, byte(len(payload)))
	} else {
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
func demoWSRead(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 15
	n := int(h[1] & 127)
	if h[0]&0xf0 != 0x80 || h[1]&128 == 0 || op != 8 && op != 9 && op != 10 || n > 125 {
		return 0, nil, errors.New("unsupported websocket frame")
	}
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	if op == 8 && (n == 1 || n >= 2 && (!utf8.Valid(payload[2:]) || !demoWSCloseCode(binary.BigEndian.Uint16(payload)))) {
		return 0, nil, errors.New("invalid websocket close")
	}
	return op, payload, nil
}
func demoWSCloseCode(v uint16) bool {
	return v >= 3000 && v <= 4999 || v >= 1000 && v <= 1014 && v != 1004 && v != 1005 && v != 1006
}
