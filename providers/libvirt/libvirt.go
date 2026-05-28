package libvirt

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/raghavendra-talur/easyshift/interfaces"
)

// LibvirtSystemURI is the libvirt connection URI easyshift targets for every
// VM and network operation.
const LibvirtSystemURI = "qemu:///system"

const (
	virshCmd       = "virsh"
	virtInstallCmd = "virt-install"
	defaultCPUMode = "host-passthrough"
	// defaultOSInfo lets modern virt-install (which mandates an OS hint)
	// detect RHCOS from the boot media and fall back to generic defaults
	// rather than erroring when detection can't pin an exact osinfo name.
	defaultOSInfo = "detect=on,require=off"
)

// LibvirtVMManager implements VMManager via virsh and virt-install.
type LibvirtVMManager struct {
	cmd interfaces.CommandRunner
}

// NewLibvirtVMManager returns a VMManager backed by libvirt CLI tools.
func NewLibvirtVMManager(cmd interfaces.CommandRunner) *LibvirtVMManager {
	return &LibvirtVMManager{cmd: cmd}
}

// Create defines a new VM via `virt-install`. If spec.BootISO is set the VM
// boots from that ISO (SNO bootstrap-in-place); otherwise it PXE-boots and
// uses spec.KernelArgs.
func (m *LibvirtVMManager) Create(ctx context.Context, spec interfaces.VMSpec) error {
	args := []string{
		"--name", spec.Name,
		"--memory", strconv.Itoa(spec.MemoryMiB),
		"--vcpus", strconv.Itoa(spec.VCPUs),
		"--cpu", defaultCPUMode,
		"--osinfo", defaultOSInfo,
		"--disk", fmt.Sprintf("pool=%s,size=%d,bus=virtio", spec.StoragePool, spec.DiskSizeGiB),
		"--network", spec.NetworkArg,
		"--noautoconsole",
	}
	if spec.BootISO != "" {
		args = append(args, "--cdrom", spec.BootISO)
	} else {
		args = append(args, "--pxe")
		if spec.KernelArgs != "" {
			args = append(args, "--extra-args", spec.KernelArgs)
		}
	}
	if _, err := m.cmd.Run(ctx, virtInstallCmd, args...); err != nil {
		return fmt.Errorf("virt-install %s: %w", spec.Name, err)
	}
	return nil
}

