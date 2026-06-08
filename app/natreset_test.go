package app_test

import (
	"context"
	"testing"

	"github.com/raghavendra-talur/easyshift/app"
	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

func natCluster(name, ip, mac string) *config.ClusterConfig {
	return &config.ClusterConfig{
		Name:         name,
		NetworkMode:  config.NetworkModeNAT,
		IPAddresses:  []string{ip},
		MACAddresses: []string{mac},
	}
}

// currentRange is what a freshly-created network reports under the new layout.
var currentRange = interfaces.NetworkInfo{
	Exists:         true,
	DHCPRangeStart: "192.168.126.100",
	DHCPRangeEnd:   "192.168.126.254",
}

// outdatedRange mimics a network created before the disjoint-range fix.
var outdatedRange = interfaces.NetworkInfo{
	Exists:         true,
	DHCPRangeStart: "192.168.126.5",
	DHCPRangeEnd:   "192.168.126.254",
}

// TestPlanNATReset_OutdatedRangeNoClusters: an old-range network with no
// clusters should recreate and not be blocked.
func TestPlanNATReset_OutdatedRangeNoClusters(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	bundle.Net.Info = outdatedRange
	mgr := app.NewClusterManager(cfg, deps)

	plan, err := mgr.PlanNATReset(context.Background(), false)
	if err != nil {
		t.Fatalf("PlanNATReset: %v", err)
	}
	if !plan.RangeOutdated || !plan.Recreate {
		t.Errorf("expected outdated range to trigger recreate, got %+v", plan)
	}
	if plan.Blocked {
		t.Error("recreate must not be blocked when no clusters run")
	}
	if !plan.HasWork() {
		t.Error("expected HasWork=true")
	}
}

// TestPlanNATReset_OutdatedRangeRunningClusterBlocks: a running NAT cluster
// blocks the recreate unless --force.
func TestPlanNATReset_OutdatedRangeRunningClusterBlocks(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	cfg.Clusters = []*config.ClusterConfig{natCluster("c1", "192.168.126.5", "52:54:00:aa:bb:cc")}
	bundle.Net.Info = outdatedRange
	bundle.VM.SetRunning("master-0-c1", true)
	mgr := app.NewClusterManager(cfg, deps)

	blocked, err := mgr.PlanNATReset(context.Background(), false)
	if err != nil {
		t.Fatalf("PlanNATReset: %v", err)
	}
	if !blocked.Blocked {
		t.Errorf("expected Blocked=true with a running cluster and no force")
	}
	if err := mgr.ApplyNATReset(context.Background(), blocked); err == nil {
		t.Error("ApplyNATReset must refuse a blocked plan")
	}

	forced, err := mgr.PlanNATReset(context.Background(), true)
	if err != nil {
		t.Fatalf("PlanNATReset(force): %v", err)
	}
	if forced.Blocked {
		t.Error("force must clear Blocked")
	}
}

// TestApplyNATReset_RecreatesAndRestores: recreate tears down, rebuilds, and
// re-adds the surviving cluster's reservation; leaked state is pruned.
func TestApplyNATReset_RecreatesAndRestores(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	cfg.Clusters = []*config.ClusterConfig{natCluster("c1", "192.168.126.5", "52:54:00:aa:bb:cc")}
	cfg.GlobalState.UsedIPs = map[string]bool{"192.168.126.5": true, "192.168.126.9": true} // .9 is leaked
	cfg.GlobalState.UsedMACs = map[string]bool{"52:54:00:aa:bb:cc": true, "52:54:00:de:ad:00": true}
	bundle.Net.Info = outdatedRange
	mgr := app.NewClusterManager(cfg, deps)

	plan, err := mgr.PlanNATReset(context.Background(), false)
	if err != nil {
		t.Fatalf("PlanNATReset: %v", err)
	}
	if err := mgr.ApplyNATReset(context.Background(), plan); err != nil {
		t.Fatalf("ApplyNATReset: %v", err)
	}

	if len(bundle.Net.ResetCalls) != 1 || bundle.Net.ResetCalls[0] != config.SharedNATNetwork {
		t.Errorf("expected one ResetNetwork on %s, got %v", config.SharedNATNetwork, bundle.Net.ResetCalls)
	}
	if len(bundle.Net.Ensured) != 1 {
		t.Errorf("expected network recreated once, got %d EnsureNetwork calls", len(bundle.Net.Ensured))
	}
	if len(bundle.Net.Added) != 1 || bundle.Net.Added[0].Host.IP != "192.168.126.5" {
		t.Errorf("expected c1 reservation restored, got %+v", bundle.Net.Added)
	}
	if cfg.GlobalState.UsedIPs["192.168.126.9"] {
		t.Error("leaked IP .9 should have been pruned")
	}
	if !cfg.GlobalState.UsedIPs["192.168.126.5"] {
		t.Error("live cluster IP .5 must be retained")
	}
	if cfg.GlobalState.UsedMACs["52:54:00:de:ad:00"] {
		t.Error("leaked MAC should have been pruned")
	}
}

// TestApplyNATReset_RemovesOrphansInPlace: a current-range network keeps its
// definition; only orphaned reservations are removed.
func TestApplyNATReset_RemovesOrphansInPlace(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	cfg.Clusters = []*config.ClusterConfig{natCluster("c1", "192.168.126.5", "52:54:00:aa:bb:cc")}
	info := currentRange
	info.Reservations = []interfaces.DHCPHost{
		{MAC: "52:54:00:aa:bb:cc", IP: "192.168.126.5", Hostname: "master-0-c1"},   // live
		{MAC: "52:54:00:99:99:99", IP: "192.168.126.6", Hostname: "master-0-gone"}, // orphan
	}
	bundle.Net.Info = info
	mgr := app.NewClusterManager(cfg, deps)

	plan, err := mgr.PlanNATReset(context.Background(), false)
	if err != nil {
		t.Fatalf("PlanNATReset: %v", err)
	}
	if plan.Recreate {
		t.Error("current range must not trigger recreate")
	}
	if len(plan.OrphanReservations) != 1 || plan.OrphanReservations[0].IP != "192.168.126.6" {
		t.Fatalf("expected one orphan (.6), got %+v", plan.OrphanReservations)
	}
	if err := mgr.ApplyNATReset(context.Background(), plan); err != nil {
		t.Fatalf("ApplyNATReset: %v", err)
	}
	if len(bundle.Net.ResetCalls) != 0 {
		t.Error("must not reset a current-range network")
	}
	if len(bundle.Net.Removed) != 1 || bundle.Net.Removed[0].Host.IP != "192.168.126.6" {
		t.Errorf("expected orphan .6 removed, got %+v", bundle.Net.Removed)
	}
}

// TestPlanNATReset_CleanIsNoOp: a current-range network whose reservations all
// map to known clusters reports no work (stale leases aside).
func TestPlanNATReset_CleanIsNoOp(t *testing.T) {
	cfg, deps, bundle := newTestEnv(t)
	cfg.Clusters = []*config.ClusterConfig{natCluster("c1", "192.168.126.5", "52:54:00:aa:bb:cc")}
	cfg.GlobalState.UsedIPs = map[string]bool{"192.168.126.5": true}
	cfg.GlobalState.UsedMACs = map[string]bool{"52:54:00:aa:bb:cc": true}
	info := currentRange
	info.Reservations = []interfaces.DHCPHost{{MAC: "52:54:00:aa:bb:cc", IP: "192.168.126.5", Hostname: "master-0-c1"}}
	info.Leases = []interfaces.DHCPLease{{MAC: "52:54:00:11:22:33", IP: "192.168.126.250"}} // stale but inert
	bundle.Net.Info = info
	mgr := app.NewClusterManager(cfg, deps)

	plan, err := mgr.PlanNATReset(context.Background(), false)
	if err != nil {
		t.Fatalf("PlanNATReset: %v", err)
	}
	if plan.HasWork() {
		t.Errorf("expected no work for a clean network, got %+v", plan)
	}
	if len(plan.StaleLeases) != 1 {
		t.Errorf("expected the stale lease to be reported, got %+v", plan.StaleLeases)
	}
}
