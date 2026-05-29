package config

import (
	"fmt"
	"net"
)

// MasterHostname is the FQDN baked into the master node (via the SSH
// hostname injector). Pinned deterministically rather than relying on DHCP
// option 12 or a PTR record, because RHCOS's node-valid-hostname.service
// would otherwise register the node permanently as localhost.localdomain.
func MasterHostname(c *ClusterConfig) string {
	return fmt.Sprintf("master-0.%s.%s", c.Name, c.Domain)
}

// FQDN is the cluster's fully-qualified domain name (<name>.<base-domain>).
func (c *ClusterConfig) FQDN() string {
	return c.Name + "." + c.Domain
}

// PrimaryMasterIP is the master's IP regardless of network mode: the
// user-supplied MasterIP in bridge mode, or the first allocated address in
// NAT mode (populated by the allocate-network stage).
func (c *ClusterConfig) PrimaryMasterIP() string {
	if c.MasterIP != "" {
		return c.MasterIP
	}
	if len(c.IPAddresses) > 0 {
		return c.IPAddresses[0]
	}
	return ""
}

// MagicDomain builds the wildcard-DNS base domain for an IP. sslip.io and
// nip.io both resolve "<anything>.<ip>.<service>" to <ip>, giving the
// cluster's api/api-int/*.apps names for free.
func MagicDomain(ip, service string) string {
	return ip + "." + service
}

// ValidMagicDNS reports whether s is a supported wildcard-DNS service (or
// empty, meaning off).
func ValidMagicDNS(s string) bool {
	switch s {
	case "", MagicDNSSslip, MagicDNSNip:
		return true
	}
	return false
}

// DNSZoneOrDomain returns the parent DNS zone, defaulting to the base Domain.
func (c *ClusterConfig) DNSZoneOrDomain() string {
	if c.DNSZone != "" {
		return c.DNSZone
	}
	return c.Domain
}

// ValidateIP returns true if s is a syntactically valid IP address.
func ValidateIP(s string) bool { return net.ParseIP(s) != nil }

// ValidateMAC returns true if s is a syntactically valid MAC address.
func ValidateMAC(s string) bool {
	_, err := net.ParseMAC(s)
	return err == nil
}

// DeriveMachineCIDR returns the /24 containing masterIP.
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
