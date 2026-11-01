package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func defaultDataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "scratch-link-go")
}

// certOK reports whether the pair loads, covers host and is not about to expire.
func certOK(certPath, keyPath, host string) bool {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	return leaf.VerifyHostname(host) == nil && time.Now().Add(7*24*time.Hour).Before(leaf.NotAfter)
}

// ensureCert returns the certificate and key to serve with, creating them on
// first run (or when they are about to expire).
func ensureCert(cfg config, log *slog.Logger) (certPath, keyPath string, err error) {
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return "", "", errors.New("-cert and -key must be used together")
		}
		if _, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile); err != nil {
			return "", "", err
		}
		return cfg.CertFile, cfg.KeyFile, nil
	}

	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir()
	}
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if certOK(certPath, keyPath, cfg.Host) {
		return certPath, keyPath, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := generateCert(cfg.Host, caPath, certPath, keyPath); err != nil {
		return "", "", err
	}
	log.Info("generated a new certificate", "dir", dir)
	log.Info("to make your browser accept it, do ONE of these:")
	log.Info("  a) open https://" + cfg.Host + ":20110/ and accept the security warning")
	log.Info("  b) import the CA: certutil -d sql:$HOME/.pki/nssdb -A -t \"C,,\" -n scratch-link-go -i " + caPath)
	return certPath, keyPath, nil
}

// generateCert writes a local CA (caPath) and a server certificate signed by
// it (certPath holds leaf+CA, keyPath the leaf key). The leaf is not a CA,
// because Firefox refuses CA certificates used as server certificates.
func generateCert(host, caPath, certPath, keyPath string) error {
	now := time.Now()
	serial := func() *big.Int {
		n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
		return n
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "scratch-link-go local CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return err
	}

	enc := func(typ string, der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}) }
	caPEM := enc("CERTIFICATE", caDER)
	chain := append(enc("CERTIFICATE", leafDER), caPEM...)
	if err := os.WriteFile(caPath, caPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, chain, 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, enc("EC PRIVATE KEY", keyDER), 0o600)
}
