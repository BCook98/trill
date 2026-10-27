package tailnet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"trill/sidecar/internal/testcert"
	"trill/sidecar/setup"
)

func TestCertificateRenewalPublishesOnlyTrustedMatchingCertificates(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := setup.PrivateDir(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "tls.pem")
	hostname := "companion.example.ts.net"
	now := time.Now()
	previous, roots := testcert.Bundle(t, hostname, now, 10*24*time.Hour)
	if err := storeTrustedCertificate(path, hostname, previous, now, roots); err != nil {
		t.Fatal(err)
	}
	renewed, newRoots := testcert.Bundle(t, hostname, now, 90*24*time.Hour)
	roots.AppendCertsFromPEM(renewed)
	if err := storeTrustedCertificate(path, hostname, renewed, now, roots); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ValidateBundle(renewed, hostname, "", now, newRoots); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		bundle []byte
		host   string
		now    time.Time
	}{
		{[]byte("not a certificate"), hostname, now},
		{previous, hostname, now.Add(20 * 24 * time.Hour)},
		{previous, "different.example.ts.net", now},
	} {
		if err := storeTrustedCertificate(path, invalid.host, invalid.bundle, invalid.now, roots); err == nil {
			t.Fatal("accepted invalid certificate")
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, renewed) {
			t.Fatal("failed refresh changed the installed identity")
		}
	}
	selfSigned, _, err := setup.SelfSigned("192.168.1.1", "test.local", "Synthetic", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeTrustedCertificate(path, "test.local", selfSigned, now, nil); err == nil {
		t.Fatal("accepted a self-signed fallback")
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("renewed private key is not private")
	}
}
