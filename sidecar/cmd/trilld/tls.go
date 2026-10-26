package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"sync"
	"time"
)

var errCertificate = errors.New("TLS certificate pair unavailable or outside validity period")

// certificateReloader commits metadata only after loading a matching, currently
// valid pair. A non-atomic two-file renewal is retried on the next handshake.
// The previous pair bridges that swap only while its leaf remains valid.
type certificateReloader struct {
	mu                sync.Mutex
	certPath, keyPath string
	certInfo, keyInfo os.FileInfo
	current           *tls.Certificate
}

func newCertificateReloader(cert, key string) (*certificateReloader, error) {
	r := &certificateReloader{certPath: cert, keyPath: key}
	if _, err := r.GetCertificate(nil); err != nil {
		return nil, err
	}
	return r, nil
}
func sameFileVersion(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.ModTime() == b.ModTime() && a.Size() == b.Size()
}
func certFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, errCertificate
	}
	return info, nil
}
func certificateValid(c *tls.Certificate, now time.Time) bool {
	return c != nil && c.Leaf != nil && !now.Before(c.Leaf.NotBefore) && now.Before(c.Leaf.NotAfter)
}
func (r *certificateReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	certInfo, certErr := certFile(r.certPath)
	keyInfo, keyErr := certFile(r.keyPath)
	if certErr == nil && keyErr == nil && (!sameFileVersion(r.certInfo, certInfo) || !sameFileVersion(r.keyInfo, keyInfo)) {
		pair, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
		if err == nil && pair.Leaf == nil {
			pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0])
		}
		// Files may change during loading. Only cache a coherent snapshot; retry any
		// incomplete swap on the next handshake, including changes of just the key.
		afterCert, ec := certFile(r.certPath)
		afterKey, ek := certFile(r.keyPath)
		if err == nil && ec == nil && ek == nil && sameFileVersion(certInfo, afterCert) && sameFileVersion(keyInfo, afterKey) && certificateValid(&pair, time.Now()) {
			r.current, r.certInfo, r.keyInfo = &pair, afterCert, afterKey
		}
	}
	if !certificateValid(r.current, time.Now()) {
		return nil, errCertificate
	}
	return r.current, nil
}
