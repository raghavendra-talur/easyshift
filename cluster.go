package easyshift

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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

// ClusterManager handles cluster operations
type ClusterManager struct {
	sync.RWMutex
	config *Config
	http   *HTTPServer
}

var (
	clusterManager *ClusterManager
	managerOnce    sync.Once
)

// GetClusterManager returns the singleton cluster manager instance
func GetClusterManager() *ClusterManager {
	managerOnce.Do(func() {
		clusterManager = &ClusterManager{
			config: GetConfig(),
			http:   NewHTTPServer(),
		}
	})
	return clusterManager
}

// CreateCluster creates a new OpenShift cluster
func CreateCluster(cfg *ClusterConfig) error {
	cm := GetClusterManager()
	cm.Lock()
	defer cm.Unlock()

	logrus.Infof("Creating cluster: %s.%s", cfg.Name, cfg.Domain)

	// Validate configuration
	if err := cm.validateClusterConfig(cfg); err != nil {
		return fmt.Errorf("invalid cluster configuration: %w", err)
	}

	// Set defaults
	cm.setClusterDefaults(cfg)

	// Allocate network resources
	if err := cm.allocateNetworkResources(cfg); err != nil {
		return fmt.Errorf("failed to allocate network resources: %w", err)
	}

	// Create cluster directory
	clusterDir := filepath.Join(cm.config.ConfigDir, "clusters", cfg.Name)
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		return fmt.Errorf("failed to create cluster directory: %w", err)
	}

	// Download OpenShift binaries and images
	if err := cm.downloadOCPResources(cfg); err != nil {
		return fmt.Errorf("failed to download OpenShift resources: %w", err)
	}

	// Create ignition configs
	if err := cm.createIgnitionConfigs(cfg); err != nil {
		return fmt.Errorf("failed to create ignition configs: %w", err)
	}

	// Create libvirt network
	if err := cm.createLibvirtNetwork(cfg); err != nil {
		return fmt.Errorf("failed to create libvirt network: %w", err)
	}

	// Create and start VMs
	if err := cm.createClusterNodes(cfg); err != nil {
		return fmt.Errorf("failed to create cluster nodes: %w", err)
	}

	// Update cluster state
	cfg.State = ClusterStateRunning
	cm.config.Clusters = append(cm.config.Clusters, cfg)
	if err := cm.config.save(); err != nil {
		return fmt.Errorf("failed to save cluster configuration: %w", err)
	}

	logrus.Infof("Cluster %s.%s created successfully", cfg.Name, cfg.Domain)
	return nil
}

// StartCluster starts a stopped cluster
func StartCluster(name string) error {
	cm := GetClusterManager()
	cm.Lock()
	defer cm.Unlock()

	cluster := cm.findCluster(name)
	if cluster == nil {
		return fmt.Errorf("cluster %s not found", name)
	}

	if cluster.State == ClusterStateRunning {
		return fmt.Errorf("cluster %s is already running", name)
	}

	logrus.Infof("Starting cluster: %s", name)

	// Start VMs
	if err := cm.startClusterNodes(cluster); err != nil {
		return fmt.Errorf("failed to start cluster nodes: %w", err)
	}

	cluster.State = ClusterStateRunning
	if err := cm.config.save(); err != nil {
		return fmt.Errorf("failed to save cluster state: %w", err)
	}

	return nil
}

// StopCluster stops a running cluster
func StopCluster(name string) error {
	cm := GetClusterManager()
	cm.Lock()
	defer cm.Unlock()

	cluster := cm.findCluster(name)
	if cluster == nil {
		return fmt.Errorf("cluster %s not found", name)
	}

	if cluster.State != ClusterStateRunning {
		return fmt.Errorf("cluster %s is not running", name)
	}

	logrus.Infof("Stopping cluster: %s", name)

	// Stop VMs
	if err := cm.stopClusterNodes(cluster); err != nil {
		return fmt.Errorf("failed to stop cluster nodes: %w", err)
	}

	cluster.State = ClusterStateStopped
	if err := cm.config.save(); err != nil {
		return fmt.Errorf("failed to save cluster state: %w", err)
	}

	return nil
}

