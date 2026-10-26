// Private demo tooling: use the real pairing store, never seed credentials by hand.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"trill/sidecar/pairing"
)

func main() {
	dir := flag.String("state-dir", "", "owned demo lab directory")
	output := flag.String("output", "", "new private invitation URL file")
	flag.Parse()
	if err := invite(*dir, *output); err != nil {
		fmt.Fprintln(os.Stderr, "demo invitation:", err)
		os.Exit(1)
	}
}
func invite(dir, output string) error {
	var marker struct{ Kind, Address, Name, Fingerprint string }
	b, err := os.ReadFile(filepath.Join(dir, "owner.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &marker); err != nil {
		return err
	}
	if marker.Kind != "trill-demo-lab-v1" || !filepath.IsAbs(dir) || !filepath.IsAbs(output) {
		return fmt.Errorf("owned demo lab and absolute output required")
	}
	code, expiry, err := pairing.NewStore(filepath.Join(dir, "devices.json")).CreateInvitation(10 * time.Minute)
	if err != nil {
		return err
	}
	payload := struct {
		Version     int       `json:"version"`
		Address     string    `json:"address"`
		Name        string    `json:"name"`
		Fingerprint string    `json:"fingerprint"`
		Code        string    `json:"code"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}{1, marker.Address, marker.Name, marker.Fingerprint, code, expiry}
	b, err = json.Marshal(payload)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, "trill://pair?data="+base64.RawURLEncoding.EncodeToString(b))
	return err
}
