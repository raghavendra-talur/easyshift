// Package localca implements interfaces.CertIssuer backed by a host-local
// self-generated CA (the "easyshift local CA"). One CA, persisted in the
// config dir, signs the api/apps serving certs of every cluster that does
// not use Let's Encrypt; `easyshift trust` installs it into host trust
// stores so browsers accept the console without warnings.
package localca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	caCommonName = "easyshift local CA"
	caValidity   = 10 * 365 * 24 * time.Hour
	// leafValidity stays under Apple's 825-day cap for TLS certs issued by
	// private CAs, so the same certs work when the CLI runs on macOS hosts.
	leafValidity = 825 * 24 * time.Hour
)

// Issuer signs serving certs with the persisted local CA. It implements
// interfaces.CertIssuer.
type Issuer struct {
	dir string
}

// New returns an Issuer rooted at dir (config.LocalCADir). The CA is
// generated lazily on first use.
func New(dir string) *Issuer { return &Issuer{dir: dir} }

func caCertPath(dir string) string { return filepath.Join(dir, "ca.crt") }
func caKeyPath(dir string) string  { return filepath.Join(dir, "ca.key") }

// EnsureCA generates the CA pair if missing and returns the CA cert path.
// Used by `easyshift trust` so trusting can happen before the first cluster.
func EnsureCA(dir string) (string, error) {
	if _, _, err := loadOrCreateCA(dir); err != nil {
		return "", err
	}
	return caCertPath(dir), nil
}

// Issue signs a leaf cert for domains with the local CA. The returned cert
// PEM is leaf followed by the CA cert (full chain for the serving secret);
// the key is PKCS#8.
func (i *Issuer) Issue(_ context.Context, domains []string) ([]byte, []byte, error) {
	if len(domains) == 0 {
		return nil, nil, fmt.Errorf("no domains to issue for")
	}
	caCert, caKey, err := loadOrCreateCA(i.dir)
	if err != nil {
		return nil, nil, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domains[0]},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     domains,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign leaf cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal leaf key: %w", err)
	}

	certPEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...,
	)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// loadOrCreateCA returns the persisted CA, generating and persisting it on
// first use (dir 0700, files 0600).
func loadOrCreateCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBytes, certErr := os.ReadFile(caCertPath(dir))
	keyBytes, keyErr := os.ReadFile(caKeyPath(dir))
	if certErr == nil && keyErr == nil {
		certBlock, _ := pem.Decode(certBytes)
		keyBlock, _ := pem.Decode(keyBytes)
		if certBlock == nil || keyBlock == nil {
			return nil, nil, fmt.Errorf("corrupt local CA in %s (remove ca.crt/ca.key to regenerate)", dir)
		}
		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse local CA cert: %w", err)
		}
		keyAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse local CA key: %w", err)
		}
		key, ok := keyAny.(*ecdsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("local CA key is not ECDSA")
		}
		if !key.PublicKey.Equal(cert.PublicKey) {
			return nil, nil, fmt.Errorf("corrupt local CA in %s (cert/key mismatch; remove ca.crt and ca.key to regenerate)", dir)
		}
		return cert, key, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create CA dir: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("self-sign CA: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(caCertPath(dir), certPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write ca.crt: %w", err)
	}
	if err := os.WriteFile(caKeyPath(dir), keyPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write ca.key: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}
