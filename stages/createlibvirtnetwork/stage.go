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
	c := sc.Cluster
	spec := interfaces.NetworkSpec{
		Name: sc.NetworkName(),
		// Bridge left empty: libvirt auto-assigns a virbrN. The network name
		// can be long, but a bridge *interface* name must fit in 15 chars.
		Subnet: c.NetworkSubnet,
		// DHCP reservation pins the master IP and supplies its hostname via
		// option 12, so RHCOS's node-valid-hostname is satisfied with no SSH
		// hostname injector.
		ReserveMAC:      firstOrEmpty(c.MACAddresses),
		ReserveIP:       c.PrimaryMasterIP(),
		ReserveHostname: "master-0",
	}
	// Under magic DNS the cluster names live on a public wildcard service
	// (sslip.io/nip.io), so we must NOT make libvirt's dnsmasq authoritative
	// for that domain — leave Domain unset so queries forward upstream.
	if c.MagicDNS == "" {
		spec.Domain = c.FQDN()
	}
	return s.net.CreateNetwork(ctx, spec)
}

func firstOrEmpty(ss []string) string {
	if len(ss) > 0 {
		return ss[0]
	}
	return ""
}

func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.NetworkMode != config.NetworkModeNAT {
		return nil
	}
	return s.net.DeleteNetwork(ctx, sc.NetworkName())
}
