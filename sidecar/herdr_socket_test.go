package sidecar

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHerdrSocketSnapshotAndEvents(t *testing.T) {
	configurationHome, err := os.MkdirTemp("/tmp", "trill-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(configurationHome)
	path := herdrSocketPath(configurationHome, "test-server")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan string, 2)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer connection.Close()
				var request struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.NewDecoder(connection).Decode(&request) != nil {
					return
				}
				requests <- request.Method
				if request.Method == "session.snapshot" {
					connection.Write([]byte(herdrFixture + "\n"))
					return
				}
				if request.Method == "events.subscribe" {
					if !strings.Contains(string(request.Params), "pane.agent_status_changed") {
						return
					}
					connection.Write([]byte("{\"id\":\"trill\",\"result\":{\"type\":\"subscribed\"}}\n{\"event\":\"workspace.created\"}\n"))
				}
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := (ExecRunner{}).HerdrSnapshot(ctx, configurationHome, "test-server")
	if err != nil || string(data) != herdrFixture {
		t.Fatal(string(data), err)
	}
	service, _ := newTerminalTestService(t)
	service.config.HerdrConfigHome = configurationHome
	if err = service.waitHerdrChange(ctx); err != nil {
		t.Fatal(err)
	}
	if <-requests != "session.snapshot" || <-requests != "events.subscribe" {
		t.Fatal("wrong socket methods")
	}
	if herdrSocketPath(configurationHome, "default") != filepath.Join(configurationHome, "herdr", "herdr.sock") {
		t.Fatal("wrong default server path")
	}
}
