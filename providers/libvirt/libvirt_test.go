package libvirt_test

import (
	"context"
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

func hasFlagValue(args []string, flag string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] != "" {
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
