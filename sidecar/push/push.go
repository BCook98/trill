// Package push defines the deliberately content-free companion/relay contract.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var hexToken = regexp.MustCompile(`^[a-f0-9]{64,200}$`)
var UUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var sessionID = regexp.MustCompile(`^herdr-[a-f0-9]{32}$`)
var ErrInvalidToken = errors.New("push token is no longer valid")
var ErrDelivery = errors.New("push delivery failed")

// Registration belongs to one authenticated pairing, never a legacy master token.
type Registration struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	ComputerID  string `json:"computerId"`
	Attention   bool   `json:"attention"`
	Completion  bool   `json:"completion"`
}

func (registration Registration) Valid() bool {
	return hexToken.MatchString(registration.Token) && len(registration.Token)%2 == 0 && UUID.MatchString(registration.ComputerID) && (registration.Environment == "sandbox" || registration.Environment == "production") && (registration.Attention || registration.Completion)
}

// No free-form content is accepted or forwarded to APNs.
type Alert struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	ComputerID  string `json:"computerId"`
	SessionID   string `json:"sessionId"`
	EventID     string `json:"eventId"`
	Kind        string `json:"kind"`
}

func (alert Alert) Valid() bool {
	return (Registration{Token: alert.Token, Environment: alert.Environment, ComputerID: alert.ComputerID, Attention: true}).Valid() && sessionID.MatchString(alert.SessionID) && UUID.MatchString(alert.EventID) && (alert.Kind == "attention" || alert.Kind == "completion")
}
func (alert Alert) Payload() ([]byte, error) {
	if !alert.Valid() {
		return nil, ErrDelivery
	}
	title := "Agent needs attention"
	if alert.Kind == "completion" {
		title = "Agent finished work"
	}
	return json.Marshal(map[string]any{
		"aps":        map[string]any{"alert": map[string]string{"title": title, "body": "Open Trill to view the session."}, "sound": "default", "thread-id": alert.ComputerID},
		"computerId": alert.ComputerID, "sessionId": alert.SessionID, "eventId": alert.EventID,
	})
}

type Sender interface {
	Send(context.Context, Alert) error
}

type RelayClient struct {
	endpoint, credential string
	client               *http.Client
}

func NewRelayClient(endpoint, credential string) (*RelayClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !ValidCredential(credential) {
		return nil, errors.New("push relay requires an HTTPS URL and a private hex credential")
	}
	return &RelayClient{endpoint, credential, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (relayClient *RelayClient) Send(ctx context.Context, alert Alert) error {
	if !alert.Valid() {
		return ErrDelivery
	}
	body, _ := json.Marshal(alert)
	request, err := http.NewRequestWithContext(ctx, "POST", relayClient.endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrDelivery
	}
	request.Header.Set("Authorization", "Bearer "+relayClient.credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := relayClient.client.Do(request)
	if err != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == 410 {
		return ErrInvalidToken
	}
	if response.StatusCode != 200 {
		return ErrDelivery
	}
	return nil
}
