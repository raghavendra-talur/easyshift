package allocatenetwork

import (
	"crypto/rand"
	"fmt"

	"github.com/TheEasyShift/easyshift/config"
)

const macPrefix = "52:54:00"

// allocator allocates IP/MAC addresses, recording usage in
// config.GlobalState. Not a side-effect boundary (pure data), so it's a
// plain struct local to this stage.
type allocator struct {
	cfg *config.Config
}

func newAllocator(cfg *config.Config) *allocator { return &allocator{cfg: cfg} }

// allocate fills cluster.MACAddresses for every node and, in NAT mode, also
// cluster.IPAddresses. Bridge mode gets IPs from LAN DHCP, so none here.
func (a *allocator) allocate(cluster *config.ClusterConfig) error {
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

	if cluster.NetworkMode != config.NetworkModeNAT {
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
	cluster.NetworkSubnet = config.BaseNetworkRange
	return nil
}

func (a *allocator) release(cluster *config.ClusterConfig) {
	for _, ip := range cluster.IPAddresses {
		delete(a.cfg.GlobalState.UsedIPs, ip)
	}
	for _, mac := range cluster.MACAddresses {
		delete(a.cfg.GlobalState.UsedMACs, mac)
	}
}

func (a *allocator) allocateIP() (string, error) {
	for i := config.NetworkStart; i <= config.NetworkEnd; i++ {
		ip := fmt.Sprintf("%s.%d", config.BaseNetworkRange, i)
		if !a.cfg.GlobalState.UsedIPs[ip] {
			a.cfg.GlobalState.UsedIPs[ip] = true
			return ip, nil
		}
	}
	return "", fmt.Errorf("no available IP addresses in range %s.%d-%d",
		config.BaseNetworkRange, config.NetworkStart, config.NetworkEnd)
}

func (a *allocator) allocateMAC() (string, error) {
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
