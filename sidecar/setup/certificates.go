package setup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"path/filepath"
	"time"
)

func SelfSigned(ip, host, name string, now time.Time) ([]byte, string, error) {
	address := net.ParseIP(ip)
	if !usableIP(address) {
		return nil, "", errors.New("certificate requires a concrete IP SAN")
	}
	normalized, err := NormalizeLANHostname(host)
	if err != nil {
		return nil, "", err
	}
	host = normalized
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), IPAddresses: []net.IP{address}, DNSNames: []string{host}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", err
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	hash := sha256.Sum256(der)
	return bundle, hex.EncodeToString(hash[:]), nil
}

// ValidateBundle verifies the actual certificate, SAN and expiry. Trusted mode
// additionally checks its chain against system roots; an empty pin is never used
// merely because the local cert command succeeded.
func ValidateBundle(bundle []byte, host, fingerprint string, now time.Time, roots *x509.CertPool) (time.Time, error) {
	pair, err := tls.X509KeyPair(bundle, bundle)
	if err != nil || len(pair.Certificate) == 0 {
		return time.Time{}, errors.New("invalid certificate/key bundle")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}, err
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return time.Time{}, errors.New("certificate is expired or not yet valid")
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return time.Time{}, errors.New("certificate SAN does not match companion address")
	}
	if fingerprint != "" {
		hash := sha256.Sum256(leaf.Raw)
		if hex.EncodeToString(hash[:]) != fingerprint {
			return time.Time{}, errors.New("certificate fingerprint differs; reconfigure and pair again")
		}
	} else {
		intermediates := x509.NewCertPool()
		for _, der := range pair.Certificate[1:] {
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return time.Time{}, err
			}
			intermediates.AddCert(cert)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, CurrentTime: now, Roots: roots, Intermediates: intermediates}); err != nil {
			return time.Time{}, errors.New("certificate chain is not trusted; enable Tailscale HTTPS and retry or explicitly use --network lan in a new state directory")
		}
	}
	return leaf.NotAfter, nil
}

type Certificates struct {
	Runner Runner
	Now    func() time.Time
	Roots  *x509.CertPool
}

func NewCertificates() Certificates { return Certificates{Runner: ExecRunner{}, Now: time.Now} }
func (m Certificates) tailscaleBundle(ctx context.Context, cli, host string) ([]byte, error) {
	// The sandboxed macOS app cannot write arbitrary caller paths. Both dash
	// destinations export the combined PEM to stdout; Runner captures it with a
	// hard limit and discards stderr. Never print this output or command errors.
	bundle, err := m.Runner.Run(ctx, cli, "cert", "--cert-file=-", "--key-file=-", host)
	if err != nil {
		return nil, errors.New("Tailscale certificate export failed; connect Tailscale on this Mac and enable MagicDNS/HTTPS certificates in the tailnet DNS settings, then verify this CLI is allowed to export a certificate and retry setup (or trill renew for saved state). Before config is saved, --network lan is an explicit alternative; saved configurations require a new state directory. No fallback certificate was installed")
	}

	if _, err = m.validate(bundle, host, ""); err != nil {
		return nil, err
	}
	return bundle, nil
}
func (m Certificates) validate(bundle []byte, host, pin string) (time.Time, error) {
	return ValidateBundle(bundle, host, pin, m.Now(), m.Roots)
}
func (m Certificates) Provision(ctx context.Context, dir string, n Network, name string) (string, error) {
	if err := checkPrivateDir(dir); err != nil {
		return "", err
	}
	var bundle []byte
	var pin string
	var err error
	if n.Kind == "tailscale" {
		bundle, err = m.tailscaleBundle(ctx, n.TailscaleCLI, n.Host)
	} else {
		bundle, pin, err = SelfSigned(n.IP, n.Host, name, m.Now())
	}
	if err != nil {
		return "", err
	}
	if _, err = m.validate(bundle, n.Host, pin); err != nil {
		return "", err
	}
	return pin, WritePrivate(filepath.Join(dir, "tls.pem"), bundle, false)
}
func (m Certificates) Check(c Config) (time.Time, error) {
	b, err := ReadPrivate(c.Certificate)
	if err != nil {
		return time.Time{}, err
	}
	u, err := url.Parse(c.Address)
	if err != nil {
		return time.Time{}, err
	}
	return m.validate(b, u.Hostname(), c.Fingerprint)
}

// Renew never signals/restarts a daemon. LAN pins stay stable; replacing that
// identity requires explicit setup in a new state directory and re-pairing.
func (m Certificates) Renew(ctx context.Context, dir string, c Config) (bool, error) {
	if err := c.Validate(dir); err != nil {
		return false, err
	}
	expiry, err := m.Check(c)
	if c.Network != "tailscale" {
		if err != nil {
			return false, err
		}
		if expiry.Sub(m.Now()) < 30*24*time.Hour {
			return false, errors.New("LAN certificate expires within 30 days; run setup in a new state directory and pair again to change the pinned identity")
		}
		return false, nil
	}
	// Even an expired certificate can be recovered through explicit renewal.
	if err == nil && expiry.Sub(m.Now()) > 30*24*time.Hour {
		return false, nil
	}
	u, _ := url.Parse(c.Address)
	bundle, err := m.tailscaleBundle(ctx, c.TailscaleCLI, u.Hostname())
	if err != nil {
		return false, err
	}
	if err = WritePrivate(c.Certificate, bundle, true); err != nil {
		return false, err
	}
	return true, nil
}
