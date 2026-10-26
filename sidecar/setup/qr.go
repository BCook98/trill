package setup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

type Payload struct {
	Version     int    `json:"version"`
	Address     string `json:"address"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Code        string `json:"code"`
	ExpiresAt   string `json:"expiresAt"`
}

func PairPayload(c Config, code string, expiry time.Time) ([]byte, error) {
	if len(code) < 64 || len(code)%2 != 0 || strings.Trim(code, "0123456789abcdefABCDEF") != "" || expiry.IsZero() {
		return nil, errors.New("invitation requires at least 64 ASCII hex characters and an expiry")
	}
	return json.Marshal(Payload{1, c.Address, c.Name, c.Fingerprint, code, expiry.UTC().Format(time.RFC3339)})
}
func PairURL(payload []byte) string {
	return "trill://pair?data=" + base64.RawURLEncoding.EncodeToString(payload)
}
func QRPNG(payload []byte) ([]byte, error) { return qrcode.Encode(string(payload), qrcode.Medium, 768) }
func PrintQR(w io.Writer, payload []byte) error {
	q, err := qrcode.New(string(payload), qrcode.Medium)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, q.ToSmallString(false))
	return err
}
