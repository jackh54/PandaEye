// Command gen-csr creates a private key and a certificate signing request on the host that will serve
// TLS, so the key never leaves it. Hand the CSR to a CA (for example Cloudflare's Origin CA, which
// signs it for the "Full (strict)" mode) and put the returned certificate next to the key.
//
//	go run . -key tls/origin.key -csr tls/origin.csr -hosts eye.example.com,eye-api.example.com
//
// It refuses to overwrite an existing key. The key is ECDSA P-256 (Cloudflare request type
// "origin-ecc"), written with mode 0600.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	keyPath := flag.String("key", "tls/origin.key", "private key to create (PEM, mode 0600)")
	csrPath := flag.String("csr", "tls/origin.csr", "certificate signing request to write (PEM)")
	hosts := flag.String("hosts", "", "comma-separated DNS names for the certificate")
	flag.Parse()
	if err := run(*keyPath, *csrPath, *hosts); err != nil {
		fmt.Fprintln(os.Stderr, "gen-csr:", err)
		os.Exit(1)
	}
}

func run(keyPath, csrPath, hosts string) error {
	var names []string
	for _, h := range strings.Split(hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			names = append(names, h)
		}
	}
	if len(names) == 0 {
		return errors.New("-hosts is required")
	}
	if _, err := os.Stat(keyPath); err == nil {
		return fmt.Errorf("%s already exists; delete it first to issue a new key", keyPath)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: names[0]},
		DNSNames: names,
	}, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := writePEM(keyPath, "PRIVATE KEY", der, 0o600, os.O_EXCL); err != nil {
		return err
	}
	return writePEM(csrPath, "CERTIFICATE REQUEST", csr, 0o644, os.O_TRUNC)
}

// writePEM creates path; with os.O_EXCL a racing second run cannot replace a key. A CSR is public and
// may be replaced (os.O_TRUNC).
func writePEM(path, typ string, der []byte, mode os.FileMode, flag int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|flag, mode)
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
