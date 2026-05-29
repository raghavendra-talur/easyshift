// Package allocatenetwork records the cluster's MAC/IP. In bridge mode it
// just registers the user-supplied MAC/IP; in NAT mode it allocates them
// from easyshift's pool, tracked in config.GlobalState.
package allocatenetwork

import (
	"context"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// Stage allocates (or records) the cluster's network addressing.
type Stage struct{}

// New returns the allocate-network stage. It has no interface dependencies.
func New() *Stage { return &Stage{} }

func (*Stage) Name() string { return "allocate-network" }

func (*Stage) Apply(_ context.Context, sc *interfaces.StageContext) error {
	if len(sc.Cluster.MACAddresses) > 0 {
		return nil
	}
	if sc.Cluster.NetworkMode == config.NetworkModeBridge {
		// User supplied the MAC + IP at the router. Record them and register
		// the MAC so a future cluster doesn't try to claim it.
		sc.Cluster.MACAddresses = []string{sc.Cluster.MasterMAC}
		sc.Cluster.IPAddresses = []string{sc.Cluster.MasterIP}
		sc.Config.GlobalState.UsedMACs[sc.Cluster.MasterMAC] = true
		deriveMagicDomain(sc.Cluster)
		return sc.Config.Save()
	}
	// NAT mode: auto-allocate MAC and IP, then set MachineCIDR.
	if err := newAllocator(sc.Config).allocate(sc.Cluster); err != nil {
		return err
	}
	sc.Cluster.MachineCIDR = sc.Cluster.NetworkSubnet + ".0/24"
	deriveMagicDomain(sc.Cluster)
	return sc.Config.Save()
}

// deriveMagicDomain sets Domain to "<masterIP>.<service>" once the IP is
// known, when a wildcard DNS service is configured and no domain is set yet.
// On resume the Domain is already persisted, so this is a no-op.
func deriveMagicDomain(c *config.ClusterConfig) {
	if c.MagicDNS == "" || c.Domain != "" {
		return
	}
	if ip := c.PrimaryMasterIP(); ip != "" {
		c.Domain = config.MagicDomain(ip, c.MagicDNS)
	}
}

func (*Stage) Rollback(_ context.Context, sc *interfaces.StageContext) error {
	newAllocator(sc.Config).release(sc.Cluster)
	sc.Cluster.IPAddresses = nil
	sc.Cluster.MACAddresses = nil
	return sc.Config.Save()
}
