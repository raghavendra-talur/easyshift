package app

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// NATResetPlan is the read-only analysis of drift between the shared NAT
// network and easyshift's config: an outdated DHCP range, reservations or
// dynamic leases that belong to no known cluster, and leaked IP/MAC entries in
// GlobalState. It drives the `nat-network reset` command.
type NATResetPlan struct {
	NetworkExists bool
	ExpectedRange string // e.g. "192.168.126.100-254"
	CurrentRange  string // empty when the network defines no range / is absent
	RangeOutdated bool

	OrphanReservations []interfaces.DHCPHost
	StaleLeases        []interfaces.DHCPLease
	LeakedIPs          []string
	LeakedMACs         []string

	// RunningClusters are NAT clusters whose VM is up; recreating the network
	// would briefly disrupt them. Recreate means the network must be torn down
	// and rebuilt (only an outdated range requires this). Blocked is set when a
	// recreate is needed but unsafe without --force.
	RunningClusters []string
	Recreate        bool
	Blocked         bool
}

// HasWork reports whether the plan would change anything. Stale leases alone
// don't count: when the range is current they're inert and expire on their own,
// and easyshift never tears down a healthy network just to flush them.
func (p *NATResetPlan) HasWork() bool {
	return p.Recreate || len(p.OrphanReservations) > 0 ||
		len(p.LeakedIPs) > 0 || len(p.LeakedMACs) > 0
}

// PlanNATReset inspects the shared NAT network and compares it against the
// clusters easyshift knows about. force only affects whether a needed recreate
// is reported as Blocked (running clusters present).
func (cm *ClusterManager) PlanNATReset(ctx context.Context, force bool) (*NATResetPlan, error) {
	info, err := cm.deps.Net.InspectNetwork(ctx, config.SharedNATNetwork)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", config.SharedNATNetwork, err)
	}

	plan := &NATResetPlan{
		NetworkExists: info.Exists,
		ExpectedRange: fmt.Sprintf("%s.%d-%d", config.BaseNetworkRange, config.DHCPDynamicStart, config.DHCPDynamicEnd),
	}

	knownIPs, knownMACs := cm.knownAddrs()

	if info.Exists {
		if info.DHCPRangeStart != "" {
			plan.CurrentRange = fmt.Sprintf("%s-%s", info.DHCPRangeStart, info.DHCPRangeEnd)
		}
		expectedStart := fmt.Sprintf("%s.%d", config.BaseNetworkRange, config.DHCPDynamicStart)
		plan.RangeOutdated = info.DHCPRangeStart != "" && info.DHCPRangeStart != expectedStart

		for _, h := range info.Reservations {
			if !knownMACs[strings.ToLower(h.MAC)] && !knownIPs[h.IP] {
				plan.OrphanReservations = append(plan.OrphanReservations, h)
			}
		}
		for _, l := range info.Leases {
			if !knownIPs[l.IP] {
				plan.StaleLeases = append(plan.StaleLeases, l)
			}
		}
	}

	for ip := range cm.cfg.GlobalState.UsedIPs {
		if !knownIPs[ip] {
			plan.LeakedIPs = append(plan.LeakedIPs, ip)
		}
	}
	for mac := range cm.cfg.GlobalState.UsedMACs {
		if !knownMACs[strings.ToLower(mac)] {
			plan.LeakedMACs = append(plan.LeakedMACs, mac)
		}
	}
	sort.Strings(plan.LeakedIPs)
	sort.Strings(plan.LeakedMACs)

	// Only an outdated range forces a tear-down (which also flushes stale
	// leases). Orphaned reservations are removed in place when the range is OK.
	plan.Recreate = plan.RangeOutdated
	if plan.Recreate {
		for _, c := range cm.cfg.Clusters {
			if c.NetworkMode != config.NetworkModeNAT {
				continue
			}
			if running, _ := cm.deps.VM.IsRunning(ctx, fmt.Sprintf("master-0-%s", c.Name)); running {
				plan.RunningClusters = append(plan.RunningClusters, c.Name)
			}
		}
		sort.Strings(plan.RunningClusters)
		if len(plan.RunningClusters) > 0 && !force {
			plan.Blocked = true
		}
	}

	return plan, nil
}

