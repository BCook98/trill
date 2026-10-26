package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func certificatePEM(t *testing.T, serial int64, before, after time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "trill.test"}, DNSNames: []string{"trill.test"}, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})
}
func replacePEM(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), "renew-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		t.Fatal(err)
	}
}
func TestCertificateReloadAndIncompleteSwap(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	now := time.Now()
	cert1, key1 := certificatePEM(t, 1, now.Add(-time.Hour), now.Add(time.Hour))
	cert2, key2 := certificatePEM(t, 2, now.Add(-time.Hour), now.Add(2*time.Hour))
	replacePEM(t, certPath, cert1)
	replacePEM(t, keyPath, key1)
	r, err := newCertificateReloader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.GetCertificate(nil)
	if err != nil || first.Leaf.SerialNumber.Int64() != 1 {
		t.Fatal("startup pair invalid", err)
	}
	cached, err := r.GetCertificate(nil)
	if err != nil || cached != first {
		t.Fatal("unchanged pair not cached")
	}
	// Cert-first replacement gives a mismatched pair: retain the valid old pair,
	// then reload next handshake when the key catches up, without a daemon restart.
	replacePEM(t, certPath, cert2)
	partial, err := r.GetCertificate(nil)
	if err != nil || partial != first {
		t.Fatal("incomplete swap discarded valid certificate")
	}
	replacePEM(t, keyPath, key2)
	second, err := r.GetCertificate(nil)
	if err != nil || second == first || second.Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("renewed pair not loaded")
	}
	// Key-first replacement must behave the same way.
	replacePEM(t, keyPath, key1)
	retained, err := r.GetCertificate(nil)
	if err != nil || retained != second {
		t.Fatal("key-first swap discarded valid pair")
	}
	replacePEM(t, certPath, cert1)
	third, err := r.GetCertificate(nil)
	if err != nil || third.Leaf.SerialNumber.Int64() != 1 {
		t.Fatal("key-first renewal not retried")
	}
	replacePEM(t, keyPath, []byte("invalid private data"))
	if got, err := r.GetCertificate(nil); err != nil || got != third {
		t.Fatal("invalid replacement discarded valid pair")
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetCertificate(nil); err != nil || got != third {
		t.Fatal("missing replacement discarded valid pair")
	}
	// Once the retained leaf expires, even a partial/invalid replacement fails.
	r.current.Leaf.NotAfter = time.Now().Add(-time.Second)
	if got, err := r.GetCertificate(nil); err == nil || got != nil {
		t.Fatal("expired fallback served")
	}
	replacePEM(t, certPath, cert2)
	replacePEM(t, keyPath, key2)
	if got, err := r.GetCertificate(nil); err != nil || got.Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("failed reload never recovered")
	}
}
func TestCertificateStartupRejectsInvalidPairs(t *testing.T) {
	now := time.Now()
	good, key := certificatePEM(t, 1, now.Add(-time.Hour), now.Add(time.Hour))
	_, otherKey := certificatePEM(t, 2, now.Add(-time.Hour), now.Add(time.Hour))
	expired, expiredKey := certificatePEM(t, 3, now.Add(-2*time.Hour), now.Add(-time.Hour))
	future, futureKey := certificatePEM(t, 4, now.Add(time.Hour), now.Add(2*time.Hour))
	for _, tc := range []struct {
		name      string
		cert, key []byte
	}{
		{"invalid", []byte("invalid"), key}, {"mismatch", good, otherKey}, {"expired", expired, expiredKey}, {"future", future, futureKey}, {"oversized", make([]byte, (1<<20)+1), key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cp, kp := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
			replacePEM(t, cp, tc.cert)
			replacePEM(t, kp, tc.key)
			if _, err := newCertificateReloader(cp, kp); err == nil {
				t.Fatal("invalid startup pair accepted")
			}
		})
	}
	if _, err := newCertificateReloader("/nonexistent/cert", "/nonexistent/key"); err == nil {
		t.Fatal("missing startup pair accepted")
	}
}
func TestCertificateReloadConcurrentHandshakes(t *testing.T) {
	dir := t.TempDir()
	cp, kp := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	now := time.Now()
	c1, k1 := certificatePEM(t, 1, now.Add(-time.Hour), now.Add(time.Hour))
	c2, k2 := certificatePEM(t, 2, now.Add(-time.Hour), now.Add(time.Hour))
	replacePEM(t, cp, c1)
	replacePEM(t, kp, k1)
	r, err := newCertificateReloader(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	// Use the same callback as tls.Config on concurrent handshakes; no live
	// listener or server is started by these tests.
	cfg := &tls.Config{GetCertificate: r.GetCertificate}
	var wg sync.WaitGroup
	for j := 0; j < 16; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				got, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
				if err != nil || got == nil || !certificateValid(got, time.Now()) {
					t.Error("concurrent callback lost valid pair")
				}
			}
		}()
	}
	replacePEM(t, cp, c2)
	replacePEM(t, kp, k2)
	wg.Wait()
	got, err := cfg.GetCertificate(nil)
	if err != nil || got.Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("concurrent reload failed")
	}
}
