package setup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedCertificateAndPin(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, ip := range []string{"192.168.1.2", "2001:db8::1"} {
		bundle, pin, err := SelfSigned(ip, "my-mac.local", "My Mac", now)
		if err != nil {
			t.Fatal(err)
		}
		expiry, err := ValidateBundle(bundle, ip, pin, now, nil)
		if err != nil || !expiry.Equal(now.AddDate(1, 0, 0)) {
			t.Fatalf("%v %v", expiry, err)
		}
		if _, err = ValidateBundle(bundle, "192.168.8.9", pin, now, nil); err == nil {
			t.Fatal("accepted wrong SAN")
		}
		if _, err = ValidateBundle(bundle, ip, strings.Repeat("0", 64), now, nil); err == nil {
			t.Fatal("accepted wrong pin")
		}
		if _, err = ValidateBundle(bundle, ip, pin, now.AddDate(2, 0, 0), nil); err == nil {
			t.Fatal("accepted expired certificate")
		}
		if _, err = ValidateBundle(bundle, ip, pin, now.Add(-time.Hour), nil); err == nil {
			t.Fatal("accepted future certificate")
		}
		if _, err = ValidateBundle(bundle, ip, "", now, x509.NewCertPool()); err == nil {
			t.Fatal("self signed certificate claimed trusted")
		}
	}
	if _, _, err := SelfSigned("0.0.0.0", "my-mac.local", "bad", now); err == nil {
		t.Fatal("issued unspecified SAN")
	}
}
func trustedBundle(t *testing.T, host string, now time.Time, validity time.Duration) ([]byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{host}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(validity), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return bundle, pool
}
func TestTailscaleExportValidationAndRenewal(t *testing.T) {
	dir := testDir(t)
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	host := "mac.example.ts.net"
	old, _ := trustedBundle(t, host, now, 10*24*time.Hour)
	bundle, roots := trustedBundle(t, host, now, 90*24*time.Hour)
	// Retain both issuers in the test trust store.
	roots.AppendCertsFromPEM(old)
	r := &fakeRunner{run: func(name string, args []string) ([]byte, error) {
		if name != "/Applications/Tailscale.app/Contents/MacOS/Tailscale" || !reflect.DeepEqual(args, []string{"cert", "--cert-file=-", "--key-file=-", host}) {
			t.Fatalf("wrong export invocation: %s %v", name, args)
		}
		return bundle, nil
	}}
	c := testConfig(dir)
	c.Network = "tailscale"
	c.IP = "100.73.54.69"
	c.Address = "https://" + host + ":7443"
	c.Fingerprint = ""
	c.TailscaleCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	m := Certificates{Runner: r, Now: func() time.Time { return now }, Roots: roots}
	if err := WritePrivate(c.Certificate, old, false); err != nil {
		t.Fatal(err)
	}
	changed, err := m.Renew(context.Background(), dir, c)
	if err != nil || !changed {
		t.Fatalf("%t %v", changed, err)
	}
	got, err := ReadPrivate(c.Certificate)
	if err != nil || !bytes.Equal(got, bundle) {
		t.Fatal("renewal did not publish validated bundle")
	}
	changed, err = m.Renew(context.Background(), dir, c)
	if err != nil || changed || len(r.calls) != 1 {
		t.Fatalf("unneeded renewal: %t %v %v", changed, err, r.calls)
	}
	st, _ := os.Stat(c.Certificate)
	if st.Mode().Perm() != 0600 {
		t.Fatal("certificate is not private")
	}
	// Failure preserves the installed identity and does not expose captured output.
	m.Now = func() time.Time { return now.Add(70 * 24 * time.Hour) }
	r.run = func(string, []string) ([]byte, error) {
		return []byte("SECRET PRIVATE KEY"), errors.New("SECRET PRIVATE KEY")
	}
	changed, err = m.Renew(context.Background(), dir, c)
	if err == nil || changed || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe failure: %v", err)
	}
	after, _ := ReadPrivate(c.Certificate)
	if !bytes.Equal(after, bundle) {
		t.Fatal("failed renewal altered installed TLS")
	}
	// Valid PEM from an untrusted issuer or wrong hostname is rejected.
	r.run = func(string, []string) ([]byte, error) { return old, nil }
	m.Roots = x509.NewCertPool()
	m.Now = func() time.Time { return now }
	if _, err = m.Renew(context.Background(), dir, c); err == nil {
		t.Fatal("accepted an untrusted exported certificate")
	}
	if len(r.calls) != 3 {
		t.Fatalf("unexpected operations: %+v", r.calls)
	}
}
func TestProvisionCreatesPrivateIdentityAndNeverOverwrites(t *testing.T) {
	dir := testDir(t)
	now := time.Now()
	r := &fakeRunner{}
	m := Certificates{Runner: r, Now: func() time.Time { return now }}
	n := Network{Kind: "lan", IP: "192.168.1.2", Host: "my-mac.local"}
	pin, err := m.Provision(context.Background(), dir, n, "Mac")
	if err != nil || len(pin) != 64 {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tls.pem")
	original, _ := ReadPrivate(path)
	if _, err = m.Provision(context.Background(), dir, n, "Other Mac"); err == nil {
		t.Fatal("replaced identity")
	}
	got, _ := ReadPrivate(path)
	if !bytes.Equal(got, original) {
		t.Fatal("changed identity")
	}
	c := testConfig(dir)
	c.Fingerprint = pin
	if changed, err := m.Renew(context.Background(), dir, c); err != nil || changed {
		t.Fatalf("LAN renew: %t %v", changed, err)
	}
	m.Now = func() time.Time { return now.AddDate(0, 11, 10) }
	if _, err = m.Renew(context.Background(), dir, c); err == nil {
		t.Fatal("missing pinned identity expiry warning")
	}
	if len(r.calls) != 0 {
		t.Fatal("LAN contacted certificate provider")
	}
}
func TestCertificateFailureDoesNotInstallFakeTrust(t *testing.T) {
	dir := testDir(t)
	now := time.Now()
	bundle, _, err := SelfSigned("192.168.1.2", "my-mac.local", "Mac", now)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{run: func(string, []string) ([]byte, error) { return bundle, nil }}
	m := Certificates{Runner: r, Now: func() time.Time { return now }, Roots: x509.NewCertPool()}
	n := Network{Kind: "tailscale", IP: "100.73.54.69", Host: "mac.example.ts.net", TailscaleCLI: "/fake/tailscale"}
	if _, err = m.Provision(context.Background(), dir, n, "Mac"); err == nil {
		t.Fatal("accepted fake trusted certificate")
	}
	if _, err = os.Stat(filepath.Join(dir, "tls.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published invalid TLS")
	}
}

func TestInitialTailscaleExportFailureExplainsRetryWithoutPublishingOrLeaking(t *testing.T) {
	dir := testDir(t)
	r := &fakeRunner{run: func(string, []string) ([]byte, error) {
		return []byte("SECRET PRIVATE KEY"), errors.New("SECRET COMMAND ERROR")
	}}
	m := Certificates{Runner: r, Now: time.Now}
	n := Network{Kind: "tailscale", IP: "100.73.54.69", Host: "mac.example.ts.net", TailscaleCLI: "/fake/tailscale"}
	_, err := m.Provision(context.Background(), dir, n, "Mac")
	if err == nil || !strings.Contains(err.Error(), "HTTPS certificates") || !strings.Contains(err.Error(), "retry setup") || !strings.Contains(err.Error(), "trill renew") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unhelpful or unsafe certificate error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed export published certificate state")
	}
	if len(r.calls) != 1 {
		t.Fatal("failed export retried or fell back without explicit choice")
	}
}
