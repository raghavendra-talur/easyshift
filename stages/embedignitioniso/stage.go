// Package embedignitioniso produces the per-cluster master ISO by embedding
// the SNO ignition into the cached live ISO, then uploads it to the libvirt
// storage pool so qemu can boot from it.
package embedignitioniso

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// Stage builds and uploads the master boot ISO.
type Stage struct {
	installer interfaces.Installer
	vm        interfaces.VMManager
}

// New returns the embed-ignition-iso stage.
func New(installer interfaces.Installer, vm interfaces.VMManager) *Stage {
	return &Stage{installer: installer, vm: vm}
}

func (*Stage) Name() string { return "embed-ignition-iso" }

// Preflight verifies the storage pool is ready before uploading the ISO.
func (s *Stage) Preflight(ctx context.Context, sc *interfaces.StageContext) error {
	return s.vm.StoragePoolActive(ctx, sc.Cluster.StoragePool)
}

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	srcISO := sc.RHCOSLiveISOPath()
	ignition := filepath.Join(sc.ClusterDir(), "bootstrap-in-place-for-live-iso.ign")
	local := sc.MasterISOPath()
	if err := s.installer.EmbedIgnitionInISO(ctx, sc.InstallerSpec(), srcISO, ignition, local); err != nil {
		return err
	}
	// In bridge mode the node's address comes from the user's router DHCP, so
	// it can race: grab a pool address before the MAC reservation takes effect,
	// and etcd bakes the wrong IP in permanently. Embed a NetworkManager
	// keyfile that pins the reserved IP statically from first boot, removing
	// the dependency on DHCP timing entirely.
	if sc.Cluster.NetworkMode == config.NetworkModeBridge {
		keyfile, err := sc.Cluster.StaticNetworkKeyfile()
		if err != nil {
			return fmt.Errorf("render static network keyfile: %w", err)
		}
		keyfilePath := filepath.Join(sc.ClusterDir(), "master.nmconnection")
		if err := os.WriteFile(keyfilePath, []byte(keyfile), 0o600); err != nil {
			return fmt.Errorf("write network keyfile: %w", err)
		}
		if err := s.installer.EmbedNetworkKeyfileInISO(ctx, sc.InstallerSpec(), keyfilePath, local); err != nil {
			return err
		}
	}
	// Upload into the pool so qemu (not just the easyshift user) can read it.
	volPath, err := s.vm.ImportISO(ctx, sc.Cluster.StoragePool, sc.MasterISOVolName(), local)
	if err != nil {
		return err
	}
	sc.Cluster.BootISOVolPath = volPath
	return sc.Config.Save()
}

func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	_ = s.vm.RemoveISO(ctx, sc.Cluster.StoragePool, sc.MasterISOVolName())
	_ = os.Remove(sc.MasterISOPath())
	sc.Cluster.BootISOVolPath = ""
	return sc.Config.Save()
}
