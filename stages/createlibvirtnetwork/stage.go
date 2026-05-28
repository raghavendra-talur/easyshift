// Package createlibvirtnetwork creates the libvirt NAT network for NAT-mode
// clusters. In bridge mode every method is a no-op.
package createlibvirtnetwork

import (
	"context"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// Stage provisions the cluster's libvirt NAT network.
type Stage struct {
	net interfaces.NetworkProvisioner
	vm  interfaces.VMManager
}

// New returns the create-libvirt-network stage. vm is used only for the
// libvirt-reachability preflight.
func New(net interfaces.NetworkProvisioner, vm interfaces.VMManager) *Stage {
	return &Stage{net: net, vm: vm}
}

func (*Stage) Name() string { return "create-libvirt-network" }

// Preflight ensures qemu:///system is reachable before NAT-mode side effects.
func (s *Stage) Preflight(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.NetworkMode != config.NetworkModeNAT {
		return nil
	}
	return s.vm.CheckAccess(ctx)
}

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.NetworkMode != config.NetworkModeNAT {
		return nil
	}
	return s.net.CreateNetwork(ctx, interfaces.NetworkSpec{
		Name:   sc.NetworkName(),
		Bridge: sc.NetworkName(),
		Subnet: sc.Cluster.NetworkSubnet,
		Domain: sc.Cluster.FQDN(),
	})
}

func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.NetworkMode != config.NetworkModeNAT {
		return nil
	}
	return s.net.DeleteNetwork(ctx, sc.NetworkName())
}
