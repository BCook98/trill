package demoapp

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func goodOptions() options {
	return options{listen: "127.0.0.1:19443", preview: "127.0.0.1:18080", moshBind: "127.0.0.1", maxLeases: 2, profile: "mixed", name: "Test", retention: 24 * time.Hour}
}
func TestNetworkOptionsRejectUnsafeDefaults(t *testing.T) {
	for _, change := range []func(*options){func(o *options) { o.listen = "127.0.0.1:7443" }, func(o *options) { o.preview = "0.0.0.0:18080" }, func(o *options) { o.address = "http://example.test" }, func(o *options) { o.address = "https://user:pw@example.test" }, func(o *options) { o.reviewPassword = "secret" }, func(o *options) { o.maxLeases = 100 }, func(o *options) { o.retention = time.Second }} {
		o := goodOptions()
		change(&o)
		if validateOptions(&o) == nil {
			t.Fatal("accepted unsafe options")
		}
	}
	o := goodOptions()
	if err := validateOptions(&o); err != nil {
		t.Fatal(err)
	}
	if o.name != "Demo · Test" {
		t.Fatal("demo not labeled")
	}
}
func TestLocalCertificateIdentityPersists(t *testing.T) {
	root := ownedTestRoot(t)
	_, pin, err := certificate(root, "https://localhost:19443", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, pin2, err := certificate(root, "https://localhost:19443", "", "")
	if err != nil || pin != pin2 {
		t.Fatal("certificate changed")
	}
	b, _ := os.ReadFile(filepath.Join(root, "tls.pem"))
	block, _ := pem.Decode(b)
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.VerifyHostname("localhost"); err != nil {
		t.Fatal(err)
	}
}
func TestCLIHelpAndResetGuard(t *testing.T) {
	var out bytes.Buffer
	if err := Run([]string{"--help"}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("no help")
	}
	root := ownedTestRoot(t)
	if err := Run([]string{"reset", "--state-dir", root}, nil, &out, &out); err == nil {
		t.Fatal("reset without confirmation")
	}
}
