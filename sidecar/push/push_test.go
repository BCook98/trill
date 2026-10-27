package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sampleAlert() Alert {
	return Alert{Token: strings.Repeat("a", 64), Environment: "sandbox", ComputerID: "11111111-1111-4111-8111-111111111111", SessionID: "herdr-" + strings.Repeat("b", 32), EventID: "22222222-2222-4222-8222-222222222222", Kind: "attention"}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAPNsPrivacySigningHeadersAndInvalidToken(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	provider, err := NewAPNs(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), "ABCDEFGHIJ", "KLMNOPQRST", "io.bcook.trill")
	if err != nil {
		t.Fatal(err)
	}
	status, reason := 200, ""
	provider.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.sandbox.push.apple.com" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-topic") != "io.bcook.trill" || r.Header.Get("apns-expiration") != "0" || r.Header.Get("apns-collapse-id") != sampleAlert().EventID {
			t.Fatal("incorrect APNs routing/headers")
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), ".")
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if len(signature) != 64 || !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			t.Fatal("invalid ES256 JWT")
		}
		contents, _ := io.ReadAll(r.Body)
		var payload map[string]json.RawMessage
		if json.Unmarshal(contents, &payload) != nil || len(payload) != 4 || payload["aps"] == nil || payload["computerId"] == nil || payload["sessionId"] == nil || payload["eventId"] == nil {
			t.Fatal("payload not allowlisted")
		}
		if strings.Contains(string(contents), sampleAlert().Token) {
			t.Fatal("token in payload")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"reason":"` + reason + `"}`)), Header: http.Header{}}, nil
	})
	if provider.Send(context.Background(), sampleAlert()) != nil {
		t.Fatal("send failed")
	}
	for _, failure := range []struct {
		status int
		reason string
	}{{410, "Unregistered"}, {400, "BadDeviceToken"}, {400, "DeviceTokenNotForTopic"}} {
		status, reason = failure.status, failure.reason
		if provider.Send(context.Background(), sampleAlert()) != ErrInvalidToken {
			t.Fatal("failed token not retired")
		}
	}
	status, reason = 403, "InvalidProviderToken"
	if provider.Send(context.Background(), sampleAlert()) != ErrDelivery {
		t.Fatal("provider failure retired device")
	}
}

type recordingSender struct{ calls int }

func (s *recordingSender) Send(context.Context, Alert) error { s.calls++; return nil }
func TestRelayRejectsContentAndUnauthenticatedRequests(t *testing.T) {
	sender := &recordingSender{}
	credential := strings.Repeat("c", 64)
	relay := &Relay{Sender: sender, Credentials: []string{credential}}
	body, _ := json.Marshal(sampleAlert())
	for _, tc := range []struct {
		body, authorization string
		status              int
	}{
		{string(body), "", 401}, {strings.TrimSuffix(string(body), "}") + `,"prompt":"secret"}`, "Bearer " + credential, 400},
		{strings.Replace(string(body), sampleAlert().SessionID, "/private/filename", 1), "Bearer " + credential, 400},
		{string(body) + `{}`, "Bearer " + credential, 400}, {string(body), "Bearer " + credential, 200}, {string(body), "Bearer " + credential, 200},
	} {
		request := httptest.NewRequest("POST", "/v1/alerts", strings.NewReader(tc.body))
		request.Header.Set("Authorization", tc.authorization)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		relay.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("got %d want %d", response.Code, tc.status)
		}
	}
	if sender.calls != 1 {
		t.Fatal("unsafe payload reached provider")
	}
}
func TestRelayClientRequiresHTTPSAndRefusesRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://relay.invalid/v1/alerts", "https://user:password@relay.invalid/v1/alerts", "https://relay.invalid/v1/alerts?token=x"} {
		if _, err := NewRelayClient(endpoint, strings.Repeat("c", 64)); err == nil {
			t.Fatal("unsafe relay URL accepted")
		}
	}
	client, _ := NewRelayClient("https://relay.invalid/v1/alerts", strings.Repeat("c", 64))
	if client.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("credential redirect allowed")
	}
}
