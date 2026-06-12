package applytlscerts

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

const sampleKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: QUJDREVG
    server: https://api.dr1.example.test:6443
  name: dr1
contexts:
- context:
    cluster: dr1
    user: admin
  name: admin
current-context: admin
`

// TestMakeKubeconfigPublic_StripsCAAndBacksUp confirms that when the kubeconfig
// still embeds the internal CA, the stage backs it up and runs `oc config unset`
// to drop certificate-authority-data for the cluster entry.
func TestMakeKubeconfigPublic_StripsCAAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte(sampleKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &fakes.CommandRunner{}
	s := &Stage{cmd: cmd}

	if err := s.makeKubeconfigPublic(context.Background(), "/usr/bin/oc", kc, "dr1"); err != nil {
		t.Fatalf("makeKubeconfigPublic: %v", err)
	}

	// Backup created with the original (CA-bearing) contents.
	bak, err := os.ReadFile(kc + ".internal-ca")
	if err != nil {
		t.Fatalf("expected internal-ca backup: %v", err)
	}
	if !strings.Contains(string(bak), "certificate-authority-data") {
		t.Error("backup should preserve the internal CA")
	}

	// Exactly one oc invocation: config unset for this cluster entry.
	if len(cmd.Calls) != 1 {
		t.Fatalf("expected 1 oc call, got %d: %+v", len(cmd.Calls), cmd.Calls)
	}
	got := strings.Join(cmd.Calls[0].Args, " ")
	want := "config unset clusters.dr1.certificate-authority-data"
	if !strings.Contains(got, want) {
		t.Errorf("oc args = %q, want substring %q", got, want)
	}
}

// TestMakeKubeconfigPublic_NoopWhenAlreadyStripped confirms idempotency: a
// kubeconfig with no certificate-authority-data triggers neither a backup nor
// an oc call, so resumes are clean.
func TestMakeKubeconfigPublic_NoopWhenAlreadyStripped(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	stripped := strings.ReplaceAll(sampleKubeconfig, "    certificate-authority-data: QUJDREVG\n", "")
	if err := os.WriteFile(kc, []byte(stripped), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &fakes.CommandRunner{}
	s := &Stage{cmd: cmd}

	if err := s.makeKubeconfigPublic(context.Background(), "/usr/bin/oc", kc, "dr1"); err != nil {
		t.Fatalf("makeKubeconfigPublic: %v", err)
	}
	if len(cmd.Calls) != 0 {
		t.Errorf("expected no oc calls when already stripped, got %+v", cmd.Calls)
	}
	if _, err := os.Stat(kc + ".internal-ca"); !os.IsNotExist(err) {
		t.Error("no backup should be written when nothing changed")
	}
}

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
