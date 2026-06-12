# Local CA, Kubeconfig Merge, and Start Convergence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every non-Let's-Encrypt cluster gets api/apps certs signed by a host-local easyshift CA (trusted once via `easyshift trust`), `create` merges an admin context into the user's kubeconfig, and `start` approves CSRs until the node is Ready.

**Architecture:** New providers `localca` (implements existing `interfaces.CertIssuer`) and `truststore`; generalized `stages/applytlscerts` (issuer selection by `TLSEmail`); new `stages/mergekubeconfig`; start convergence helper in `app`; `trust` command and end-of-create summary in `cmd`. Spec: `docs/superpowers/specs/2026-06-12-local-ca-kubeconfig-access-design.md`.

**Tech Stack:** Go stdlib only (`crypto/x509`, `crypto/ecdsa`); all process execution through `interfaces.CommandRunner`; tests with `providers/fakes`.

**Conventions for every commit in this plan:**
- Run `make test` before committing (it runs gofmt check + vet + `go test ./...`).
- Commit with `git commit -s` and end the message body with `Assisted-by: Claude Code/claude-fable-5`. Do NOT add `Co-Authored-By`.
- Stages may import ONLY `config` + `interfaces`. Providers never import other providers or stages. Only `app` imports concrete providers/stages. `cmd` imports `app`, `config`, `interfaces`, `providers/fakes`.

---

### Task 1: config path helpers for the local CA

**Files:**
- Modify: `config/paths.go`
- Test: `config/paths_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `config/paths_test.go`:

```go
package config

import (
	"path/filepath"
	"testing"
)

