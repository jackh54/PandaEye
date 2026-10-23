package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesAKeyAndAMatchingCSR(t *testing.T) {
	dir := t.TempDir()
	key, csr := filepath.Join(dir, "tls", "k.pem"), filepath.Join(dir, "tls", "r.csr")
	if err := run(key, csr, "eye.example.com, eye-api.example.com"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode().Perm())
	}
	raw, err := os.ReadFile(csr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(raw)
	req, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if len(req.DNSNames) != 2 || req.DNSNames[1] != "eye-api.example.com" {
		t.Fatalf("names %v", req.DNSNames)
	}
	if err := run(key, csr, "eye.example.com"); err == nil {
		t.Fatal("must refuse to overwrite an existing key")
	}
}
