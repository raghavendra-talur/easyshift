package easyshift

import (
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	macPrefix = "52:54:00"
)

// NetworkManager handles IP and MAC address allocation
type NetworkManager struct {
	sync.Mutex
	usedIPs  map[string]bool
	usedMACs map[string]bool
}

var (
	networkManager *NetworkManager
	networkOnce    sync.Once
)

// GetNetworkManager returns the singleton network manager instance
func GetNetworkManager() *NetworkManager {
	networkOnce.Do(func() {
		networkManager = &NetworkManager{
			usedIPs:  make(map[string]bool),
			usedMACs: make(map[string]bool),
		}
		// Load used IPs and MACs from config
		cfg := GetConfig()
		for _, cluster := range cfg.Clusters {
			for _, ip := range cluster.IPAddresses {
				networkManager.usedIPs[ip] = true
			}
			for _, mac := range cluster.MACAddresses {
				networkManager.usedMACs[mac] = true
			}
		}
	})
	return networkManager
}

// AllocateNetworkResources allocates IP and MAC addresses for a cluster
func (nm *NetworkManager) AllocateNetworkResources(cluster *ClusterConfig) error {
	nm.Lock()
	defer nm.Unlock()

	// Calculate total nodes
	totalNodes := cluster.MasterCount + cluster.WorkerCount

	// Allocate subnet
	//subnet := fmt.Sprintf("%s.0/24", BaseNetworkRange)
	cluster.NetworkSubnet = BaseNetworkRange

	// Allocate IPs and MACs
	ips := make([]string, totalNodes)
	macs := make([]string, totalNodes)

	// Allocate for master nodes first
	for i := 0; i < cluster.MasterCount; i++ {
		ip, err := nm.allocateIP()
		if err != nil {
			return fmt.Errorf("failed to allocate IP for master-%d: %w", i, err)
		}
		mac, err := nm.allocateMAC()
		if err != nil {
			return fmt.Errorf("failed to allocate MAC for master-%d: %w", i, err)
		}
		ips[i] = ip
		macs[i] = mac
	}

	// Allocate for worker nodes
	for i := 0; i < cluster.WorkerCount; i++ {
		ip, err := nm.allocateIP()
		if err != nil {
			return fmt.Errorf("failed to allocate IP for worker-%d: %w", i, err)
		}
		mac, err := nm.allocateMAC()
		if err != nil {
			return fmt.Errorf("failed to allocate MAC for worker-%d: %w", i, err)
		}
		ips[cluster.MasterCount+i] = ip
		macs[cluster.MasterCount+i] = mac
	}

	cluster.IPAddresses = ips
	cluster.MACAddresses = macs

	return nil
}

// ReleaseNetworkResources releases allocated IP and MAC addresses
func (nm *NetworkManager) ReleaseNetworkResources(cluster *ClusterConfig) {
	nm.Lock()
	defer nm.Unlock()

	for _, ip := range cluster.IPAddresses {
		delete(nm.usedIPs, ip)
	}
	for _, mac := range cluster.MACAddresses {
		delete(nm.usedMACs, mac)
	}
}

func (nm *NetworkManager) allocateIP() (string, error) {
	for i := NetworkStart; i <= NetworkEnd; i++ {
		ip := fmt.Sprintf("%s.%d", BaseNetworkRange, i)
		if !nm.usedIPs[ip] {
			nm.usedIPs[ip] = true
			return ip, nil
		}
	}
	return "", fmt.Errorf("no available IP addresses")
}

func (nm *NetworkManager) allocateMAC() (string, error) {
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 100; i++ { // Try 100 times
		mac := generateMAC()
		if !nm.usedMACs[mac] {
			nm.usedMACs[mac] = true
			return mac, nil
		}
	}
	return "", fmt.Errorf("failed to allocate MAC address")
}

func generateMAC() string {
	bytes := make([]string, 3)
	for i := 0; i < 3; i++ {
		bytes[i] = fmt.Sprintf("%02x", rand.Intn(256))
	}
	return fmt.Sprintf("%s:%s", macPrefix, strings.Join(bytes, ":"))
}

// IsIPAvailable checks if an IP address is available
func (nm *NetworkManager) IsIPAvailable(ip string) bool {
	nm.Lock()
	defer nm.Unlock()
	return !nm.usedIPs[ip]
}

// IsMACAvailable checks if a MAC address is available
func (nm *NetworkManager) IsMACAvailable(mac string) bool {
	nm.Lock()
	defer nm.Unlock()
	return !nm.usedMACs[mac]
}

// ValidateIP validates an IP address format
func ValidateIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// ValidateMAC validates a MAC address format
func ValidateMAC(mac string) bool {
	_, err := net.ParseMAC(mac)
	return err == nil
}
