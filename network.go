package easyshift

import (
	"crypto/rand"
	"fmt"
	"net"
)

const macPrefix = "52:54:00"

// NetworkAllocator allocates IP and MAC addresses for cluster nodes,
// recording usage in Config.GlobalState. It is not a side-effect boundary
// (pure data manipulation), so it is a plain struct rather than an interface.
type NetworkAllocator struct {
	cfg *Config
}

// NewNetworkAllocator returns an allocator that records state in cfg.GlobalState.
func NewNetworkAllocator(cfg *Config) *NetworkAllocator {
	return &NetworkAllocator{cfg: cfg}
}

// Allocate populates cluster.MACAddresses for every node and, in NAT mode,
// also populates cluster.IPAddresses. In bridge mode IPs come from the LAN
// DHCP server so we do not pre-allocate them.
func (a *NetworkAllocator) Allocate(cluster *ClusterConfig) error {
	total := cluster.MasterCount + cluster.WorkerCount
	macs := make([]string, total)
	for i := 0; i < total; i++ {
		mac, err := a.allocateMAC()
		if err != nil {
			return fmt.Errorf("allocate mac for node %d: %w", i, err)
		}
		macs[i] = mac
	}
	cluster.MACAddresses = macs

	if cluster.NetworkMode != NetworkModeNAT {
		// Direct mode: no static IPs, no subnet to record.
		cluster.IPAddresses = nil
		cluster.NetworkSubnet = ""
		return nil
	}

	ips := make([]string, total)
	for i := 0; i < total; i++ {
		ip, err := a.allocateIP()
		if err != nil {
			return fmt.Errorf("allocate ip for node %d: %w", i, err)
		}
		ips[i] = ip
	}
	cluster.IPAddresses = ips
	cluster.NetworkSubnet = BaseNetworkRange
	return nil
}

// Release marks the cluster's IPs and MACs as free.
func (a *NetworkAllocator) Release(cluster *ClusterConfig) {
	for _, ip := range cluster.IPAddresses {
		delete(a.cfg.GlobalState.UsedIPs, ip)
	}
	for _, mac := range cluster.MACAddresses {
		delete(a.cfg.GlobalState.UsedMACs, mac)
	}
}

func (a *NetworkAllocator) allocateIP() (string, error) {
	for i := NetworkStart; i <= NetworkEnd; i++ {
		ip := fmt.Sprintf("%s.%d", BaseNetworkRange, i)
		if !a.cfg.GlobalState.UsedIPs[ip] {
			a.cfg.GlobalState.UsedIPs[ip] = true
			return ip, nil
		}
	}
	return "", fmt.Errorf("no available IP addresses in range %s.%d-%d",
		BaseNetworkRange, NetworkStart, NetworkEnd)
}

func (a *NetworkAllocator) allocateMAC() (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		mac, err := generateMAC()
		if err != nil {
			return "", err
		}
		if !a.cfg.GlobalState.UsedMACs[mac] {
			a.cfg.GlobalState.UsedMACs[mac] = true
			return mac, nil
		}
	}
	return "", fmt.Errorf("failed to allocate unique MAC after 100 attempts")
}

func generateMAC() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return fmt.Sprintf("%s:%02x:%02x:%02x", macPrefix, b[0], b[1], b[2]), nil
}

// ValidateIP returns true if s is a syntactically valid IP address.
func ValidateIP(s string) bool { return net.ParseIP(s) != nil }

// DeriveMachineCIDR returns the /24 containing masterIP. Errors if masterIP
// is not a valid IPv4 address. The /24 default matches the most common
// home/LAN setup; users with /23 or /22 LANs should set --machine-cidr.
func DeriveMachineCIDR(masterIP string) (string, error) {
	ip := net.ParseIP(masterIP)
	if ip == nil {
		return "", fmt.Errorf("invalid IP %q", masterIP)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", fmt.Errorf("not an IPv4 address: %q", masterIP)
	}
	return fmt.Sprintf("%d.%d.%d.0/24", ip4[0], ip4[1], ip4[2]), nil
}

// ValidateMAC returns true if s is a syntactically valid MAC address.
func ValidateMAC(s string) bool {
	_, err := net.ParseMAC(s)
	return err == nil
}
