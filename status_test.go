package easyshift_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift"
)

// TestStatus_BridgeMode_AllGreen confirms a healthy bridge-mode cluster
// produces every expected check and they all pass.
func TestStatus_BridgeMode_AllGreen(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	c := newBridgeModeCluster("good", "br0")
	withDNSRecords(bundle, c)
	bundle.Host.ARPTable = map[string]string{c.MasterMAC: c.MasterIP}
	bundle.Cmd.Output = []byte("running\n") // virsh domstate -> running

	mgr := easyshift.NewClusterManager(cfg, deps)
	if err := mgr.Create(context.Background(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rep, err := mgr.Status(context.Background(), "good")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	wantChecks := []string{
		"VM running",
		"ARP for master MAC",
		"DNS records",
		"API port 6443 (by IP)",
		"API via DNS",
	}
	if got, want := len(rep.Checks), len(wantChecks); got != want {
		t.Fatalf("check count: got %d want %d (%+v)", got, want, rep.Checks)
	}
	for i, name := range wantChecks {
		if rep.Checks[i].Name != name {
			t.Errorf("Checks[%d].Name: got %q want %q", i, rep.Checks[i].Name, name)
		}
		if !rep.Checks[i].OK {
			t.Errorf("expected %q to pass, got fail: %s", name, rep.Checks[i].Detail)
		}
	}

	var buf bytes.Buffer
	rep.Print(&buf)
	out := buf.String()
	if !strings.Contains(out, "[ OK ]") || strings.Contains(out, "[FAIL]") {
		t.Errorf("Print should be all-OK:\n%s", out)
	}
}

// TestStatus_BridgeMode_SurfacesProblems flips each diagnostic into the bad
// state and confirms it shows up as a failure with an actionable hint.
func TestStatus_BridgeMode_SurfacesProblems(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	c := newBridgeModeCluster("bad", "br0")
	withDNSRecords(bundle, c)
	bundle.Host.ARPTable = map[string]string{c.MasterMAC: "10.99.99.99"} // wrong IP
	bundle.Cmd.Output = []byte("shut off\n")
	bundle.Host.TCPReachable = map[string]error{
		c.MasterIP + ":6443":                       errors.New("connection refused"),
		"api." + c.Name + "." + c.Domain + ":6443": errors.New("connection refused"),
	}

	mgr := easyshift.NewClusterManager(cfg, deps)
	if err := mgr.Create(context.Background(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rep, err := mgr.Status(context.Background(), "bad")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	byName := map[string]easyshift.StatusCheck{}
	for _, c := range rep.Checks {
		byName[c.Name] = c
	}

	for _, name := range []string{"VM running", "ARP for master MAC", "API port 6443 (by IP)", "API via DNS"} {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("missing check %q", name)
		}
		if c.OK {
			t.Errorf("expected %q to fail, got pass: %s", name, c.Detail)
		}
		if c.Hint == "" {
			t.Errorf("expected hint on failing check %q", name)
		}
	}
	if !strings.Contains(byName["ARP for master MAC"].Detail, "10.99.99.99") {
		t.Errorf("ARP failure should name the wrong IP: %v", byName["ARP for master MAC"])
	}
}
