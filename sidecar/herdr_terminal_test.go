package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"trill/sidecar/pairing"
)

type terminalTestRunner struct {
	herdrRunner
	executable string
}

func (runner *terminalTestRunner) StartHerdrTerminal(ctx context.Context, path string, arguments, environment []string) (*herdrTerminalProcess, error) {
	if path != "/tmp/herdr" || strings.Contains(strings.Join(arguments, " "), "takeover") {
		return nil, errUnsupported
	}
	return (ExecRunner{}).StartHerdrTerminal(ctx, runner.executable, nil, environment)
}
func newTerminalTestService(t *testing.T) (*Service, *terminalTestRunner) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "controller")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/python3
import sys,json,base64
sequence=0
def frame(data):
 global sequence
 sequence+=1
 print(json.dumps(dict(type='terminal.frame',seq=sequence,width=80,height=24,bytes=base64.b64encode(data).decode())),flush=True)
frame(b'initial')
for line in sys.stdin:
 command=json.loads(line)
 if command['type']=='terminal.release': break
 if command['type']=='terminal.input': frame(base64.b64decode(command['bytes']))
 elif command['type']=='terminal.scroll': frame(json.dumps(command).encode())
 else: frame(command['type'].encode())
`), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &terminalTestRunner{herdrRunner: herdrRunner{snapshot: herdrFixture}, executable: script}
	service, err := NewService(Config{Backends: "herdr", HerdrPath: "/tmp/herdr", HerdrSession: "test-server", AllowControl: true}, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service, runner
}
func TestHerdrTerminalWebSocketInputAndDisconnect(t *testing.T) {
	service, _ := newTerminalTestService(t)
	handler := NewHTTP(service, "test-token")
	t.Cleanup(handler.Close)
	server := httptest.NewServer(handler)
	defer server.Close()
	sessions, err := service.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := request(handler, "POST", "/v1/sessions/"+sessions[0].ID+"/terminal", "test-token", `{"cols":80,"rows":24}`)
	var lease TerminalLease
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &lease) != nil {
		t.Fatalf("bootstrap: %d %s", response.Code, response.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/leases/"+lease.LeaseID+"/terminal", &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer test-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	read := func() []byte {
		t.Helper()
		_, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Bytes []byte `json:"bytes"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		return frame.Bytes
	}
	if string(read()) != "initial" {
		t.Fatal("missing initial frame")
	}
	link := "\x1b]8;;https://example.com/target\x1b\\Repository\x1b]8;;\x1b\\"
	data, _ := json.Marshal(map[string]string{"type": "terminal.input", "bytes": base64.StdEncoding.EncodeToString([]byte(link))})
	if err := socket.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
	if string(read()) != link {
		t.Fatal("OSC8 target did not survive relay")
	}
	for _, kind := range []string{"terminal.resize", "terminal.scroll"} {
		command := `{"type":"terminal.resize","cols":93,"rows":31}`
		if kind == "terminal.scroll" {
			command = `{"type":"terminal.scroll","direction":"up","lines":12,"column":35,"row":10}`
		}
		if err := socket.Write(ctx, websocket.MessageText, []byte(command)); err != nil {
			t.Fatal(err)
		}
		data := read()
		if kind == "terminal.scroll" {
			var scroll struct {
				Column int `json:"column"`
				Row    int `json:"row"`
			}
			if json.Unmarshal(data, &scroll) != nil || scroll.Column != 35 || scroll.Row != 10 {
				t.Fatal("lost scroll hit position", string(data))
			}
		} else if string(data) != kind {
			t.Fatal("lost controller operation")
		}
	}
	socket.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		count := len(service.leases)
		service.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket disconnect did not release controller")
}
func TestHerdrTerminalOwnershipBarrier(t *testing.T) {
	service, _ := newTerminalTestService(t)
	sessions, _ := service.Sessions(context.Background())
	id := sessions[0].ID
	old, err := service.bootstrapDevice(context.Background(), id, 80, 24, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.bootstrapDevice(context.Background(), id, 80, 24, "phone-b"); err != errTerminalBusy {
		t.Fatal(err)
	}
	fresh, err := service.bootstrapDevice(context.Background(), id, 80, 24, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	if old.LeaseID == fresh.LeaseID || service.DeleteLease(old.LeaseID) {
		t.Fatal("old controller survived replacement")
	}
}

func TestTerminalCommandValidation(t *testing.T) {
	for _, data := range []string{`{"type":"terminal.input","bytes":"aGk="}`, `{"type":"terminal.resize","cols":80,"rows":24}`, `{"type":"terminal.scroll","direction":"up","lines":12}`, `{"type":"terminal.release"}`} {
		if err := validateTerminalCommand([]byte(data)); err != nil {
			t.Fatal(data, err)
		}
	}
	for _, data := range []string{`{"type":"server.stop"}`, `{"type":"terminal.input","bytes":"bad"}`, `{"type":"terminal.input","text":"run"}`, `{"type":"terminal.resize","cols":99999,"rows":24}`, `{"type":"terminal.scroll","direction":"up","lines":0}`, `{"type":"terminal.release","takeover":true}`, `{"type":"terminal.release"} {}`, `{"type":"terminal.input","bytes":"aGk=","cols":80}`} {
		if validateTerminalCommand([]byte(data)) == nil {
			t.Fatal("accepted", data)
		}
	}
	for _, data := range []string{
		`{"type":"terminal.scroll","direction":"up","lines":2,"column":-1,"row":5}`,
		`{"type":"terminal.scroll","direction":"up","lines":2,"column":5}`,
		`{"type":"terminal.scroll","direction":"up","lines":2,"column":5,"row":200}`,
		`{"type":"terminal.input","bytes":"aGk=","column":5,"row":5}`,
	} {
		if validateTerminalCommand([]byte(data)) == nil {
			t.Fatal("accepted invalid scroll position", data)
		}
	}
}

func TestHerdrTerminalPairedRevocationClosesLiveSocket(t *testing.T) {
	service, _ := newTerminalTestService(t)
	store := pairing.NewStore(filepath.Join(t.TempDir(), "devices.json"))
	handler := NewHTTPWithPairing(service, "", store)
	t.Cleanup(handler.Close)
	device, token := pairPhone(t, handler, store, "Stream phone")
	sessions, _ := service.Sessions(context.Background())
	response := request(handler, "POST", "/v1/sessions/"+sessions[0].ID+"/terminal", token, `{"cols":80,"rows":24}`)
	var lease TerminalLease
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &lease) != nil {
		t.Fatal(response.Code, response.Body)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/leases/"+lease.LeaseID+"/terminal", &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if _, _, err = socket.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Revoke(device); err != nil {
		t.Fatal(err)
	}
	if _, _, err = socket.Read(ctx); err == nil {
		t.Fatal("revoked stream stayed open")
	}
}
