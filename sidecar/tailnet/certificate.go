package tailnet

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"os"
	"time"

	"tailscale.com/tsnet"
	"trill/sidecar/setup"
)

// RefreshCertificate issues or renews through this embedded node. Only a
// hostname-matching, system-trusted chain is published; there is no pinned or
// self-signed fallback for Tailscale modes.
func RefreshCertificate(ctx context.Context, server *tsnet.Server, hostname, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client, err := server.LocalClient()
	if err != nil {
		return err
	}
	certificate, key, err := client.CertPairWithValidity(ctx, hostname, 30*24*time.Hour)
	if err != nil {
		return errors.New("embedded Tailscale certificate issuance failed; enable MagicDNS and HTTPS certificates in this tailnet and retry (no fallback certificate installed)")
	}
	bundle := append(append([]byte(nil), certificate...), key...)
	return storeTrustedCertificate(path, hostname, bundle, time.Now(), nil)
}

func storeTrustedCertificate(path, hostname string, bundle []byte, now time.Time, roots *x509.CertPool) error {
	if _, err := setup.ValidateBundle(bundle, hostname, "", now, roots); err != nil {
		return err
	}
	previous, err := setup.ReadPrivate(path)
	if err == nil && bytes.Equal(previous, bundle) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return setup.WritePrivate(path, bundle, err == nil)
}
