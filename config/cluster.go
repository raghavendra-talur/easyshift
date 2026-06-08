package config

import (
	"fmt"
	"net"
	"strings"
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

// PrimaryMasterMAC mirrors PrimaryMasterIP for the master's MAC: the
// user-supplied MasterMAC in bridge mode, or the first allocated MAC in NAT
// mode (populated by the allocate-network stage).
func (c *ClusterConfig) PrimaryMasterMAC() string {
	if c.MasterMAC != "" {
		return c.MasterMAC
	}
	if len(c.MACAddresses) > 0 {
		return c.MACAddresses[0]
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

// DeriveGateway returns the conventional default gateway for a CIDR: the .1
// host of the network (network address + 1).
func DeriveGateway(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("not an IPv4 CIDR: %q", cidr)
	}
	gw := make(net.IP, len(ip))
	copy(gw, ip)
	gw[3]++
	return gw.String(), nil
}

// prefixLen returns the prefix length (the /N) of a CIDR.
func prefixLen(cidr string) (int, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ones, _ := ipnet.Mask.Size()
	return ones, nil
}

// StaticNetworkKeyfile renders a NetworkManager keyfile that pins the master
// node to its reserved IP. Embedded in the live ISO (via `coreos-installer iso
// network embed`), it makes the node configure its NIC statically from first
// boot — including while Ignition runs — so etcd never binds a DHCP-pool
// address grabbed before the reservation took effect. It applies to both
// network modes: in bridge mode the address races the user's router DHCP; in
// NAT mode it races libvirt's dnsmasq, where a reservation that hasn't
// propagated when the VM first DISCOVERs leaves the master on a sticky dynamic
// pool lease (the wrong-nodeIP failure). Matching on MAC binds the right
// interface regardless of kernel NIC naming, and the keyfile propagates into
// the installed system so the static IP persists past the bootstrap reboot.
// Gateway and DNS fall back to the .1 of MachineCIDR when unset.
func (c *ClusterConfig) StaticNetworkKeyfile() (string, error) {
	mac := c.PrimaryMasterMAC()
	ip := c.PrimaryMasterIP()
	if mac == "" || ip == "" {
		return "", fmt.Errorf("static network keyfile needs both master MAC and IP")
	}
	prefix, err := prefixLen(c.MachineCIDR)
	if err != nil {
		return "", err
	}
	gateway := c.Gateway
	if gateway == "" {
		if gateway, err = DeriveGateway(c.MachineCIDR); err != nil {
			return "", err
		}
	}
	dns := c.DNS
	if dns == "" {
		dns = gateway
	}
	// NetworkManager keyfile dns is ';'-separated and conventionally
	// terminated with a trailing ';'.
	dnsField := strings.ReplaceAll(dns, ",", ";")
	if !strings.HasSuffix(dnsField, ";") {
		dnsField += ";"
	}
	return fmt.Sprintf(`[connection]
id=master-0-%s
type=ethernet
autoconnect=true
autoconnect-priority=999

[ethernet]
mac-address=%s

[ipv4]
method=manual
address1=%s/%d,%s
dns=%s
may-fail=false

[ipv6]
method=disabled
`, c.Name, strings.ToUpper(mac), ip, prefix, gateway, dnsField), nil
}
