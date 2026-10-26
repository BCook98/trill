package demoapp

import (
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
	"os"
	"path/filepath"
	"time"
)

func certificate(root, address, certPath, keyPath string) (tls.Certificate, string, error) {
	u, err := url.Parse(address)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if (certPath == "") != (keyPath == "") {
		return tls.Certificate{}, "", errors.New("--cert and --key must be supplied together")
	}
	var cert tls.Certificate
	if certPath != "" {
		cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	} else {
		path := filepath.Join(root, "tls.pem")
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			bundle, e := localCertificate(u.Hostname())
			if e != nil {
				return cert, "", e
			}
			f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return cert, "", e
			}
			_, e = f.Write(bundle)
			closeErr := f.Close()
			if e != nil {
				return cert, "", e
			}
			if closeErr != nil {
				return cert, "", closeErr
			}
		}
		if err = privateInfo(path, false); err == nil {
			cert, err = tls.LoadX509KeyPair(path, path)
		}
	}
	if err != nil {
		return cert, "", err
	}
	if len(cert.Certificate) == 0 {
		return cert, "", errors.New("empty TLS certificate")
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		return cert, "", e
	}
	if e = leaf.VerifyHostname(u.Hostname()); e != nil {
		return cert, "", errors.New("TLS certificate does not match advertised demo address; use matching cert/key or a fresh state directory")
	}
	if time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return cert, "", errors.New("demo TLS certificate is expired or not yet valid")
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(digest[:]), nil
}

// Local fixtures need localhost/loopback SANs; production LAN provisioning
// deliberately rejects those and remains unchanged.
func localCertificate(host string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Trill Demo"}, NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
	if ip := net.ParseIP(host); ip != nil {
		cert.IPAddresses = append(cert.IPAddresses, ip)
	} else if host != "localhost" {
		cert.DNSNames = append(cert.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})...), nil
}
