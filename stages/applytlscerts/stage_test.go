package applytlscerts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift/providers/fakes"
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
