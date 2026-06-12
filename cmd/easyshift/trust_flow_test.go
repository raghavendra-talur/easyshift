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
