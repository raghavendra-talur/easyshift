package embedignitioniso

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
	"github.com/raghavendra-talur/easyshift/providers/fakes"
)

func newStageContext(t *testing.T, c *config.ClusterConfig) *interfaces.StageContext {
	t.Helper()
	dir := t.TempDir()
	cfg := config.NewDefaultConfig(dir)
	cfg.Clusters = []*config.ClusterConfig{c}
	return &interfaces.StageContext{Cluster: c, Config: cfg}
}

func bridgeCluster() *config.ClusterConfig {
	return &config.ClusterConfig{
		Name:        "dr1",
		Domain:      "rtalur.dev",
		NetworkMode: config.NetworkModeBridge,
		Bridge:      "br0",
		MasterMAC:   "52:54:00:de:ad:02",
		MasterIP:    "192.168.50.236",
		MachineCIDR: "192.168.50.0/24",
		Gateway:     "192.168.50.1",
		StoragePool: "images",
	}
}

// TestApply_BridgeEmbedsNetworkKeyfile confirms bridge mode writes a
// NetworkManager keyfile next to the cluster dir and feeds it to the installer
// for embedding into the boot ISO.
func TestApply_BridgeEmbedsNetworkKeyfile(t *testing.T) {
	inst := &fakes.Installer{}
	vm := &fakes.VMManager{}
	sc := newStageContext(t, bridgeCluster())

	if err := os.MkdirAll(sc.ClusterDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	s := New(inst, vm)
	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !inst.EmbeddedNetwork {
		t.Fatal("expected EmbedNetworkKeyfileInISO to be called in bridge mode")
	}
	data, err := os.ReadFile(inst.LastNetworkKeyfile)
	if err != nil {
		t.Fatalf("read embedded keyfile %q: %v", inst.LastNetworkKeyfile, err)
	}
	if !strings.Contains(string(data), "address1=192.168.50.236/24,192.168.50.1") {
		t.Errorf("keyfile missing static address:\n%s", data)
	}
}

// TestApply_NATSkipsNetworkKeyfile confirms NAT mode (deterministic libvirt
// DHCP) does not embed a static network keyfile.
func TestApply_NATSkipsNetworkKeyfile(t *testing.T) {
	inst := &fakes.Installer{}
	vm := &fakes.VMManager{}
	c := &config.ClusterConfig{
		Name:        "demo",
		Domain:      "local",
		NetworkMode: config.NetworkModeNAT,
		StoragePool: "default",
	}
	sc := newStageContext(t, c)
	if err := os.MkdirAll(sc.ClusterDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	s := New(inst, vm)
	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if inst.EmbeddedNetwork {
		t.Error("NAT mode must not embed a static network keyfile")
	}
	if _, err := os.Stat(filepath.Join(sc.ClusterDir(), "master.nmconnection")); !os.IsNotExist(err) {
		t.Error("NAT mode must not write master.nmconnection")
	}
}
