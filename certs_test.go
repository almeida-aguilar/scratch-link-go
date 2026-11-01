package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	host := "device-manager.scratch.mit.edu"
	if err := generateCert(host, ca, cert, key); err != nil {
		t.Fatal(err)
	}
	if !certOK(cert, key, host) {
		t.Fatal("generated certificate failed certOK")
	}
	if certOK(cert, key, "other.example") {
		t.Error("certificate should not be valid for another host")
	}

	// A browser that trusts ca.pem must accept the server certificate.
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if leaf.IsCA {
		t.Error("leaf must not be a CA (Firefox rejects that)")
	}
	caPEM, _ := os.ReadFile(ca)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("bad CA PEM")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host}); err != nil {
		t.Errorf("chain does not verify against the CA: %v", err)
	}
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
		t.Errorf("key permissions = %v, want 0600", st.Mode().Perm())
	}
}