// DeleteCluster deletes a cluster
func DeleteCluster(name string) error {
	cm := GetClusterManager()
	cm.Lock()
	defer cm.Unlock()

	cluster := cm.findCluster(name)
	if cluster == nil {
		return fmt.Errorf("cluster %s not found", name)
	}

	logrus.Infof("Deleting cluster: %s", name)

	// Stop cluster if running
	if cluster.State == ClusterStateRunning {
		if err := StopCluster(name); err != nil {
			return fmt.Errorf("failed to stop cluster: %w", err)
		}
	}

	// Delete VMs
	if err := cm.deleteClusterNodes(cluster); err != nil {
		return fmt.Errorf("failed to delete cluster nodes: %w", err)
	}

	// Delete cluster directory
	clusterDir := filepath.Join(cm.config.ConfigDir, "clusters", cluster.Name)
	if err := os.RemoveAll(clusterDir); err != nil {
		return fmt.Errorf("failed to delete cluster directory: %w", err)
	}

	// Remove cluster from configuration
	for i, c := range cm.config.Clusters {
		if c.Name == name {
			cm.config.Clusters = append(cm.config.Clusters[:i], cm.config.Clusters[i+1:]...)
			break
		}
	}

	// Release network resources
	cm.releaseNetworkResources(cluster)

	if err := cm.config.save(); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	return nil
}

// ListClusters returns information about all clusters
func ListClusters() error {
	cm := GetClusterManager()
	cm.RLock()
	defer cm.RUnlock()

	if len(cm.config.Clusters) == 0 {
		fmt.Println("No clusters found")
		return nil
	}

	fmt.Println("Clusters:")
	for _, cluster := range cm.config.Clusters {
		fmt.Printf("- Name: %s.%s\n", cluster.Name, cluster.Domain)
		fmt.Printf("  State: %s\n", cluster.State)
		fmt.Printf("  Version: %s\n", cluster.OCPVersion)
		fmt.Printf("  Nodes: %d masters, %d workers\n", cluster.MasterCount, cluster.WorkerCount)
		fmt.Println()
	}

	return nil
}

// Helper functions

func (cm *ClusterManager) findCluster(name string) *ClusterConfig {
	for _, cluster := range cm.config.Clusters {
		if cluster.Name == name {
			return cluster
		}
	}
	return nil
}

func (cm *ClusterManager) validateClusterConfig(cfg *ClusterConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("cluster name is required")
	}

	if cm.findCluster(cfg.Name) != nil {
		return fmt.Errorf("cluster %s already exists", cfg.Name)
	}

	if len(cm.config.Clusters) >= DefaultClustersMax {
		return fmt.Errorf("maximum number of clusters (%d) reached", DefaultClustersMax)
	}

	if cfg.MasterCount != 1 {
		return fmt.Errorf("only single master configuration is supported")
	}

	if cfg.WorkerCount > DefaultWorkersMax {
		return fmt.Errorf("maximum number of workers (%d) exceeded", DefaultWorkersMax)
	}

	return nil
}

func (cm *ClusterManager) setClusterDefaults(cfg *ClusterConfig) {
	if cfg.Domain == "" {
		cfg.Domain = "local"
	}
	if cfg.OCPVersion == "" {
		cfg.OCPVersion = DefaultOCPVersion
	}
	if cfg.MasterCPUs == 0 {
		cfg.MasterCPUs = defaultMasterCPUs
	}
	if cfg.WorkerCPUs == 0 {
		cfg.WorkerCPUs = defaultWorkerCPUs
	}
	if cfg.MasterDiskGB == 0 {
		cfg.MasterDiskGB = defaultMasterDiskGB
	}
	if cfg.WorkerDiskGB == 0 {
		cfg.WorkerDiskGB = defaultWorkerDiskGB
	}

	cfg.State = ClusterStateCreating
}

// downloadOCPResources downloads required OpenShift resources for the cluster
func (cm *ClusterManager) downloadOCPResources(cfg *ClusterConfig) error {
	logrus.Infof("Downloading OpenShift resources for cluster %s", cfg.Name)

	installer := NewInstallManager(cm.config.ConfigDir, cm.http)
	if err := installer.PrepareInstallation(cfg); err != nil {
		return fmt.Errorf("failed to prepare installation: %w", err)
	}

	return nil
}

// createIgnitionConfigs creates and configures ignition files
func (cm *ClusterManager) createIgnitionConfigs(cfg *ClusterConfig) error {
	logrus.Infof("Creating ignition configs for cluster %s", cfg.Name)

	installer := NewInstallManager(cm.config.ConfigDir, cm.http)
	if err := installer.generateIgnitionConfigs(cfg); err != nil {
		return fmt.Errorf("failed to generate ignition configs: %w", err)
	}

	return nil
}

// createLibvirtNetwork creates the virtual network for the cluster
func (cm *ClusterManager) createLibvirtNetwork(cfg *ClusterConfig) error {
	logrus.Infof("Creating libvirt network for cluster %s", cfg.Name)

	libvirt := NewLibvirtManager(cm.config.ConfigDir)
	if err := libvirt.CreateNetwork(cfg); err != nil {
		return fmt.Errorf("failed to create libvirt network: %w", err)
	}

	return nil
}

