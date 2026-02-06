package easyshift

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	virshCmd        = "virsh"
	virtInstallCmd  = "virt-install"
	defaultBridge   = "virbr0"
	defaultCPUMode  = "host-passthrough"
	defaultEmulator = "/usr/bin/qemu-kvm"
)

// LibvirtManager handles libvirt operations
type LibvirtManager struct {
	baseDir string
}

// NewLibvirtManager creates a new libvirt manager
func NewLibvirtManager(baseDir string) *LibvirtManager {
	return &LibvirtManager{
		baseDir: baseDir,
	}
}

// CreateNetwork creates a new libvirt network for a cluster
func (lm *LibvirtManager) CreateNetwork(cluster *ClusterConfig) error {
	networkName := fmt.Sprintf("easyshift-%s", cluster.Name)
	networkXML := fmt.Sprintf(`
<network>
  <name>%s</name>
  <forward mode='nat'>
    <nat>
      <port start='1024' end='65535'/>
    </nat>
  </forward>
  <bridge name='%s' stp='on' delay='0'/>
  <domain name='%s.%s' localOnly='yes'/>
  <ip address='%s.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='%s.5' end='%s.20'/>
    </dhcp>
  </ip>
</network>`, networkName, defaultBridge, cluster.Name, cluster.Domain,
		cluster.NetworkSubnet, cluster.NetworkSubnet, cluster.NetworkSubnet)

	// Write network XML to temporary file
	xmlFile := fmt.Sprintf("/tmp/%s-network.xml", cluster.Name)
	if err := writeFile(xmlFile, []byte(networkXML)); err != nil {
		return fmt.Errorf("failed to write network XML: %w", err)
	}

	// Define and start network
	if err := execCmd(virshCmd, "net-define", xmlFile); err != nil {
		return fmt.Errorf("failed to define network: %w", err)
	}

	if err := execCmd(virshCmd, "net-start", networkName); err != nil {
		return fmt.Errorf("failed to start network: %w", err)
	}

	if err := execCmd(virshCmd, "net-autostart", networkName); err != nil {
		return fmt.Errorf("failed to set network autostart: %w", err)
	}

	return nil
}

// CreateVM creates a new virtual machine
func (lm *LibvirtManager) CreateVM(cluster *ClusterConfig, nodeName string, isMaster bool) error {
	vmName := fmt.Sprintf("%s-%s", nodeName, cluster.Name)
	diskPath := fmt.Sprintf("%s/clusters/%s/vms/%s.qcow2", lm.baseDir, cluster.Name, vmName)

	var ram, cpus, diskSize int
	if isMaster {
		ram = cluster.MasterRAM
		cpus = cluster.MasterCPUs
		diskSize = cluster.MasterDiskGB
	} else {
		ram = cluster.WorkerRAM
		cpus = cluster.WorkerCPUs
		diskSize = cluster.WorkerDiskGB
	}

	args := []string{
		"--name", vmName,
		"--memory", fmt.Sprintf("%d", ram),
		"--vcpus", fmt.Sprintf("%d", cpus),
		"--cpu", defaultCPUMode,
		"--disk", fmt.Sprintf("path=%s,size=%d,bus=virtio", diskPath, diskSize),
		"--network", fmt.Sprintf("network=easyshift-%s,mac=%s", cluster.Name, getMACAddress(cluster, nodeName)),
		"--os-variant", "rhel8.0",
		"--noautoconsole",
	}

	if err := execCmd(virtInstallCmd, args...); err != nil {
		return fmt.Errorf("failed to create VM %s: %w", vmName, err)
	}

	return nil
}

// StartVM starts a virtual machine
func (lm *LibvirtManager) StartVM(cluster *ClusterConfig, vmName string) error {
	if err := execCmd(virshCmd, "start", vmName); err != nil {
		return fmt.Errorf("failed to start VM %s: %w", vmName, err)
	}

	// Wait for VM to be running
	for i := 0; i < 30; i++ {
		if lm.isVMRunning(vmName) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for VM %s to start", vmName)
}

// StopVM stops a virtual machine
func (lm *LibvirtManager) StopVM(vmName string) error {
	if err := execCmd(virshCmd, "shutdown", vmName); err != nil {
		return fmt.Errorf("failed to stop VM %s: %w", vmName, err)
	}

	// Wait for VM to be stopped
	for i := 0; i < 30; i++ {
		if !lm.isVMRunning(vmName) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	// Force stop if shutdown didn't work
	if err := execCmd(virshCmd, "destroy", vmName); err != nil {
		return fmt.Errorf("failed to force stop VM %s: %w", vmName, err)
	}

	return nil
}

// DeleteVM deletes a virtual machine
func (lm *LibvirtManager) DeleteVM(vmName string) error {
	if lm.isVMRunning(vmName) {
		if err := lm.StopVM(vmName); err != nil {
			return fmt.Errorf("failed to stop VM before deletion: %w", err)
		}
	}

	if err := execCmd(virshCmd, "undefine", vmName, "--remove-all-storage"); err != nil {
		return fmt.Errorf("failed to delete VM %s: %w", vmName, err)
	}

	return nil
}

// DeleteNetwork deletes a libvirt network
func (lm *LibvirtManager) DeleteNetwork(networkName string) error {
	if err := execCmd(virshCmd, "net-destroy", networkName); err != nil {
		logrus.Warnf("Failed to destroy network %s: %v", networkName, err)
	}

	if err := execCmd(virshCmd, "net-undefine", networkName); err != nil {
		return fmt.Errorf("failed to undefine network %s: %w", networkName, err)
	}

	return nil
}

// Helper functions

func (lm *LibvirtManager) isVMRunning(vmName string) bool {
	cmd := exec.Command(virshCmd, "domstate", vmName)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "running"
}

func getMACAddress(cluster *ClusterConfig, nodeName string) string {
	// Return pre-allocated MAC address from cluster config
	for i, name := range cluster.IPAddresses {
		if strings.Contains(name, nodeName) {
			return cluster.MACAddresses[i]
		}
	}
	return ""
}

// Helper function to execute commands
func execCmd(cmd string, args ...string) error {
	logrus.Debugf("Executing: %s %s", cmd, strings.Join(args, " "))
	command := exec.Command(cmd, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %s\nOutput: %s", err, string(output))
	}
	return nil
}