// ApplyNATReset executes the plan: recreate the network when its range is
// outdated (restoring the surviving clusters' reservations), otherwise remove
// orphaned reservations in place — and prune leaked allocation state either way.
func (cm *ClusterManager) ApplyNATReset(ctx context.Context, plan *NATResetPlan) error {
	if plan.Blocked {
		return fmt.Errorf("resetting %s needs to recreate it (outdated DHCP range), "+
			"which would disrupt running cluster(s): %s\n  stop them with `easyshift stop <name>` or re-run with --force",
			config.SharedNATNetwork, strings.Join(plan.RunningClusters, ", "))
	}

	if plan.Recreate {
		if err := cm.deps.Net.ResetNetwork(ctx, config.SharedNATNetwork); err != nil {
			return fmt.Errorf("reset network: %w", err)
		}
		// Rebuild immediately and restore the surviving clusters' reservations
		// so they keep working; if none remain, leave it for the next create.
		// The recreate drops every old reservation, so orphans vanish with it.
		if nat := cm.natClusters(); len(nat) > 0 {
			if err := cm.deps.Net.EnsureNetwork(ctx, interfaces.NetworkSpec{
				Name:   config.SharedNATNetwork,
				Subnet: config.BaseNetworkRange,
			}); err != nil {
				return fmt.Errorf("recreate network: %w", err)
			}
			for _, c := range nat {
				mac, ip := c.PrimaryMasterMAC(), c.PrimaryMasterIP()
				if mac == "" || ip == "" {
					continue
				}
				if err := cm.deps.Net.AddHost(ctx, config.SharedNATNetwork, interfaces.DHCPHost{
					MAC: mac, IP: ip, Hostname: fmt.Sprintf("master-0-%s", c.Name),
				}); err != nil {
					return fmt.Errorf("restore reservation for %s: %w", c.Name, err)
				}
			}
		}
	} else {
		for _, h := range plan.OrphanReservations {
			if err := cm.deps.Net.RemoveHost(ctx, config.SharedNATNetwork, h); err != nil {
				return fmt.Errorf("remove orphan reservation %s: %w", h.MAC, err)
			}
		}
	}

	for _, ip := range plan.LeakedIPs {
		delete(cm.cfg.GlobalState.UsedIPs, ip)
	}
	for _, mac := range plan.LeakedMACs {
		delete(cm.cfg.GlobalState.UsedMACs, mac)
	}
	return cm.cfg.Save()
}

// knownAddrs returns the set of IPs and (lowercased) MACs owned by registered
// clusters.
func (cm *ClusterManager) knownAddrs() (ips, macs map[string]bool) {
	ips, macs = map[string]bool{}, map[string]bool{}
	for _, c := range cm.cfg.Clusters {
		for _, ip := range c.IPAddresses {
			ips[ip] = true
		}
		for _, mac := range c.MACAddresses {
			macs[strings.ToLower(mac)] = true
		}
	}
	return ips, macs
}

func (cm *ClusterManager) natClusters() []*config.ClusterConfig {
	var out []*config.ClusterConfig
	for _, c := range cm.cfg.Clusters {
		if c.NetworkMode == config.NetworkModeNAT {
			out = append(out, c)
		}
	}
	return out
}

// Print writes a human-readable summary. applied is false for a dry run.
func (p *NATResetPlan) Print(w io.Writer, applied bool) {
	fmt.Fprintf(w, "Shared NAT network: %s\n", config.SharedNATNetwork)
	if !p.NetworkExists {
		fmt.Fprintln(w, "  not defined — it will be created on the next `easyshift create`.")
	} else {
		rangeNote := p.CurrentRange
		if p.RangeOutdated {
			rangeNote += fmt.Sprintf("  (OUTDATED — expected %s)", p.ExpectedRange)
		}
		fmt.Fprintf(w, "  DHCP range: %s\n", rangeNote)
	}

	if !p.HasWork() {
		fmt.Fprintln(w, "\nNothing to do — the network and config are consistent.")
		if len(p.StaleLeases) > 0 {
			fmt.Fprintf(w, "(%d stale dynamic lease(s) present but harmless; they expire on their own.)\n", len(p.StaleLeases))
		}
		return
	}

	verb := func(future, past string) string {
		if applied {
			return past
		}
		return future
	}

	fmt.Fprintln(w)
	if p.Recreate {
		fmt.Fprintf(w, "- %s the network to apply the corrected DHCP range (also flushes %d stale lease(s)).\n",
			verb("recreate", "recreated"), len(p.StaleLeases))
		if len(p.RunningClusters) > 0 {
			fmt.Fprintf(w, "  note: running cluster(s) %s briefly lose connectivity during the rebuild.\n",
				strings.Join(p.RunningClusters, ", "))
		}
	}
	for _, h := range p.OrphanReservations {
		fmt.Fprintf(w, "- %s orphaned reservation %s -> %s (%s).\n",
			verb("remove", "removed"), h.MAC, h.IP, h.Hostname)
	}
	for _, ip := range p.LeakedIPs {
		fmt.Fprintf(w, "- %s leaked IP allocation %s from config.\n", verb("free", "freed"), ip)
	}
	for _, mac := range p.LeakedMACs {
		fmt.Fprintf(w, "- %s leaked MAC allocation %s from config.\n", verb("free", "freed"), mac)
	}

	if !applied {
		fmt.Fprintln(w, "\nRe-run without --dry-run to apply.")
	}
}
