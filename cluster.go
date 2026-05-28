package easyshift

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

const (
	ClusterStateNone     = "none"
	ClusterStateCreating = "creating"
	ClusterStateRunning  = "running"
	ClusterStateStopped  = "stopped"
	ClusterStateError    = "error"

	defaultMasterCPUs   = 4
	defaultWorkerCPUs   = 2
	defaultMasterDiskGB = 120
	defaultWorkerDiskGB = 120
)

// ClusterManager owns the cluster lifecycle. Side-effecting work is
// delegated to Stages run by a Runner, so the manager itself contains
// validation, default-application, and stage-list assembly only.
type ClusterManager struct {
	cfg    *Config
	deps   Deps
	runner *Runner
}

// NewClusterManager constructs a ClusterManager backed by the given config
// and deps. The Runner persists per-cluster state under cfg.ConfigDir.
func NewClusterManager(cfg *Config, deps Deps) *ClusterManager {
	return &ClusterManager{
		cfg:    cfg,
		deps:   deps,
		runner: NewRunner(cfg.ConfigDir),
	}
}

// Create provisions a cluster by running ClusterCreateStages. If a cluster
// with the same name already exists in a non-running state, Create resumes
// from the first unapplied stage; if it is already running, Create returns
// an error.
func (cm *ClusterManager) Create(ctx context.Context, c *ClusterConfig) error {
	if err := cm.validateName(c); err != nil {
		return err
	}
	if err := EnsurePullSecret(cm.cfg.ConfigDir); err != nil {
		return err
	}

	if existing := cm.find(c.Name); existing != nil {
		if existing.State == ClusterStateRunning {
			return fmt.Errorf("cluster %s is already running", c.Name)
		}
		logrus.Infof("resuming create for existing cluster %s (state=%s)", c.Name, existing.State)
		c = existing
	} else {
		cm.applyDefaults(c)
		if err := cm.validateNew(c); err != nil {
			return err
		}
	}

	// Resolve channel aliases (e.g., "stable") to a concrete version BEFORE
	// any stage runs, so the resolved value is what register-cluster
	// persists and every subsequent stage references the same version.
	// Idempotent: a cluster resumed from disk already has a concrete version.
	if !IsResolvedOCPVersion(c.OCPVersion) {
		resolved, err := ResolveOCPVersion(ctx, cm.deps.Download, c.OCPVersion)
		if err != nil {
			return fmt.Errorf("resolve OCP version %q: %w", c.OCPVersion, err)
		}
		logrus.Infof("resolved OCP channel %q to %s", c.OCPVersion, resolved)
		c.OCPVersion = resolved
	}

	sc := &StageContext{Cluster: c, Config: cm.cfg, Deps: cm.deps}
	stages := ClusterCreateStages()
	if err := cm.runner.Preflight(ctx, sc, stages); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	return cm.runner.Apply(ctx, sc, stages)
}

// Start boots all nodes for a stopped cluster. (Will be re-implemented as
// its own stage set in a later phase.)
func (cm *ClusterManager) Start(ctx context.Context, name string) error {
	c := cm.find(name)
	if c == nil {
		return fmt.Errorf("cluster %s not found", name)
	}
	if c.State == ClusterStateRunning {
		return fmt.Errorf("cluster %s is already running", name)
	}
	for _, vm := range cm.vmNames(c) {
		if err := cm.deps.VM.Start(ctx, vm); err != nil {
			return fmt.Errorf("start %s: %w", vm, err)
		}
	}
	c.State = ClusterStateRunning
	return cm.cfg.Save()
}

// Stop shuts down all nodes for a running cluster.
func (cm *ClusterManager) Stop(ctx context.Context, name string) error {
	c := cm.find(name)
	if c == nil {
		return fmt.Errorf("cluster %s not found", name)
	}
	if c.State != ClusterStateRunning {
		return fmt.Errorf("cluster %s is not running", name)
	}
	for _, vm := range cm.vmNames(c) {
		if err := cm.deps.VM.Stop(ctx, vm); err != nil {
			logrus.Warnf("stop %s: %v", vm, err)
		}
	}
	c.State = ClusterStateStopped
	return cm.cfg.Save()
}

// Delete tears down a cluster by rolling back every stage recorded as
// applied in state.json, then removes the on-disk state file.
func (cm *ClusterManager) Delete(ctx context.Context, name string) error {
	c := cm.find(name)
	if c == nil {
		return fmt.Errorf("cluster %s not found", name)
	}

	if c.State == ClusterStateRunning {
		if err := cm.Stop(ctx, name); err != nil {
			return err
		}
	}

	sc := &StageContext{Cluster: c, Config: cm.cfg, Deps: cm.deps}
	if err := cm.runner.Rollback(ctx, sc, ClusterCreateStages()); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}

	// ensure-cluster-dir's Rollback removed the cluster dir mid-chain, but
	// the runner re-creates it each time it persists state.json after a
	// rollback step. With all rollbacks done, the dir has no purpose —
	// wipe it (and the empty state.json inside) so a future Create is fresh.
	_ = os.RemoveAll(filepath.Join(cm.cfg.ConfigDir, "clusters", name))

	return cm.cfg.Save()
}