func TestLocalCAPaths(t *testing.T) {
	dir := "/home/u/.config/easyshift"
	if got, want := LocalCADir(dir), filepath.Join(dir, "ca"); got != want {
		t.Errorf("LocalCADir = %q, want %q", got, want)
	}
	if got, want := LocalCACertPath(dir), filepath.Join(dir, "ca", "ca.crt"); got != want {
		t.Errorf("LocalCACertPath = %q, want %q", got, want)
	}
	if got, want := LocalCATrustedMarkerPath(dir), filepath.Join(dir, "ca", "trusted"); got != want {
		t.Errorf("LocalCATrustedMarkerPath = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestLocalCAPaths -v`
Expected: FAIL — `undefined: LocalCADir` (compile error).

- [ ] **Step 3: Implement the helpers**

Append to `config/paths.go` (after the `ACMEAccountDir` function):

```go
// LocalCADir is the on-disk home of the host-local easyshift CA that signs
// api/apps serving certs for clusters without Let's Encrypt. Like the ACME
// account dir it is shared by all clusters and outlives any one of them.
func LocalCADir(configDir string) string {
	return filepath.Join(configDir, "ca")
}

// LocalCACertPath is the CA certificate inside LocalCADir. The file name must
// stay in sync with providers/localca, which owns generation.
func LocalCACertPath(configDir string) string {
	return filepath.Join(LocalCADir(configDir), "ca.crt")
}

// LocalCATrustedMarkerPath marks that `easyshift trust` succeeded on this
// host. It only drives the end-of-create hint; it is not a security control.
func LocalCATrustedMarkerPath(configDir string) string {
	return filepath.Join(LocalCADir(configDir), "trusted")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./config/ -run TestLocalCAPaths -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config/paths.go config/paths_test.go
git commit -s -m "config: add path helpers for the host-local CA

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 2: providers/localca — CA generation and cert issuance

**Files:**
- Create: `providers/localca/localca.go`
- Test: `providers/localca/localca_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `providers/localca/localca_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./providers/localca/ -v`
Expected: FAIL — package does not exist / `undefined: New`.

- [ ] **Step 3: Implement the provider**

Create `providers/localca/localca.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./providers/localca/ -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add providers/localca/
git commit -s -m "providers: add localca CertIssuer backed by a host-local CA

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 3: wire NewLocalCertIssuer through Deps, fakes, and production deps

**Files:**
- Modify: `interfaces/deps.go`
- Modify: `providers/fakes/fakes.go` (Bundle, All, WriteTrace)
- Modify: `app/deps.go`

No new behavior to TDD here (pure wiring); existing suites prove nothing broke.

- [ ] **Step 1: Add the Deps field**

In `interfaces/deps.go`, after the `NewCertIssuer` field, add:

```go
	// NewLocalCertIssuer constructs a CertIssuer backed by the host-local
	// easyshift CA rooted at caDir (config.LocalCADir). Used for every
	// cluster without TLSEmail. Function-shaped to mirror NewCertIssuer.
	NewLocalCertIssuer func(caDir string) (CertIssuer, error)
```

- [ ] **Step 2: Wire the fake**

In `providers/fakes/fakes.go`:

Add a `LocalCertIssuer` field to `Bundle` (after `CertIssuer`):

```go
	CertIssuer      *CertIssuer
	LocalCertIssuer *CertIssuer
```

In `All()`, initialize it in the `Bundle` literal:

```go
		CertIssuer:      &CertIssuer{},
		LocalCertIssuer: &CertIssuer{},
```

and add to the returned `interfaces.Deps` (after `NewCertIssuer`):

```go
		NewLocalCertIssuer: func(_ string) (interfaces.CertIssuer, error) {
			return b.LocalCertIssuer, nil
		},
```

In `WriteTrace`, after the existing `b.CertIssuer.Issued` block, add:

```go
	if len(b.LocalCertIssuer.Issued) > 0 {
		fmt.Fprintf(w, "\nLocal-CA TLS certs issued (%d):\n", len(b.LocalCertIssuer.Issued))
		for _, d := range b.LocalCertIssuer.Issued {
			fmt.Fprintf(w, "  %v\n", d)
		}
	}
```

- [ ] **Step 3: Wire production**

In `app/deps.go`, add the import `"github.com/TheEasyShift/easyshift/providers/localca"` and, after the `NewCertIssuer` entry in the `Deps` literal:

```go
		NewLocalCertIssuer: func(caDir string) (interfaces.CertIssuer, error) {
			return localca.New(caDir), nil
		},
```

- [ ] **Step 4: Build and test**

Run: `make test`
Expected: PASS (everything compiles; no behavior change yet)

- [ ] **Step 5: Commit**

```bash
git add interfaces/deps.go providers/fakes/fakes.go app/deps.go
git commit -s -m "app: wire the local-CA cert issuer factory through Deps and fakes

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 4: generalize apply-tls-certs (local CA path + kubeconfig CA append)

**Files:**
- Modify: `stages/applytlscerts/stage.go`
- Modify: `app/manager.go` (buildStages call)
- Test: `stages/applytlscerts/stage_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `stages/applytlscerts/stage_test.go` (note the new imports: add `"encoding/base64"` and the `config`/`interfaces` packages to the import block):

```go
// --- local-CA path -------------------------------------------------------

// newLocalStageEnv builds a StageContext rooted in a temp config dir with a
// kubeconfig on disk, plus a Stage wired with fake issuers.
func newLocalStageEnv(t *testing.T) (*Stage, *interfaces.StageContext, *fakes.CertIssuer, *fakes.CommandRunner) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.NewDefaultConfig(tmp)
	c := &config.ClusterConfig{Name: "dr1", Domain: "example.test", OCPVersion: "4.99.0", MasterCount: 1}
	sc := &interfaces.StageContext{Cluster: c, Config: cfg}

	if err := os.MkdirAll(filepath.Join(sc.ClusterDir(), "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sc.KubeconfigPath(), []byte(sampleKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	local := &fakes.CertIssuer{}
	cmd := &fakes.CommandRunner{}
	s := New(
		func(_ interfaces.CertIssuerOpts) (interfaces.CertIssuer, error) {
			t.Fatal("ACME issuer must not be constructed when TLSEmail is empty")
			return nil, nil
		},
		func(_ string) (interfaces.CertIssuer, error) { return local, nil },
		cmd,
	)
	return s, sc, local, cmd
}

// TestApply_UsesLocalCAWhenNoTLSEmail confirms the stage is no longer a no-op
// without TLSEmail: it issues api+apps certs from the local issuer and drives
// the same secret/patch machinery as the ACME path.
func TestApply_UsesLocalCAWhenNoTLSEmail(t *testing.T) {
	s, sc, local, cmd := newLocalStageEnv(t)

	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(local.Issued) != 2 {
		t.Fatalf("expected 2 issuances (api, apps), got %v", local.Issued)
	}
	if local.Issued[0][0] != "api.dr1.example.test" || local.Issued[1][0] != "*.apps.dr1.example.test" {
		t.Errorf("issued domains = %v", local.Issued)
	}

	joined := ""
	for _, call := range cmd.Calls {
		joined += strings.Join(call.Args, " ") + "\n"
	}
	for _, want := range []string{
		"patch apiserver/cluster",
		"patch ingresscontroller/default",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing oc call %q in:\n%s", want, joined)
		}
	}
}

// --- kubeconfig CA append ------------------------------------------------

const fakeCAPEM = `-----BEGIN CERTIFICATE-----
RkFLRUNB
-----END CERTIFICATE-----
`

const otherCertPEM = `-----BEGIN CERTIFICATE-----
T1RIRVI=
-----END CERTIFICATE-----
`

// TestAppendLocalCA_AppendsAndBacksUp: bundle lacking our CA gets it appended
// via `oc config set`, with the original kubeconfig backed up.
func TestAppendLocalCA_AppendsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte(sampleKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte(fakeCAPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &fakes.CommandRunner{}
	cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "config view") {
			return []byte(base64.StdEncoding.EncodeToString([]byte(otherCertPEM))), nil
		}
		return nil, nil
	}
	s := &Stage{cmd: cmd}

	if err := s.appendLocalCAToKubeconfig(context.Background(), "/usr/bin/oc", kc, "dr1", caPath); err != nil {
		t.Fatalf("appendLocalCAToKubeconfig: %v", err)
	}

	if _, err := os.Stat(kc + ".internal-ca"); err != nil {
		t.Errorf("expected internal-ca backup: %v", err)
	}

	var setCall []string
	for _, call := range cmd.Calls {
		if len(call.Args) > 3 && call.Args[len(call.Args)-2] == "clusters.dr1.certificate-authority-data" {
			setCall = call.Args
		}
	}
	if setCall == nil {
		t.Fatalf("no `oc config set` call recorded: %+v", cmd.Calls)
	}
	wantBundle := base64.StdEncoding.EncodeToString([]byte(otherCertPEM + fakeCAPEM))
	if got := setCall[len(setCall)-1]; got != wantBundle {
		t.Errorf("set bundle = %q, want old+ours = %q", got, wantBundle)
	}
}

// TestAppendLocalCA_IdempotentWhenPresent: a bundle already containing our CA
// triggers no oc config set and no backup.
func TestAppendLocalCA_IdempotentWhenPresent(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte(sampleKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte(fakeCAPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &fakes.CommandRunner{}
	cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "config view") {
			return []byte(base64.StdEncoding.EncodeToString([]byte(otherCertPEM + fakeCAPEM))), nil
		}
		return nil, nil
	}
	s := &Stage{cmd: cmd}

	if err := s.appendLocalCAToKubeconfig(context.Background(), "/usr/bin/oc", kc, "dr1", caPath); err != nil {
		t.Fatalf("appendLocalCAToKubeconfig: %v", err)
	}
	for _, call := range cmd.Calls {
		if strings.Contains(strings.Join(call.Args, " "), "config set ") {
			t.Errorf("unexpected config set call: %v", call.Args)
		}
	}
	if _, err := os.Stat(kc + ".internal-ca"); !os.IsNotExist(err) {
		t.Error("no backup should be written when nothing changed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./stages/applytlscerts/ -v`
Expected: FAIL — `New` has wrong arity / `appendLocalCAToKubeconfig` undefined.

- [ ] **Step 3: Implement the stage changes**

In `stages/applytlscerts/stage.go`:

Update the package comment:

```go
// Package applytlscerts issues the cluster's api + *.apps serving certs and
// patches APIServer + IngressController to serve them. With TLSEmail set it
// uses Let's Encrypt (ACME DNS-01); otherwise it signs with the host-local
// easyshift CA so every cluster gets a cert chain the host can trust.
package applytlscerts
```

Update the struct and constructor:

```go
// Stage issues and applies the cluster's TLS certificates.
type Stage struct {
	newCertIssuer  func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error)
	newLocalIssuer func(caDir string) (interfaces.CertIssuer, error)
	cmd            interfaces.CommandRunner
}

// New returns the apply-tls-certs stage. Both issuer params are factories:
// the ACME one because per-cluster settings (email, staging) aren't known
// until create, the local one to mirror that shape for wiring symmetry.
func New(
	newCertIssuer func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error),
	newLocalIssuer func(caDir string) (interfaces.CertIssuer, error),
	cmd interfaces.CommandRunner,
) *Stage {
	return &Stage{newCertIssuer: newCertIssuer, newLocalIssuer: newLocalIssuer, cmd: cmd}
}
```

Replace `Apply`'s opening (the `TLSEmail == ""` early return plus issuer construction) so issuer selection branches; the issue/plant/patch body stays identical:

```go
func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	var issuer interfaces.CertIssuer
	if sc.Cluster.TLSEmail != "" {
		token, err := config.ReadDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
		if err != nil {
			return err
		}
		issuer, err = s.newCertIssuer(interfaces.CertIssuerOpts{
			Email:       sc.Cluster.TLSEmail,
			AccountDir:  config.ACMEAccountDir(sc.Config.ConfigDir, sc.Cluster.DNSProvider, sc.Cluster.TLSStaging),
			DNSProvider: sc.Cluster.DNSProvider,
			Token:       token,
			Staging:     sc.Cluster.TLSStaging,
		})
		if err != nil {
			return fmt.Errorf("cert issuer: %w", err)
		}
	} else {
		var err error
		issuer, err = s.newLocalIssuer(config.LocalCADir(sc.Config.ConfigDir))
		if err != nil {
			return fmt.Errorf("local cert issuer: %w", err)
		}
	}
	// ... existing body from `tlsDir := ...` through the ingresscontroller
	// patch stays byte-for-byte unchanged ...
```

Replace the trailing `makeKubeconfigPublic` call with the branch:

```go
	// The admin kubeconfig's embedded internal CA no longer validates
	// api.<fqdn> once the named certificate is served. LE path: drop the CA
	// so the system trust store takes over. Local path: append our CA to the
	// bundle (keeping the internal CA valid during the apiserver rollout).
	// Both are best-effort: the cluster is up, so only warn on failure.
	if sc.Cluster.TLSEmail != "" {
		if err := s.makeKubeconfigPublic(ctx, oc, kubeconfig, sc.Cluster.Name); err != nil {
			logrus.Warnf("apply-tls-certs: could not make %s trust the public cert automatically "+
				"(use --insecure-skip-tls-verify, or `oc config unset clusters.%s.certificate-authority-data`): %v",
				kubeconfig, sc.Cluster.Name, err)
		}
	} else {
		caPath := config.LocalCACertPath(sc.Config.ConfigDir)
		if err := s.appendLocalCAToKubeconfig(ctx, oc, kubeconfig, sc.Cluster.Name, caPath); err != nil {
			logrus.Warnf("apply-tls-certs: could not add the easyshift CA to %s "+
				"(oc may report certificate errors; the original is at %s.internal-ca): %v",
				kubeconfig, kubeconfig, err)
		}
	}
	return nil
}
```

Add the new method (alongside `makeKubeconfigPublic`); imports gain `"encoding/base64"`:

```go
// appendLocalCAToKubeconfig appends the easyshift local CA to the admin
// kubeconfig's certificate-authority-data so `oc` validates the local-CA
// serving cert on api.<fqdn>. The internal CA is kept in the bundle (the
// apiserver serves the old cert until its rollout completes). Idempotent:
// once our CA is in the bundle, resumes change nothing.
func (s *Stage) appendLocalCAToKubeconfig(ctx context.Context, oc, kubeconfig, clusterEntry, caCertPath string) error {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("read local CA cert: %w", err)
	}
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		return fmt.Errorf("no PEM block in %s", caCertPath)
	}

	out, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "view", "--raw",
		"-o", `jsonpath={.clusters[?(@.name=="`+clusterEntry+`")].cluster.certificate-authority-data}`)
	if err != nil {
		return fmt.Errorf("read kubeconfig CA bundle: %w", err)
	}
	bundle, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("decode kubeconfig CA bundle: %w", err)
	}
	for rest := bundle; ; {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		if bytes.Equal(block.Bytes, caBlock.Bytes) {
			return nil // already trusted
		}
		rest = r
	}

	orig, err := os.ReadFile(kubeconfig)
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	backup := kubeconfig + ".internal-ca"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(backup, orig, 0o600); err != nil {
			return fmt.Errorf("back up kubeconfig: %w", err)
		}
	}

	newBundle := append(append([]byte{}, bundle...), caPEM...)
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "set",
		"clusters."+clusterEntry+".certificate-authority-data",
		base64.StdEncoding.EncodeToString(newBundle)); err != nil {
		return fmt.Errorf("append CA to kubeconfig: %w", err)
	}
	logrus.Infof("added the easyshift local CA to %s (original saved at %s)", kubeconfig, backup)
	return nil
}
```

Also add `"encoding/pem"` and `"strings"` to imports (bytes/errors already imported).

Update `app/manager.go` buildStages:

```go
		applytlscerts.New(d.NewCertIssuer, d.NewLocalCertIssuer, d.Cmd),
```

The strings import in `stage_test.go` is already present; add `encoding/base64`, `github.com/TheEasyShift/easyshift/config`, `github.com/TheEasyShift/easyshift/interfaces` to the test imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./stages/applytlscerts/ ./app/ -v`
Expected: PASS, including the pre-existing `TestMakeKubeconfigPublic_*` tests and the app suite (the full-pipeline create tests now exercise the local-CA branch via the fake local issuer).

- [ ] **Step 5: Commit**

```bash
git add stages/applytlscerts/ app/manager.go
git commit -s -m "stages: sign api/apps certs with the local CA when LE is unset

apply-tls-certs is no longer a no-op without --tls-email: it issues from
the host-local easyshift CA and appends that CA to the admin kubeconfig
bundle instead of stripping it.

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 5: stages/mergekubeconfig — merge an admin context into the user's kubeconfig

**Files:**
- Create: `stages/mergekubeconfig/stage.go`
- Test: `stages/mergekubeconfig/stage_test.go` (create)
- Modify: `app/manager.go` (buildStages: insert between applytlscerts and finalize)

- [ ] **Step 1: Write the failing tests**

Create `stages/mergekubeconfig/stage_test.go`:

```go
package mergekubeconfig

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func newEnv(t *testing.T) (*Stage, *interfaces.StageContext, *fakes.CommandRunner, string) {
	t.Helper()
	tmp := t.TempDir()
	target := filepath.Join(tmp, "kube", "config")
	t.Setenv("KUBECONFIG", target)

	cfg := config.NewDefaultConfig(tmp)
	c := &config.ClusterConfig{Name: "dr1", Domain: "example.test", OCPVersion: "4.99.0", MasterCount: 1}
	sc := &interfaces.StageContext{Cluster: c, Config: cfg}
	if err := os.MkdirAll(filepath.Join(sc.ClusterDir(), "auth"), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := &fakes.CommandRunner{}
	cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, ".cluster.server"):
			return []byte("https://api.dr1.example.test:6443"), nil
		case strings.Contains(j, "certificate-authority-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CABUNDLE"))), nil
		case strings.Contains(j, "client-certificate-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CLIENTCRT"))), nil
		case strings.Contains(j, "client-key-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CLIENTKEY"))), nil
		case strings.Contains(j, "current-context"):
			return []byte("dr1\n"), nil
		}
		return nil, nil
	}
	return New(cmd), sc, cmd, target
}

func joined(cmd *fakes.CommandRunner) []string {
	var out []string
	for _, c := range cmd.Calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func TestApply_MergesContextAndSetsCurrent(t *testing.T) {
	s, sc, cmd, target := newEnv(t)

	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Client material extracted and decoded to files for --embed-certs.
	crt, err := os.ReadFile(filepath.Join(sc.ClusterDir(), "auth", "client.crt"))
	if err != nil || string(crt) != "CLIENTCRT" {
		t.Errorf("client.crt = %q, %v", crt, err)
	}

	calls := joined(cmd)
	wants := []string{
		"--kubeconfig " + target + " config set-cluster easyshift-dr1 --server=https://api.dr1.example.test:6443",
		"config set-credentials easyshift-dr1-admin",
		"config set-context dr1 --cluster=easyshift-dr1 --user=easyshift-dr1-admin",
		"config use-context dr1",
	}
	for _, want := range wants {
		found := false
		for _, c := range calls {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call containing %q in %v", want, calls)
		}
	}

	// Target parent dir created.
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Errorf("target dir not created: %v", err)
	}
}

func TestRollback_RemovesEntriesAndUnsetsCurrentContext(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)

	if err := s.Rollback(context.Background(), sc); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	calls := joined(cmd)
	wants := []string{
		"config delete-context dr1",
		"config delete-cluster easyshift-dr1",
		"config unset users.easyshift-dr1-admin",
		"config unset current-context", // current-context was "dr1" per the fake
	}
	for _, want := range wants {
		found := false
		for _, c := range calls {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call containing %q in %v", want, calls)
		}
	}
}

func TestRollback_KeepsForeignCurrentContext(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)
	inner := cmd.RunFunc
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "current-context") &&
			!strings.Contains(strings.Join(args, " "), "unset") {
			return []byte("someone-elses-context\n"), nil
		}
		return inner(name, args)
	}

	if err := s.Rollback(context.Background(), sc); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	for _, c := range joined(cmd) {
		if strings.Contains(c, "unset current-context") {
			t.Errorf("must not unset a current-context easyshift doesn't own: %v", c)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./stages/mergekubeconfig/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the stage**

Create `stages/mergekubeconfig/stage.go`:

```go
// Package mergekubeconfig merges the cluster's admin credentials into the
// user's kubeconfig (~/.kube/config or $KUBECONFIG's first path) as a context
// named after the cluster, and makes it the current context — minikube-style
// "kubectl works the moment create returns". Rollback removes exactly the
// entries Apply created, so delete cleans up for free.
package mergekubeconfig

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage merges/removes the cluster's kubeconfig context.
type Stage struct {
	cmd interfaces.CommandRunner
}

// New returns the merge-kubeconfig stage.
func New(cmd interfaces.CommandRunner) *Stage { return &Stage{cmd: cmd} }

func (*Stage) Name() string { return "merge-kubeconfig" }

// targetKubeconfig resolves the user's kubeconfig: the first $KUBECONFIG
// path if set, else ~/.kube/config.
func targetKubeconfig() (string, error) {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		if paths := filepath.SplitList(env); len(paths) > 0 && paths[0] != "" {
			return paths[0], nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".kube", "config"), nil
}

func clusterEntry(name string) string { return "easyshift-" + name }
func userEntry(name string) string    { return "easyshift-" + name + "-admin" }

// Apply extracts the admin client cert/key (and CA bundle, when the admin
// kubeconfig still carries one) and writes set-cluster/set-credentials/
// set-context/use-context entries into the user's kubeconfig. Every step is
// an idempotent `oc config set-*`, so retries are safe. A pre-existing
// foreign context with the cluster's name is overwritten (documented).
func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	target, err := targetKubeconfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create kubeconfig dir: %w", err)
	}

	oc := sc.OCBinaryPath()
	admin := sc.KubeconfigPath()
	name := sc.Cluster.Name
	authDir := filepath.Join(sc.ClusterDir(), "auth")

	server, err := s.jsonpath(ctx, oc, admin, "{.clusters[0].cluster.server}")
	if err != nil {
		return err
	}
	caData, err := s.jsonpath(ctx, oc, admin, "{.clusters[0].cluster.certificate-authority-data}")
	if err != nil {
		return err
	}
	certData, err := s.jsonpath(ctx, oc, admin, "{.users[0].user.client-certificate-data}")
	if err != nil {
		return err
	}
	keyData, err := s.jsonpath(ctx, oc, admin, "{.users[0].user.client-key-data}")
	if err != nil {
		return err
	}

	certPath := filepath.Join(authDir, "client.crt")
	keyPath := filepath.Join(authDir, "client.key")
	if err := writeB64(certPath, certData); err != nil {
		return err
	}
	if err := writeB64(keyPath, keyData); err != nil {
		return err
	}

	setCluster := []string{"--kubeconfig", target, "config", "set-cluster", clusterEntry(name), "--server=" + server}
	if caData != "" {
		caPath := filepath.Join(authDir, "ca-bundle.crt")
		if err := writeB64(caPath, caData); err != nil {
			return err
		}
		setCluster = append(setCluster, "--certificate-authority="+caPath, "--embed-certs=true")
	}
	// No CA data (Let's Encrypt cluster): the entry validates via the system
	// trust store, so no CA flags at all.
	steps := [][]string{
		setCluster,
		{"--kubeconfig", target, "config", "set-credentials", userEntry(name),
			"--client-certificate=" + certPath, "--client-key=" + keyPath, "--embed-certs=true"},
		{"--kubeconfig", target, "config", "set-context", name,
			"--cluster=" + clusterEntry(name), "--user=" + userEntry(name)},
		{"--kubeconfig", target, "config", "use-context", name},
	}
	for _, args := range steps {
		if _, err := s.cmd.Run(ctx, oc, args...); err != nil {
			return fmt.Errorf("oc %s: %w", strings.Join(args[2:4], " "), err)
		}
	}
	logrus.Infof("merged context %q into %s and set it current", name, target)
	return nil
}

// Rollback removes the context/cluster/user entries Apply created and
// unsets current-context only if it still points at our context. Best
// effort by design: a missing kubeconfig or entry must not block delete.
func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	target, err := targetKubeconfig()
	if err != nil {
		logrus.Warnf("merge-kubeconfig rollback: %v", err)
		return nil
	}
	oc := sc.OCBinaryPath()
	name := sc.Cluster.Name

	if out, err := s.cmd.Run(ctx, oc, "--kubeconfig", target, "config", "current-context"); err == nil &&
		strings.TrimSpace(string(out)) == name {
		if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", target, "config", "unset", "current-context"); err != nil {
			logrus.Warnf("merge-kubeconfig rollback: unset current-context: %v", err)
		}
	}
	for _, args := range [][]string{
		{"--kubeconfig", target, "config", "delete-context", name},
		{"--kubeconfig", target, "config", "delete-cluster", clusterEntry(name)},
		{"--kubeconfig", target, "config", "unset", "users." + userEntry(name)},
	} {
		if _, err := s.cmd.Run(ctx, oc, args...); err != nil {
			logrus.Debugf("merge-kubeconfig rollback: oc %v: %v", args, err)
		}
	}
	return nil
}

// jsonpath runs `oc config view --raw -o jsonpath=<expr>` against kubeconfig
// and returns the trimmed output.
func (s *Stage) jsonpath(ctx context.Context, oc, kubeconfig, expr string) (string, error) {
	out, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "view", "--raw", "-o", "jsonpath="+expr)
	if err != nil {
		return "", fmt.Errorf("extract %s from admin kubeconfig: %w", expr, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeB64 decodes base64 data (possibly empty under --simulate) to path.
func writeB64(path, b64 string) error {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("decode for %s: %w", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
```

Insert the stage in `app/manager.go` buildStages between `applytlscerts` and `finalize` (add import `"github.com/TheEasyShift/easyshift/stages/mergekubeconfig"`):

```go
		applytlscerts.New(d.NewCertIssuer, d.NewLocalCertIssuer, d.Cmd),
		mergekubeconfig.New(d.Cmd),
		finalize.New(),
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./stages/mergekubeconfig/ ./app/ -v`
Expected: PASS. NOTE: the app suite's create tests now also run merge-kubeconfig with the fake runner; if any app test asserts an exact stage count or exact `cmd.Calls`, update it to include the new stage. Also set `t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))` in any app-level create/delete test that would otherwise write the developer's real `~/.kube/config` — check `app/cluster_test.go` and add it to test setup where Create or Delete is invoked.

- [ ] **Step 5: Commit**

```bash
git add stages/mergekubeconfig/ app/manager.go app/cluster_test.go
git commit -s -m "stages: merge an admin context into the user kubeconfig on create

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 6: end-of-create summary

**Files:**
- Create: `cmd/easyshift/summary.go`
- Test: `cmd/easyshift/summary_test.go` (create)
- Modify: `cmd/easyshift/main.go` (create RunE)

- [ ] **Step 1: Write the failing test**

Create `cmd/easyshift/summary_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/config"
)

func TestPrintCreateSummary(t *testing.T) {
	cfg := config.NewDefaultConfig(t.TempDir())
	c := &config.ClusterConfig{Name: "dr1", Domain: "example.test"}

	var buf bytes.Buffer
	printCreateSummary(&buf, cfg, c)
	out := buf.String()

	for _, want := range []string{
		`context "dr1"`,
		"https://console-openshift-console.apps.dr1.example.test",
		filepath.Join("clusters", "dr1", "auth", "kubeadmin-password"),
		"easyshift trust", // local-CA cluster, marker absent -> hint
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestPrintCreateSummary_NoTrustHint(t *testing.T) {
	cfg := config.NewDefaultConfig(t.TempDir())

	// Case 1: CA already trusted (marker present).
	if err := os.MkdirAll(config.LocalCADir(cfg.ConfigDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.LocalCATrustedMarkerPath(cfg.ConfigDir), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printCreateSummary(&buf, cfg, &config.ClusterConfig{Name: "dr1", Domain: "example.test"})
	if strings.Contains(buf.String(), "easyshift trust") {
		t.Error("no trust hint expected when the marker exists")
	}

	// Case 2: Let's Encrypt cluster — publicly trusted, no hint regardless.
	buf.Reset()
	printCreateSummary(&buf, config.NewDefaultConfig(t.TempDir()),
		&config.ClusterConfig{Name: "dr1", Domain: "example.test", TLSEmail: "a@b.c"})
	if strings.Contains(buf.String(), "easyshift trust") {
		t.Error("no trust hint expected for an LE cluster")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/easyshift/ -run TestPrintCreateSummary -v`
Expected: FAIL — `undefined: printCreateSummary`.

- [ ] **Step 3: Implement**

Create `cmd/easyshift/summary.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/TheEasyShift/easyshift/config"
)

// printCreateSummary tells the user how to reach the cluster Create just
// finished: the kubeconfig context that is now current, the console URL,
// where the kubeadmin password lives (path only — never the secret), and a
// one-time `easyshift trust` hint for local-CA clusters.
func printCreateSummary(w io.Writer, cfg *config.Config, c *config.ClusterConfig) {
	fqdn := c.FQDN()
	fmt.Fprintf(w, "\nCluster %s is ready.\n", c.Name)
	fmt.Fprintf(w, "  kubectl/oc: context %q is merged into your kubeconfig and set as current\n", c.Name)
	fmt.Fprintf(w, "  console:    https://console-openshift-console.apps.%s\n", fqdn)
	fmt.Fprintf(w, "  kubeadmin:  password file %s\n",
		filepath.Join(config.ClusterDir(cfg.ConfigDir, c.Name), "auth", "kubeadmin-password"))
	if c.TLSEmail == "" {
		if _, err := os.Stat(config.LocalCATrustedMarkerPath(cfg.ConfigDir)); err != nil {
			fmt.Fprintf(w, "  tip:        run `easyshift trust` once to remove browser TLS warnings (uses sudo)\n")
		}
	}
}
```

In `cmd/easyshift/main.go`, change the end of the create `RunE` from `return (*mgr).Create(context.Background(), c)` to:

```go
			if err := (*mgr).Create(context.Background(), c); err != nil {
				return err
			}
			// Create may have resumed an existing cluster (a different
			// *ClusterConfig than ours) — re-find it for accurate fields.
			for _, cl := range (*mgr).List() {
				if cl.Name == name {
					printCreateSummary(os.Stdout, *cfgp, cl)
					break
				}
			}
			return nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/easyshift/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/easyshift/summary.go cmd/easyshift/summary_test.go cmd/easyshift/main.go
git commit -s -m "cmd: print access summary at the end of create

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 7: TrustStoreInstaller interface + providers/truststore + fakes

**Files:**
- Modify: `interfaces/interfaces.go`, `interfaces/deps.go`
- Create: `providers/truststore/truststore.go`
- Test: `providers/truststore/truststore_test.go` (create)
- Modify: `providers/fakes/fakes.go`, `app/deps.go`

- [ ] **Step 1: Add the interface and Deps field**

In `interfaces/interfaces.go`, after `CertIssuerOpts`, add:

```go
// TrustStoreInstaller installs/removes the easyshift local CA in the host's
// trust stores (system store via sudo, NSS databases via certutil when
// present). Consumed only by cmd (the `trust` command) — never by stages.
type TrustStoreInstaller interface {
	Install(ctx context.Context, caCertPath string) error
	Uninstall(ctx context.Context, caCertPath string) error
}
```

In `interfaces/deps.go`, after `PullSecret`:

```go
	// TrustStore installs the local CA into host trust stores. Consumed only
	// by cmd (`easyshift trust`) — never by stages.
	TrustStore TrustStoreInstaller
```

- [ ] **Step 2: Write the failing provider tests**

Create `providers/truststore/truststore_test.go`:

```go
package truststore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func calls(cmd *fakes.CommandRunner) []string {
	var out []string
	for _, c := range cmd.Calls {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

func newTestInstaller(t *testing.T, goos string, existing ...string) (*Installer, *fakes.CommandRunner, string) {
	t.Helper()
	home := t.TempDir()
	cmd := &fakes.CommandRunner{}
	exists := map[string]bool{}
	for _, p := range existing {
		exists[p] = true
	}
	ins := New(cmd)
	ins.goos = goos
	ins.home = home
	ins.pathExists = func(p string) bool {
		if strings.HasPrefix(p, home) {
			_, err := os.Stat(p)
			return err == nil
		}
		return exists[p]
	}
	ins.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return ins, cmd, home
}

func TestInstall_FedoraFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := calls(cmd)
	want := []string{
		"sudo cp /cfg/ca/ca.crt /etc/pki/ca-trust/source/anchors/easyshift-local-ca.crt",
		"sudo update-ca-trust extract",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("call %d = %v, want %q", i, got, w)
		}
	}
}

func TestInstall_DebianFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/usr/local/share/ca-certificates")

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "/usr/local/share/ca-certificates/easyshift-local-ca.crt") ||
		!strings.Contains(got, "sudo update-ca-certificates") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestInstall_NoKnownStore(t *testing.T) {
	ins, _, _ := newTestInstaller(t, "linux")
	err := ins.Install(context.Background(), "/cfg/ca/ca.crt")
	if err == nil || !strings.Contains(err.Error(), "/etc/pki/ca-trust/source/anchors") {
		t.Errorf("want error naming the expected locations, got %v", err)
	}
}

func TestInstall_Darwin(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "darwin")
	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /cfg/ca/ca.crt") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestInstall_NSSDatabases(t *testing.T) {
	ins, cmd, home := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	ins.lookPath = func(string) (string, error) { return "/usr/bin/certutil", nil }

	// One user NSS db + one Firefox profile with cert9.db.
	nss := filepath.Join(home, ".pki", "nssdb")
	profile := filepath.Join(home, ".mozilla", "firefox", "abc.default")
	for _, d := range []string{nss, profile} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "cert9.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	for _, want := range []string{
		"certutil -A -d sql:" + nss,
		"certutil -A -d sql:" + profile,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUninstall_FedoraFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	if err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "sudo rm -f /etc/pki/ca-trust/source/anchors/easyshift-local-ca.crt") ||
		!strings.Contains(got, "sudo update-ca-trust extract") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./providers/truststore/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Implement the provider**

Create `providers/truststore/truststore.go`:

```go
// Package truststore installs the easyshift local CA into the host's trust
// stores: the system store (sudo: update-ca-trust on Fedora/RHEL,
// update-ca-certificates on Debian-family, `security` on macOS) and — when
// certutil is available — the NSS databases Chrome and Firefox actually
// read on Linux. All execution goes through CommandRunner so --simulate
// traces it and tests assert exact invocations; sudo prompts on /dev/tty,
// so captured output does not break password entry.
package truststore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/interfaces"
)

const (
	anchorName  = "easyshift-local-ca.crt"
	nssNickname = "easyshift local CA"

	fedoraAnchors = "/etc/pki/ca-trust/source/anchors"
	debianAnchors = "/usr/local/share/ca-certificates"
)

// Installer is the real interfaces.TrustStoreInstaller. The probe fields are
// injectable so tests pin the platform.
type Installer struct {
	cmd        interfaces.CommandRunner
	goos       string
	home       string
	pathExists func(string) bool
	lookPath   func(string) (string, error)
}

// New returns an Installer for the current host.
func New(cmd interfaces.CommandRunner) *Installer {
	home, _ := os.UserHomeDir()
	return &Installer{
		cmd:  cmd,
		goos: runtime.GOOS,
		home: home,
		pathExists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		lookPath: exec.LookPath,
	}
}

// Install adds the CA to the system store (fatal on failure) and to any NSS
// databases found (best-effort).
func (t *Installer) Install(ctx context.Context, caCertPath string) error {
	if err := t.installSystem(ctx, caCertPath); err != nil {
		return err
	}
	t.eachNSSDB(ctx, func(dir string) error {
		_, err := t.cmd.Run(ctx, "certutil", "-A", "-d", "sql:"+dir, "-t", "C,,", "-n", nssNickname, "-i", caCertPath)
		return err
	})
	return nil
}

// Uninstall reverses Install, tolerating absence at every step.
func (t *Installer) Uninstall(ctx context.Context, caCertPath string) error {
	switch t.goos {
	case "darwin":
		if _, err := t.cmd.Run(ctx, "sudo", "security", "delete-certificate", "-c", nssNickname,
			"/Library/Keychains/System.keychain"); err != nil {
			logrus.Warnf("remove CA from system keychain: %v", err)
		}
	default:
		switch {
		case t.pathExists(fedoraAnchors):
			if _, err := t.cmd.Run(ctx, "sudo", "rm", "-f", filepath.Join(fedoraAnchors, anchorName)); err != nil {
				return err
			}
			if _, err := t.cmd.Run(ctx, "sudo", "update-ca-trust", "extract"); err != nil {
				return err
			}
		case t.pathExists(debianAnchors):
			if _, err := t.cmd.Run(ctx, "sudo", "rm", "-f", filepath.Join(debianAnchors, anchorName)); err != nil {
				return err
			}
			if _, err := t.cmd.Run(ctx, "sudo", "update-ca-certificates"); err != nil {
				return err
			}
		}
	}
	t.eachNSSDB(ctx, func(dir string) error {
		_, err := t.cmd.Run(ctx, "certutil", "-D", "-d", "sql:"+dir, "-n", nssNickname)
		return err
	})
	_ = caCertPath
	return nil
}

func (t *Installer) installSystem(ctx context.Context, caCertPath string) error {
	if t.goos == "darwin" {
		_, err := t.cmd.Run(ctx, "sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain", caCertPath)
		return err
	}
	switch {
	case t.pathExists(fedoraAnchors):
		if _, err := t.cmd.Run(ctx, "sudo", "cp", caCertPath, filepath.Join(fedoraAnchors, anchorName)); err != nil {
			return err
		}
		_, err := t.cmd.Run(ctx, "sudo", "update-ca-trust", "extract")
		return err
	case t.pathExists(debianAnchors):
		if _, err := t.cmd.Run(ctx, "sudo", "cp", caCertPath, filepath.Join(debianAnchors, anchorName)); err != nil {
			return err
		}
		_, err := t.cmd.Run(ctx, "sudo", "update-ca-certificates")
		return err
	default:
		return fmt.Errorf("no known system trust store found (looked for %s and %s)", fedoraAnchors, debianAnchors)
	}
}

// eachNSSDB runs fn for every NSS database dir on the host (no sudo: the
// databases are user-owned). Missing certutil downgrades to an info message.
func (t *Installer) eachNSSDB(ctx context.Context, fn func(dir string) error) {
	if _, err := t.lookPath("certutil"); err != nil {
		logrus.Info("certutil not found; Firefox/Chrome may still warn about the console. " +
			"Install nss-tools (Fedora) or libnss3-tools (Debian) and re-run `easyshift trust`.")
		return
	}
	var dirs []string
	if d := filepath.Join(t.home, ".pki", "nssdb"); t.pathExists(d) {
		dirs = append(dirs, d)
	}
	for _, glob := range []string{
		filepath.Join(t.home, ".mozilla", "firefox", "*"),
		filepath.Join(t.home, "Library", "Application Support", "Firefox", "Profiles", "*"),
	} {
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			if t.pathExists(filepath.Join(m, "cert9.db")) {
				dirs = append(dirs, m)
			}
		}
	}
	for _, dir := range dirs {
		if err := fn(dir); err != nil {
			logrus.Warnf("certutil in %s: %v", dir, err)
		}
	}
}
```

- [ ] **Step 5: Wire fake and production**

In `providers/fakes/fakes.go` add (near `PullSecretFetcher`):

```go
// TrustStore is a fake interfaces.TrustStoreInstaller recording calls.
type TrustStore struct {
	mu          sync.Mutex
	Installed   []string // caCertPath per Install call
	Uninstalled []string // caCertPath per Uninstall call
	Err         error
}

func (t *TrustStore) Install(_ context.Context, caCertPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Installed = append(t.Installed, caCertPath)
	return t.Err
}

func (t *TrustStore) Uninstall(_ context.Context, caCertPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Uninstalled = append(t.Uninstalled, caCertPath)
	return t.Err
}
```

Add `TrustStore *TrustStore` to `Bundle`, `TrustStore: &TrustStore{}` to the literal in `All()`, `TrustStore: b.TrustStore` to the returned Deps, and in `WriteTrace` (after the CSR/hostname block):

```go
	if len(b.TrustStore.Installed)+len(b.TrustStore.Uninstalled) > 0 {
		fmt.Fprintf(w, "\nTrust store: installs=%v uninstalls=%v\n",
			b.TrustStore.Installed, b.TrustStore.Uninstalled)
	}
```

In `app/deps.go` add the import `"github.com/TheEasyShift/easyshift/providers/truststore"` and to the Deps literal:

```go
		TrustStore: truststore.New(cmd),
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./providers/truststore/ ./providers/fakes/ ./app/ -v && make test`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add interfaces/ providers/truststore/ providers/fakes/fakes.go app/deps.go
git commit -s -m "providers: add trust-store installer for the local CA

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 8: `easyshift trust` command

**Files:**
- Create: `cmd/easyshift/trust_flow.go`
- Test: `cmd/easyshift/trust_flow_test.go` (create)
- Modify: `app/manager.go` or new `app/localca.go` (EnsureLocalCA helper), `cmd/easyshift/main.go` (register command)

- [ ] **Step 1: Write the failing test**

Create `cmd/easyshift/trust_flow_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func TestRunTrust_InstallsAndMarks(t *testing.T) {
	cfg := config.NewDefaultConfig(t.TempDir())
	ts := &fakes.TrustStore{}
	var out bytes.Buffer

	if err := runTrust(context.Background(), cfg, ts, false, &out); err != nil {
		t.Fatalf("runTrust: %v", err)
	}

	if len(ts.Installed) != 1 || ts.Installed[0] != config.LocalCACertPath(cfg.ConfigDir) {
		t.Errorf("Installed = %v", ts.Installed)
	}
	// CA generated on demand.
	if _, err := os.Stat(config.LocalCACertPath(cfg.ConfigDir)); err != nil {
		t.Errorf("CA not generated: %v", err)
	}
	// Marker written (drives the end-of-create hint).
	if _, err := os.Stat(config.LocalCATrustedMarkerPath(cfg.ConfigDir)); err != nil {
		t.Errorf("trusted marker missing: %v", err)
	}
}

func TestRunTrust_Uninstall(t *testing.T) {
	cfg := config.NewDefaultConfig(t.TempDir())
	ts := &fakes.TrustStore{}
	var out bytes.Buffer

	if err := runTrust(context.Background(), cfg, ts, false, &out); err != nil {
		t.Fatal(err)
	}
	if err := runTrust(context.Background(), cfg, ts, true, &out); err != nil {
		t.Fatalf("runTrust --uninstall: %v", err)
	}
	if len(ts.Uninstalled) != 1 {
		t.Errorf("Uninstalled = %v", ts.Uninstalled)
	}
	if _, err := os.Stat(config.LocalCATrustedMarkerPath(cfg.ConfigDir)); !os.IsNotExist(err) {
		t.Error("marker should be removed on uninstall")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/easyshift/ -run TestRunTrust -v`
Expected: FAIL — `undefined: runTrust`.

- [ ] **Step 3: Implement**

Create `app/localca.go` (cmd must not import concrete providers; app is the assembler):

```go
package app

import (
	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/localca"
)

// EnsureLocalCA generates the host-local CA if missing and returns the CA
// cert path. Exposed here so cmd (the `trust` command) never imports a
// concrete provider.
func EnsureLocalCA(cfg *config.Config) (string, error) {
	return localca.EnsureCA(config.LocalCADir(cfg.ConfigDir))
}
```

Create `cmd/easyshift/trust_flow.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/app"
	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

// runTrust generates the local CA if needed and installs (or removes) it in
// the host trust stores. Separated from cobra wiring for testability, like
// the pull-secret flow.
func runTrust(ctx context.Context, cfg *config.Config, ts interfaces.TrustStoreInstaller, uninstall bool, out io.Writer) error {
	caPath, err := app.EnsureLocalCA(cfg)
	if err != nil {
		return err
	}
	marker := config.LocalCATrustedMarkerPath(cfg.ConfigDir)

	if uninstall {
		if err := ts.Uninstall(ctx, caPath); err != nil {
			return err
		}
		_ = os.Remove(marker)
		fmt.Fprintln(out, "easyshift local CA removed from the host trust stores")
		return nil
	}

	if err := ts.Install(ctx, caPath); err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte("trusted\n"), 0o600); err != nil {
		logrus.Warnf("write trust marker: %v", err)
	}
	fmt.Fprintf(out, "easyshift local CA (%s) installed into the host trust stores\n", caPath)
	fmt.Fprintln(out, "Browsers now trust the console of every easyshift cluster on this host.")
	return nil
}
```

In `cmd/easyshift/main.go`, add a constructor and register it in `rootCmd.AddCommand(...)` (add `newTrustCommand(&cfg, &deps),` after `newDNSCommand(cfg),`):

```go
func newTrustCommand(cfgp **config.Config, depsp *interfaces.Deps) *cobra.Command {
	var uninstall bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Install the easyshift local CA into the host trust stores (uses sudo)",
		Long: "Installs the easyshift local CA — which signs the api/apps certificates of every\n" +
			"cluster created without --tls-email — into the system trust store (via sudo) and,\n" +
			"when certutil is available, the NSS databases used by Firefox and Chrome.\n" +
			"One-time per host; reversible with --uninstall.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runTrust(context.Background(), *cfgp, depsp.TrustStore, uninstall, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Remove the easyshift local CA from the host trust stores")
	return cmd
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/easyshift/ ./app/ -v`
Expected: PASS

- [ ] **Step 5: Smoke-check the simulate path**

```bash
make build && ./easyshift trust --simulate
```
Expected: trace shows `Trust store: installs=[...]` and exit 0.

- [ ] **Step 6: Commit**

```bash
git add app/localca.go cmd/easyshift/trust_flow.go cmd/easyshift/trust_flow_test.go cmd/easyshift/main.go
git commit -s -m "cmd: add 'easyshift trust' to install the local CA on the host

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 9: start convergence (API wait + CSR approval + node Ready)

**Files:**
- Create: `app/converge.go`
- Test: `app/converge_test.go` (create)
- Modify: `app/manager.go` (Start), `providers/fakes/fakes.go` (Ready jsonpath default), `cmd/easyshift/main.go` (start command message)

- [ ] **Step 1: Make the fake runner converge-friendly**

In `providers/fakes/fakes.go`, in `CommandRunner.Run`, between the `RunFunc` check and the final return, insert:

```go
	// Default node-Ready answer so simulate/full-pipeline tests converge
	// instantly (same spirit as the ssh-keygen side effect above). Tests that
	// need an un-Ready node set RunFunc.
	if len(c.Output) == 0 && c.Err == nil {
		for _, a := range args {
			if strings.Contains(a, `?(@.type=="Ready")`) {
				return []byte("True"), nil
			}
		}
	}
```

- [ ] **Step 2: Write the failing tests**

Create `app/converge_test.go`:

```go
package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func newStartEnv(t *testing.T) (*ClusterManager, *fakes.Bundle) {
	t.Helper()
	deps, bundle := fakes.All()
	cfg := config.NewDefaultConfig(t.TempDir())
	cfg.Clusters = []*config.ClusterConfig{{
		Name: "dr1", Domain: "example.test", OCPVersion: "4.99.0",
		MasterCount: 1, State: config.ClusterStateStopped,
	}}
	return NewClusterManager(cfg, deps), bundle
}

func shortTimeouts(t *testing.T) {
	t.Helper()
	oldAPI, oldConv, oldPoll := apiWaitTimeout, convergeTimeout, convergePollInterval
	apiWaitTimeout, convergeTimeout, convergePollInterval = 200*time.Millisecond, 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { apiWaitTimeout, convergeTimeout, convergePollInterval = oldAPI, oldConv, oldPoll })
}

