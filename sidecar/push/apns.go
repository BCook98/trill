package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// APNs runs only on the trusted relay. Never distribute its key to companions.
type APNs struct {
	key                  *ecdsa.PrivateKey
	keyID, teamID, topic string
	client               *http.Client
	mu                   sync.Mutex
	token                string
	issued               time.Time
}

func NewAPNs(privateKey []byte, keyID, teamID, topic string) (*APNs, error) {
	identifier := regexp.MustCompile(`^[A-Z0-9]{10}$`)
	if !identifier.MatchString(keyID) || !identifier.MatchString(teamID) || topic != "io.bcook.trill" {
		return nil, errors.New("invalid APNs configuration")
	}
	block, rest := pem.Decode(privateKey)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid APNs private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid APNs private key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("APNs requires a P-256 private key")
	}
	return &APNs{key: key, keyID: keyID, teamID: teamID, topic: topic, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (provider *APNs) providerToken() (string, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.token != "" && time.Since(provider.issued) >= 0 && time.Since(provider.issued) < 50*time.Minute {
		return provider.token, nil
	}
	encode := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": provider.keyID})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iss": provider.teamID, "iat": now.Unix()})
	unsigned := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signatureR, signatureS, err := ecdsa.Sign(rand.Reader, provider.key, digest[:])
	if err != nil {
		return "", ErrDelivery
	}
	signature := make([]byte, 64)
	signatureR.FillBytes(signature[:32])
	signatureS.FillBytes(signature[32:])
	provider.token, provider.issued = unsigned+"."+encode(signature), now
	return provider.token, nil
}
func (provider *APNs) Send(ctx context.Context, alert Alert) error {
	payload, err := alert.Payload()
	if err != nil {
		return err
	}
	token, err := provider.providerToken()
	if err != nil {
		return err
	}
	host := "https://api.push.apple.com"
	if alert.Environment == "sandbox" {
		host = "https://api.sandbox.push.apple.com"
	}
	request, err := http.NewRequestWithContext(ctx, "POST", host+"/3/device/"+alert.Token, bytes.NewReader(payload))
	if err != nil {
		return ErrDelivery
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("authorization", "bearer "+token)
	request.Header.Set("apns-topic", provider.topic)
	request.Header.Set("apns-push-type", "alert")
	request.Header.Set("apns-priority", "10")
	request.Header.Set("apns-expiration", "0") // No stale alerts queued while the device is offline.
	request.Header.Set("apns-id", alert.EventID)
	request.Header.Set("apns-collapse-id", alert.EventID)
	response, err := provider.client.Do(request)
	if err != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	var failure struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
	if response.StatusCode == 200 {
		return nil
	}
	if response.StatusCode == 410 || (response.StatusCode == 400 && (failure.Reason == "BadDeviceToken" || failure.Reason == "DeviceTokenNotForTopic")) {
		return ErrInvalidToken
	}
	return ErrDelivery
}