// createClusterNodes creates all the required virtual machines for the cluster
func (cm *ClusterManager) createClusterNodes(cfg *ClusterConfig) error {
	logrus.Infof("Creating cluster nodes for %s", cfg.Name)

	libvirt := NewLibvirtManager(cm.config.ConfigDir)

	// Create master nodes
	for i := 0; i < cfg.MasterCount; i++ {
		nodeName := fmt.Sprintf("master-%d", i)
		if err := libvirt.CreateVM(cfg, nodeName, true); err != nil {
			return fmt.Errorf("failed to create master node %s: %w", nodeName, err)
		}
	}

	// Create worker nodes
	for i := 0; i < cfg.WorkerCount; i++ {
		nodeName := fmt.Sprintf("worker-%d", i)
		if err := libvirt.CreateVM(cfg, nodeName, false); err != nil {
			return fmt.Errorf("failed to create worker node %s: %w", nodeName, err)
		}
	}

	return nil
}

// startClusterNodes starts all nodes in the cluster
func (cm *ClusterManager) startClusterNodes(cfg *ClusterConfig) error {
	logrus.Infof("Starting cluster nodes for %s", cfg.Name)

	libvirt := NewLibvirtManager(cm.config.ConfigDir)

	// Start master nodes
	for i := 0; i < cfg.MasterCount; i++ {
		nodeName := fmt.Sprintf("master-%d-%s", i, cfg.Name)
		if err := libvirt.StartVM(cfg, nodeName); err != nil {
			return fmt.Errorf("failed to start master node %s: %w", nodeName, err)
		}
	}

	// Start worker nodes
	for i := 0; i < cfg.WorkerCount; i++ {
		nodeName := fmt.Sprintf("worker-%d-%s", i, cfg.Name)
		if err := libvirt.StartVM(cfg, nodeName); err != nil {
			return fmt.Errorf("failed to start worker node %s: %w", nodeName, err)
		}
	}

	return nil
}

// stopClusterNodes stops all nodes in the cluster
func (cm *ClusterManager) stopClusterNodes(cfg *ClusterConfig) error {
	logrus.Infof("Stopping cluster nodes for %s", cfg.Name)

	libvirt := NewLibvirtManager(cm.config.ConfigDir)

	// Stop worker nodes first
	for i := 0; i < cfg.WorkerCount; i++ {
		nodeName := fmt.Sprintf("worker-%d-%s", i, cfg.Name)
		if err := libvirt.StopVM(nodeName); err != nil {
			logrus.Warnf("Failed to stop worker node %s: %v", nodeName, err)
		}
	}

	// Stop master nodes last
	for i := 0; i < cfg.MasterCount; i++ {
		nodeName := fmt.Sprintf("master-%d-%s", i, cfg.Name)
		if err := libvirt.StopVM(nodeName); err != nil {
			logrus.Warnf("Failed to stop master node %s: %v", nodeName, err)
		}
	}

	return nil
}

// deleteClusterNodes deletes all nodes in the cluster
func (cm *ClusterManager) deleteClusterNodes(cfg *ClusterConfig) error {
	logrus.Infof("Deleting cluster nodes for %s", cfg.Name)

	libvirt := NewLibvirtManager(cm.config.ConfigDir)

	// Delete worker nodes first
	for i := 0; i < cfg.WorkerCount; i++ {
		nodeName := fmt.Sprintf("worker-%d-%s", i, cfg.Name)
		if err := libvirt.DeleteVM(nodeName); err != nil {
			logrus.Warnf("Failed to delete worker node %s: %v", nodeName, err)
		}
	}

	// Delete master nodes last
	for i := 0; i < cfg.MasterCount; i++ {
		nodeName := fmt.Sprintf("master-%d-%s", i, cfg.Name)
		if err := libvirt.DeleteVM(nodeName); err != nil {
			logrus.Warnf("Failed to delete master node %s: %v", nodeName, err)
		}
	}

	return nil
}

// allocateNetworkResources allocates network resources for the cluster
func (cm *ClusterManager) allocateNetworkResources(cfg *ClusterConfig) error {
	logrus.Infof("Allocating network resources for cluster %s", cfg.Name)

	networkManager := GetNetworkManager()
	if err := networkManager.AllocateNetworkResources(cfg); err != nil {
		return fmt.Errorf("failed to allocate network resources: %w", err)
	}

	return nil
}

// releaseNetworkResources releases network resources for the cluster
func (cm *ClusterManager) releaseNetworkResources(cfg *ClusterConfig) {
	logrus.Infof("Releasing network resources for cluster %s", cfg.Name)

	networkManager := GetNetworkManager()
	networkManager.ReleaseNetworkResources(cfg)
}