// List returns all known clusters.
func (cm *ClusterManager) List() []*ClusterConfig {
	out := make([]*ClusterConfig, len(cm.cfg.Clusters))
	copy(out, cm.cfg.Clusters)
	return out
}

func (cm *ClusterManager) find(name string) *ClusterConfig {
	for _, c := range cm.cfg.Clusters {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// validateName checks just the name; used on every Create entry (including resumes).
func (cm *ClusterManager) validateName(c *ClusterConfig) error {
	if c.Name == "" {
		return fmt.Errorf("cluster name is required")
	}
	return nil
}

// validateNew checks invariants that only apply to brand-new clusters.
// Resume Create on an existing cluster skips these.
func (cm *ClusterManager) validateNew(c *ClusterConfig) error {
	if len(cm.cfg.Clusters) >= DefaultClustersMax {
		return fmt.Errorf("maximum number of clusters (%d) reached", DefaultClustersMax)
	}
	if c.MasterCount != 1 {
		return fmt.Errorf("only single-master clusters are supported")
	}
	if c.WorkerCount != 0 {
		// Phase 1 supports SNO only. Workers will be addable via `easyshift
		// addnode` once that command lands (Phase 4).
		return fmt.Errorf("Phase 1 supports SNO only: WorkerCount must be 0 (add workers later via addnode)")
	}
	switch c.NetworkMode {
	case NetworkModeNAT:
		if c.Bridge != "" {
			return fmt.Errorf("NetworkMode=nat is incompatible with --bridge")
		}
		if c.MasterMAC != "" || c.MasterIP != "" || c.MachineCIDR != "" {
			return fmt.Errorf("NetworkMode=nat assigns MAC/IP/CIDR automatically; remove --master-mac/--master-ip/--machine-cidr")
		}
	case NetworkModeBridge:
		if c.Bridge == "" {
			return fmt.Errorf("NetworkMode=bridge requires --bridge (name of an existing host Linux bridge, e.g. br0)")
		}
		if c.MasterMAC == "" {
			return fmt.Errorf("NetworkMode=bridge requires --master-mac (the MAC you reserved at the router)")
		}
		if _, err := net.ParseMAC(c.MasterMAC); err != nil {
			return fmt.Errorf("invalid --master-mac %q: %w", c.MasterMAC, err)
		}
		if c.MasterIP == "" {
			return fmt.Errorf("NetworkMode=bridge requires --master-ip (the IP the router will reserve for --master-mac)")
		}
		if net.ParseIP(c.MasterIP) == nil {
			return fmt.Errorf("invalid --master-ip %q", c.MasterIP)
		}
		if c.MachineCIDR == "" {
			return fmt.Errorf("could not derive --machine-cidr; pass it explicitly")
		}
		if _, _, err := net.ParseCIDR(c.MachineCIDR); err != nil {
			return fmt.Errorf("invalid --machine-cidr %q: %w", c.MachineCIDR, err)
		}
	default:
		return fmt.Errorf("invalid NetworkMode %q (want %q or %q)", c.NetworkMode, NetworkModeNAT, NetworkModeBridge)
	}
	return nil
}

func (cm *ClusterManager) applyDefaults(c *ClusterConfig) {
	if c.Domain == "" {
		c.Domain = "local"
	}
	if c.OCPVersion == "" {
		c.OCPVersion = DefaultOCPVersion
	}
	if c.MasterCPUs == 0 {
		c.MasterCPUs = defaultMasterCPUs
	}
	if c.WorkerCPUs == 0 {
		c.WorkerCPUs = defaultWorkerCPUs
	}
	if c.MasterDiskGB == 0 {
		c.MasterDiskGB = defaultMasterDiskGB
	}
	if c.WorkerDiskGB == 0 {
		c.WorkerDiskGB = defaultWorkerDiskGB
	}
	if c.NetworkMode == "" {
		c.NetworkMode = NetworkModeNAT
	}
	if c.StoragePool == "" {
		c.StoragePool = DefaultStoragePool
	}
	// Bridge mode: derive MachineCIDR from MasterIP unless the user gave us
	// an explicit override on --machine-cidr.
	if c.NetworkMode == NetworkModeBridge && c.MachineCIDR == "" && c.MasterIP != "" {
		if cidr, err := DeriveMachineCIDR(c.MasterIP); err == nil {
			c.MachineCIDR = cidr
		}
	}
}

func (cm *ClusterManager) vmNames(c *ClusterConfig) []string {
	names := make([]string, 0, c.MasterCount+c.WorkerCount)
	for i := 0; i < c.MasterCount; i++ {
		names = append(names, fmt.Sprintf("master-%d-%s", i, c.Name))
	}
	for i := 0; i < c.WorkerCount; i++ {
		names = append(names, fmt.Sprintf("worker-%d-%s", i, c.Name))
	}
	return names
}
