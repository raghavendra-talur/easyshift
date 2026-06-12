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