// TestStart_ConvergesAndApprovesCSRs: happy path — API answers, node Ready,
// the CSR approver was launched, no error.
func TestStart_ConvergesAndApprovesCSRs(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)

	if err := mgr.Start(context.Background(), "dr1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !bundle.CSR.WasStarted() {
		t.Error("CSR approver should run during start convergence")
	}
	if got := mgr.cfg.Clusters[0].State; got != config.ClusterStateRunning {
		t.Errorf("state = %q, want running", got)
	}
}

// TestStart_TimesOutWhenNodeNotReady: node stays NotReady -> Start returns an
// error naming the condition, but the cluster stays marked running (VMs are
// up; it may still recover).
func TestStart_TimesOutWhenNodeNotReady(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)
	bundle.Cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		for _, a := range args {
			if strings.Contains(a, `?(@.type=="Ready")`) {
				return []byte("False"), nil
			}
		}
		return nil, nil // readyz + csr checks succeed
	}

	err := mgr.Start(context.Background(), "dr1")
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("want converge timeout error, got %v", err)
	}
	if got := mgr.cfg.Clusters[0].State; got != config.ClusterStateRunning {
		t.Errorf("state = %q, want running despite convergence timeout", got)
	}
}

// TestStart_TimesOutWhenAPINeverUp: readyz keeps failing -> API wait error.
func TestStart_TimesOutWhenAPINeverUp(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)
	bundle.Cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "/readyz" {
				return nil, context.DeadlineExceeded
			}
		}
		return nil, nil
	}

	err := mgr.Start(context.Background(), "dr1")
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("want API wait error, got %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./app/ -run TestStart_ -v`
Expected: FAIL — `undefined: apiWaitTimeout` etc.

- [ ] **Step 4: Implement convergence**

Create `app/converge.go`:

```go
package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/config"
)

// Convergence tuning. Vars (not consts) so tests shrink them.
var (
	apiWaitTimeout       = 15 * time.Minute // SNO cold boot is slow
	convergeTimeout      = 10 * time.Minute // after the API is up
	convergePollInterval = 10 * time.Second
)

// convergeAfterStart waits for the API to come back after a VM boot, then
// approves pending node CSRs (the kubelet's client cert can expire while the
// VM is off; until the renewal CSRs are approved the node never rejoins)
// until the node reports Ready. The stopped-cluster analog of the approver
// that runs during install.
func (cm *ClusterManager) convergeAfterStart(ctx context.Context, c *config.ClusterConfig) error {
	oc := filepath.Join(config.BinariesDir(cm.cfg.ConfigDir, c.OCPVersion), "oc")
	kubeconfig := filepath.Join(config.ClusterDir(cm.cfg.ConfigDir, c.Name), "auth", "kubeconfig")

	if err := cm.waitForAPI(ctx, oc, kubeconfig); err != nil {
		return err
	}

	csrCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = cm.deps.CSR.Run(csrCtx, oc, kubeconfig) }()

	deadline := time.Now().Add(convergeTimeout)
	for {
		ready := cm.nodeReady(ctx, oc, kubeconfig)
		pending := cm.csrsPending(ctx, oc, kubeconfig)
		if ready && !pending {
			logrus.Infof("cluster %s converged: node Ready, no pending CSRs", c.Name)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster %s did not converge within %s (node Ready=%t, CSRs pending=%t); "+
				"it may still recover on its own — check `easyshift status %s`",
				c.Name, convergeTimeout, ready, pending, c.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(convergePollInterval):
		}
	}
}

// waitForAPI polls /readyz until the kube-apiserver answers.
func (cm *ClusterManager) waitForAPI(ctx context.Context, oc, kubeconfig string) error {
	deadline := time.Now().Add(apiWaitTimeout)
	for {
		if _, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "--raw", "/readyz"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("API did not become ready within %s after VM start", apiWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(convergePollInterval):
		}
	}
}

// nodeReady reports whether every node's Ready condition is True.
func (cm *ClusterManager) nodeReady(ctx context.Context, oc, kubeconfig string) bool {
	out, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "nodes",
		"-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != "True" {
			return false
		}
	}
	return true
}

// csrsPending reports whether `oc get csr` lists any Pending request.
func (cm *ClusterManager) csrsPending(ctx context.Context, oc, kubeconfig string) bool {
	out, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "csr")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Pending")
}
```

In `app/manager.go`, change the tail of `Start` from:

```go
	c.State = config.ClusterStateRunning
	return cm.cfg.Save()
```

to:

```go	
	c.State = config.ClusterStateRunning
	if err := cm.cfg.Save(); err != nil {
		return err
	}
	// Convergence failure does not unmark running: the VMs are up and a
	// slow cluster may still recover; status stays accurate either way.
	return cm.convergeAfterStart(ctx, c)
```

In `cmd/easyshift/main.go`, update the start command's RunE to report success:

```go
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := (*mgr).Start(context.Background(), args[0]); err != nil {
					return err
				}
				fmt.Printf("cluster %s started: API up, node Ready, CSRs approved\n", args[0])
				return nil
			},
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./app/ -v`
Expected: PASS, including pre-existing Start/Stop tests (the fake's Ready default makes them converge instantly).

- [ ] **Step 6: Commit**

```bash
git add app/converge.go app/converge_test.go app/manager.go providers/fakes/fakes.go cmd/easyshift/main.go
git commit -s -m "app: converge on start — wait for API, approve CSRs, node Ready

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 10: docs + final check

**Files:**
- Create: `docs/user/access.md`
- Modify: `docs/README.md` (add link; follow the existing list format)
- Modify: `docs/dev/stages.md` (apply-tls-certs description + new merge-kubeconfig entry; follow the existing per-stage format)

- [ ] **Step 1: Write the user doc**

Create `docs/user/access.md`:

```markdown
# Accessing your cluster

## kubectl / oc — zero setup

`easyshift create` merges an admin context named after the cluster into your
kubeconfig (`$KUBECONFIG`'s first path, else `~/.kube/config`) and makes it
the current context. When create finishes, `oc get nodes` just works.

- The context is named `<cluster-name>`; the cluster/user entries are
  prefixed `easyshift-` to avoid colliding with entries you manage yourself.
- A pre-existing context with the same name as your cluster is overwritten.
- `easyshift delete` removes exactly the entries it added and resets
  `current-context` only if it still points at the deleted cluster.

The raw admin kubeconfig stays at
`~/.config/easyshift/clusters/<name>/auth/kubeconfig`; the original
(internal-CA) copy is preserved at `kubeconfig.internal-ca`.

## Console login

The console URL and the kubeadmin password file path are printed at the end
of `create`. Log in as `kubeadmin` with the password from
`~/.config/easyshift/clusters/<name>/auth/kubeadmin-password`.

## TLS: the easyshift local CA

Clusters created **without** `--tls-email` get `api.<fqdn>` and
`*.apps.<fqdn>` certificates signed by a CA generated once per host at
`~/.config/easyshift/ca/`. To make browsers trust the console (no TLS
warnings), run once:

    easyshift trust

This uses `sudo` to install the CA into the system trust store
(Fedora/RHEL: `update-ca-trust`; Debian/Ubuntu: `update-ca-certificates`;
macOS: the System keychain) and — when `certutil` is installed — into the
NSS databases Firefox and Chrome read on Linux. Without `certutil`
(package `nss-tools` on Fedora, `libnss3-tools` on Debian) those browsers
may still warn.

Reverse it anytime with `easyshift trust --uninstall`.

Clusters created **with** `--tls-email` use Let's Encrypt instead and need no
trust step.

Note: the local CA's key lives unencrypted (mode 0600) in your config dir,
and anything it signs is trusted once you run `easyshift trust`. That is the
same trade-off minikube and mkcert make — fine for throwaway dev clusters on
your own machine; don't copy the key elsewhere.

## start waits for the node

`easyshift start` boots the VM, waits for the API, approves pending
certificate signing requests (a node whose VM was off across a kubelet
cert rotation can't rejoin until its renewal CSRs are approved), and returns
once the node is Ready. If convergence times out the cluster stays running —
check `easyshift status <name>`.
```

- [ ] **Step 2: Link it and update the stage docs**

- In `docs/README.md`: add an entry for `user/access.md` ("Accessing your cluster — kubeconfig contexts, console login, `easyshift trust`, TLS") in the user-docs list, matching the file's existing format.
- In `docs/dev/stages.md`: update the `apply-tls-certs` entry to say it always runs — Let's Encrypt when `TLSEmail` is set, the host-local easyshift CA otherwise — and appends the local CA to the admin kubeconfig bundle (vs. stripping for LE). Add a `merge-kubeconfig` entry after it: "Merges an admin context (named after the cluster, `easyshift-` prefixed cluster/user entries) into the user's kubeconfig via `oc config set-*` and sets current-context; Rollback removes exactly those entries." Match the file's existing per-stage format.

- [ ] **Step 3: Full check**

Run: `make check`
Expected: lint + build + full test suite PASS.

Also smoke the pipeline end to end:

```bash
KUBECONFIG=$(mktemp -d)/config ./easyshift create --name sim1 --simulate
```
Expected: trace shows local-CA cert issuance, merge-kubeconfig `oc config` calls, and the create summary; exit 0.

- [ ] **Step 4: Commit**

```bash
git add docs/
git commit -s -m "docs: document cluster access, easyshift trust, and start convergence

Assisted-by: Claude Code/claude-fable-5"
```
