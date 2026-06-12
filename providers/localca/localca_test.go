package localca

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// parseAll decodes every PEM block into certificates.
func parseAll(t *testing.T, pemBytes []byte) []*x509.Certificate {
	t.Helper()
	var certs []*x509.Certificate
	for rest := pemBytes; ; {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse cert: %v", err)
		}
		certs = append(certs, c)
		rest = r
	}
	return certs
}

func TestIssue_CreatesCAAndValidChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	iss := New(dir)

	certPEM, keyPEM, err := iss.Issue(context.Background(), []string{"api.dr1.example.test"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	certs := parseAll(t, certPEM)
	if len(certs) != 2 {
		t.Fatalf("expected leaf+CA in chain, got %d certs", len(certs))
	}
	leaf, ca := certs[0], certs[1]

	if !ca.IsCA || ca.Subject.CommonName != "easyshift local CA" {
		t.Errorf("CA cert wrong: IsCA=%t CN=%q", ca.IsCA, ca.Subject.CommonName)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "api.dr1.example.test" {
		t.Errorf("leaf SANs = %v", leaf.DNSNames)
	}
	// Leaf validity must respect Apple's 825-day cap for private CAs.
	if max := time.Now().Add(826 * 24 * time.Hour); leaf.NotAfter.After(max) {
		t.Errorf("leaf NotAfter %v exceeds 825 days", leaf.NotAfter)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:   pool,
		DNSName: "api.dr1.example.test",
	}); err != nil {
		t.Errorf("chain does not verify: %v", err)
	}

	// Key must be parseable PKCS#8.
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		t.Fatal("no PEM block in key")
	}
	if _, err := x509.ParsePKCS8PrivateKey(kb.Bytes); err != nil {
		t.Errorf("key is not PKCS#8: %v", err)
	}

	// CA files on disk with tight modes.
	for _, f := range []string{"ca.crt", "ca.key"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, st.Mode().Perm())
		}
	}
}

func TestIssue_ReusesCAAcrossIssuers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")

	c1, _, err := New(dir).Issue(context.Background(), []string{"api.a.test"})
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := New(dir).Issue(context.Background(), []string{"*.apps.a.test"})
	if err != nil {
		t.Fatal(err)
	}

	ca1 := parseAll(t, c1)[1]
	ca2 := parseAll(t, c2)[1]
	if !ca1.Equal(ca2) {
		t.Error("second issuer should reuse the persisted CA, not mint a new one")
	}

	leaf2 := parseAll(t, c2)[0]
	if len(leaf2.DNSNames) != 1 || leaf2.DNSNames[0] != "*.apps.a.test" {
		t.Errorf("wildcard SAN lost: %v", leaf2.DNSNames)
	}
}

func TestEnsureCA_GeneratesWithoutIssuing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	path, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if path != filepath.Join(dir, "ca.crt") {
		t.Errorf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if certs := parseAll(t, data); len(certs) != 1 || !certs[0].IsCA {
		t.Error("ca.crt should hold exactly the CA certificate")
	}
	// Second call is a no-op returning the same path.
	again, err := EnsureCA(dir)
	if err != nil || again != path {
		t.Errorf("EnsureCA not idempotent: %q %v", again, err)
	}
}