// Start boots a VM and waits up to 60s for it to enter the running state.
func (m *LibvirtVMManager) Start(ctx context.Context, name string) error {
	if _, err := m.cmd.Run(ctx, virshCmd, "start", name); err != nil {
		return fmt.Errorf("virsh start %s: %w", name, err)
	}
	for i := 0; i < 30; i++ {
		running, err := m.IsRunning(ctx, name)
		if err == nil && running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for VM %s to start", name)
}

// Stop gracefully shuts down a VM, falling back to forced destroy after 60s.
func (m *LibvirtVMManager) Stop(ctx context.Context, name string) error {
	if _, err := m.cmd.Run(ctx, virshCmd, "shutdown", name); err != nil {
		return fmt.Errorf("virsh shutdown %s: %w", name, err)
	}
	for i := 0; i < 30; i++ {
		running, err := m.IsRunning(ctx, name)
		if err == nil && !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if _, err := m.cmd.Run(ctx, virshCmd, "destroy", name); err != nil {
		return fmt.Errorf("virsh destroy %s: %w", name, err)
	}
	return nil
}

// Delete undefines a VM and removes its storage.
func (m *LibvirtVMManager) Delete(ctx context.Context, name string) error {
	running, _ := m.IsRunning(ctx, name)
	if running {
		if err := m.Stop(ctx, name); err != nil {
			return err
		}
	}
	if _, err := m.cmd.Run(ctx, virshCmd, "undefine", name, "--remove-all-storage"); err != nil {
		return fmt.Errorf("virsh undefine %s: %w", name, err)
	}
	return nil
}

// IsRunning returns true if `virsh domstate` reports the VM as running.
func (m *LibvirtVMManager) IsRunning(ctx context.Context, name string) (bool, error) {
	out, err := m.cmd.Run(ctx, virshCmd, "domstate", name)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "running", nil
}

// ImportISO uploads localPath into the default storage pool as a raw volume
// named volName and returns the resulting pool volume path. The upload
// streams bytes through libvirtd (which reads them from the client), so the
// source file only needs to be readable by the easyshift process — it does
// not need to be reachable by the qemu user.
func (m *LibvirtVMManager) ImportISO(ctx context.Context, pool, volName, localPath string) (string, error) {
	fi, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat iso %s: %w", localPath, err)
	}
	// Drop any stale volume from a previous attempt so vol-create-as is idempotent.
	_, _ = m.cmd.Run(ctx, virshCmd, "vol-delete", "--pool", pool, volName)

	if _, err := m.cmd.Run(ctx, virshCmd, "vol-create-as", pool, volName,
		strconv.FormatInt(fi.Size(), 10), "--format", "raw"); err != nil {
		return "", fmt.Errorf("vol-create-as %s: %w", volName, err)
	}
	if _, err := m.cmd.Run(ctx, virshCmd, "vol-upload", "--pool", pool, volName, localPath); err != nil {
		return "", fmt.Errorf("vol-upload %s: %w", volName, err)
	}
	out, err := m.cmd.Run(ctx, virshCmd, "vol-path", "--pool", pool, volName)
	if err != nil {
		return "", fmt.Errorf("vol-path %s: %w", volName, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoveISO deletes a volume created by ImportISO. Missing volumes are not
// an error (best-effort cleanup during rollback).
func (m *LibvirtVMManager) RemoveISO(ctx context.Context, pool, volName string) error {
	_, _ = m.cmd.Run(ctx, virshCmd, "vol-delete", "--pool", pool, volName)
	return nil
}

// CheckAccess runs a cheap probe against qemu:///system to detect the common
// deploy-time problems: libvirtd not running, user not in the libvirt group,
// or a polkit denial.
func (m *LibvirtVMManager) CheckAccess(ctx context.Context) error {
	if _, err := m.cmd.Run(ctx, virshCmd, "-c", LibvirtSystemURI, "list", "--name"); err != nil {
		return fmt.Errorf("libvirt at %s is not reachable: %w\n  hint: ensure libvirtd/virtqemud is running and your user is in the 'libvirt' group", LibvirtSystemURI, err)
	}
	return nil
}

// StoragePoolActive verifies the named libvirt pool exists and is running,
// since both the master disk and the boot ISO are created there.
func (m *LibvirtVMManager) StoragePoolActive(ctx context.Context, pool string) error {
	out, err := m.cmd.Run(ctx, virshCmd, "-c", LibvirtSystemURI, "pool-info", "--pool", pool)
	if err != nil {
		return fmt.Errorf("libvirt storage pool %q not found: %w\n  hint: pass --storage-pool <name> to use an existing pool (see `virsh pool-list --all`), or create it: virsh pool-define-as %s dir --target /var/lib/libvirt/images && virsh pool-autostart %s && virsh pool-start %s",
			pool, err, pool, pool, pool)
	}
	if !strings.Contains(string(out), "running") {
		return fmt.Errorf("libvirt storage pool %q exists but is not running\n  hint: virsh pool-start %s", pool, pool)
	}
	return nil
}

// LibvirtNetworkProvisioner implements NetworkProvisioner for libvirt NAT networks.
type LibvirtNetworkProvisioner struct {
	cmd interfaces.CommandRunner
}

// NewLibvirtNetworkProvisioner returns a NetworkProvisioner backed by virsh.
func NewLibvirtNetworkProvisioner(cmd interfaces.CommandRunner) *LibvirtNetworkProvisioner {
	return &LibvirtNetworkProvisioner{cmd: cmd}
}

const libvirtNetworkXMLTemplate = `<network>
  <name>%s</name>
  <forward mode='nat'>
    <nat>
      <port start='1024' end='65535'/>
    </nat>
  </forward>
  <bridge name='%s' stp='on' delay='0'/>
  <domain name='%s' localOnly='yes'/>
  <ip address='%s.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='%s.5' end='%s.254'/>
    </dhcp>
  </ip>
</network>`

// CreateNetwork defines, starts, and autostart-enables a libvirt NAT network.
func (p *LibvirtNetworkProvisioner) CreateNetwork(ctx context.Context, spec interfaces.NetworkSpec) error {
	xml := fmt.Sprintf(libvirtNetworkXMLTemplate,
		spec.Name, spec.Bridge, spec.Domain, spec.Subnet, spec.Subnet, spec.Subnet)

	xmlFile, err := writeTempFile(spec.Name+"-network-*.xml", []byte(xml))
	if err != nil {
		return fmt.Errorf("write network XML: %w", err)
	}
	defer os.Remove(xmlFile)

	if _, err := p.cmd.Run(ctx, virshCmd, "net-define", xmlFile); err != nil {
		return fmt.Errorf("net-define %s: %w", spec.Name, err)
	}
	if _, err := p.cmd.Run(ctx, virshCmd, "net-start", spec.Name); err != nil {
		return fmt.Errorf("net-start %s: %w", spec.Name, err)
	}
	if _, err := p.cmd.Run(ctx, virshCmd, "net-autostart", spec.Name); err != nil {
		return fmt.Errorf("net-autostart %s: %w", spec.Name, err)
	}
	return nil
}

// DeleteNetwork destroys and undefines a libvirt network.
func (p *LibvirtNetworkProvisioner) DeleteNetwork(ctx context.Context, name string) error {
	_, _ = p.cmd.Run(ctx, virshCmd, "net-destroy", name)
	if _, err := p.cmd.Run(ctx, virshCmd, "net-undefine", name); err != nil {
		return fmt.Errorf("net-undefine %s: %w", name, err)
	}
	return nil
}

func writeTempFile(pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	return f.Name(), nil
}
