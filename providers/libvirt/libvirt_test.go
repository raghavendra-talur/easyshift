package libvirt_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift/interfaces"
	"github.com/raghavendra-talur/easyshift/providers/fakes"
	"github.com/raghavendra-talur/easyshift/providers/libvirt"
)

// TestLibvirtVMManager_CreateArgs locks in the virt-install argument list,
// in particular the --osinfo flag that modern virt-install requires (its
// absence is a fatal error on Fedora 39+).
func TestLibvirtVMManager_CreateArgs(t *testing.T) {
	cmd := &fakes.CommandRunner{}
	vm := libvirt.NewLibvirtVMManager(cmd)

	err := vm.Create(context.Background(), interfaces.VMSpec{
		Name:        "master-0-demo",
		MemoryMiB:   16000,
		VCPUs:       4,
		DiskSizeGiB: 120,
		MAC:         "52:54:00:11:22:33",
		NetworkArg:  "bridge=br0,mac=52:54:00:11:22:33",
		BootISO:     "/var/lib/libvirt/images/easyshift-demo-master.iso",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(cmd.Calls) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmd.Calls))
	}
	call := cmd.Calls[0]
	if call.Name != "virt-install" {
		t.Errorf("command: got %q want virt-install", call.Name)
	}
	joined := strings.Join(call.Args, " ")

	// --connect qemu:///system must be present: bare virt-install defaults to
	// qemu:///session for non-root users, where NAT bridge creation fails with
	// "Operation not permitted".
	if !hasFlagWithValue(call.Args, "--connect", "qemu:///system") {
		t.Errorf("virt-install args missing --connect qemu:///system: %s", joined)
	}
	// --boot hd,cdrom is the OCP-prescribed boot order for SNO bootstrap-in-
	// place: the empty disk falls through to the ISO on first boot, and the
	// post-install reboot pivots onto the installed OS rather than re-running
	// the live environment. Without this the VM re-boots the ISO in a loop.
	if !hasFlagWithValue(call.Args, "--boot", "hd,cdrom") {
		t.Errorf("virt-install args missing --boot hd,cdrom: %s", joined)
	}
	// --osinfo must be present (regression: modern virt-install requires it).
	if !hasFlagValue(call.Args, "--osinfo") {
		t.Errorf("virt-install args missing --osinfo: %s", joined)
	}
	// Booting from ISO uses --cdrom, not --pxe.
	if !hasFlagValue(call.Args, "--cdrom") {
		t.Errorf("expected --cdrom for ISO boot: %s", joined)
	}
	if contains(call.Args, "--pxe") {
		t.Errorf("must not pass --pxe when BootISO is set: %s", joined)
	}
	if !hasFlagValue(call.Args, "--network") {
		t.Errorf("expected --network: %s", joined)
	}
}

// TestEnsureNetwork_UndefinesOnFailedStart confirms that when the network
// doesn't yet exist and net-start fails after net-define, EnsureNetwork
// undefines it so a leftover defined-but-inactive network can't block the
// next attempt's net-define.
func TestEnsureNetwork_UndefinesOnFailedStart(t *testing.T) {
	cmd := &fakes.CommandRunner{
		RunFunc: func(_ string, args []string) ([]byte, error) {
			if contains(args, "net-info") {
				return nil, errors.New("Network not found") // doesn't exist yet
			}
			if contains(args, "net-start") {
				return nil, errors.New("boom")
			}
			return nil, nil
		},
	}
	p := libvirt.NewLibvirtNetworkProvisioner(cmd)

	err := p.EnsureNetwork(context.Background(), interfaces.NetworkSpec{
		Name:   "easyshift-nat",
		Subnet: "192.168.126",
	})
	if err == nil || !strings.Contains(err.Error(), "net-start") {
		t.Fatalf("expected net-start failure, got %v", err)
	}
	var sawUndefine bool
	for _, call := range cmd.Calls {
		if contains(call.Args, "net-undefine") {
			sawUndefine = true
		}
	}
	if !sawUndefine {
		t.Error("expected net-undefine cleanup after a failed net-start")
	}
}

// TestEnsureNetwork_IdempotentWhenExists confirms that if the network already
// exists, EnsureNetwork does not re-define it (just ensures it's started).
func TestEnsureNetwork_IdempotentWhenExists(t *testing.T) {
	cmd := &fakes.CommandRunner{} // all commands succeed -> net-info "exists"
	p := libvirt.NewLibvirtNetworkProvisioner(cmd)

	if err := p.EnsureNetwork(context.Background(), interfaces.NetworkSpec{
		Name:   "easyshift-nat",
		Subnet: "192.168.126",
	}); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	for _, call := range cmd.Calls {
		if contains(call.Args, "net-define") {
			t.Error("net-define must not be called when the network already exists")
		}
		// Every virsh call must target qemu:///system explicitly (otherwise a
		// non-root user falls back to the unprivileged session daemon).
		if !hasFlagWithValue(call.Args, "-c", "qemu:///system") {
			t.Errorf("virsh call missing -c qemu:///system: %v", call.Args)
		}
	}
}

func hasFlagValue(args []string, flag string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] != "" {
			return true
		}
	}
	return false
}

func hasFlagWithValue(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
