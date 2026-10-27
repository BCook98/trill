// trill-push-relay is an opt-in, operator-managed service; setup never deploys it.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"time"
	"trill/sidecar/push"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	configurationPath := flag.String("config", "", "private 0600 JSON configuration file")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected relay arguments")
	}
	contents, err := push.ReadPrivateFile(*configurationPath)
	if err != nil {
		return err
	}
	var configuration struct {
		Listen               string   `json:"listen"`
		TLSCertificate       string   `json:"tlsCertificate"`
		TLSKey               string   `json:"tlsKey"`
		APNsKey              string   `json:"apnsKey"`
		KeyID                string   `json:"keyId"`
		TeamID               string   `json:"teamId"`
		Topic                string   `json:"topic"`
		CompanionCredentials []string `json:"companionCredentials"`
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&configuration) != nil || configuration.Listen == "" || configuration.TLSCertificate == "" || configuration.TLSKey == "" || len(configuration.CompanionCredentials) == 0 {
		return errors.New("invalid relay configuration")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid relay configuration")
	}
	if _, err := push.ReadPrivateFile(configuration.TLSKey); err != nil {
		return err
	}
	for _, credential := range configuration.CompanionCredentials {
		if !push.ValidCredential(credential) {
			return errors.New("invalid relay credential")
		}
	}
	key, err := push.ReadPrivateFile(configuration.APNsKey)
	if err != nil {
		return err
	}
	provider, err := push.NewAPNs(key, configuration.KeyID, configuration.TeamID, configuration.Topic)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.Listen, Handler: &push.Relay{Sender: provider, Credentials: configuration.CompanionCredentials}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(os.Stderr, "relay: ", 0)}
	return server.ListenAndServeTLS(configuration.TLSCertificate, configuration.TLSKey)
}
