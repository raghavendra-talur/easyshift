package libvirt

import (
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift/interfaces"
)

// TestBuildNetworkXML_Shared covers the shared NAT network shape: the bridge
// interface name is omitted (so the kernel's 15-char IFNAMSIZ limit can't be
// blown — the regression that broke NAT), no <domain> is present (magic DNS
// forwards wildcard-service queries upstream), and the DHCP range is set.
// Reservations are NOT baked into the definition (they're added via
// net-update), so no <host> should appear here.
func TestBuildNetworkXML_Shared(t *testing.T) {
	xml := buildNetworkXML(interfaces.NetworkSpec{
		Name:   "easyshift-nat",
		Subnet: "192.168.126",
	})
	if strings.Contains(xml, "<bridge name=") {
		t.Errorf("bridge interface name must be omitted (15-char limit):\n%s", xml)
	}
	if strings.Contains(xml, "<domain") {
		t.Errorf("shared network must omit <domain> under magic DNS:\n%s", xml)
	}
	if strings.Contains(xml, "<host ") {
		t.Errorf("reservations are added via net-update, not baked in:\n%s", xml)
	}
	// The dynamic pool must stay clear of the static reservation band
	// [NetworkStart, NetworkEnd] (.5-.20) so a master can never be handed a
	// dynamic lease that collides with — or strays from — its reserved address.
	if !strings.Contains(xml, "<range start='192.168.126.100' end='192.168.126.254'/>") {
		t.Errorf("expected DHCP range disjoint from reservations:\n%s", xml)
	}
}

// TestParseNetworkXML covers extracting the DHCP range and static reservations
// from `virsh net-dumpxml` output, as nat-network reset relies on.
func TestParseNetworkXML(t *testing.T) {
	xml := `<network>
  <name>easyshift-nat</name>
  <forward mode='nat'/>
  <ip address='192.168.126.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.126.100' end='192.168.126.254'/>
      <host mac='52:54:00:aa:bb:cc' name='master-0-c1' ip='192.168.126.5'/>
      <host mac='52:54:00:99:99:99' name='master-0-gone' ip='192.168.126.6'/>
    </dhcp>
  </ip>
</network>`
	start, end := parseDHCPRange(xml)
	if start != "192.168.126.100" || end != "192.168.126.254" {
		t.Errorf("range: got %s-%s", start, end)
	}
	hosts := parseReservations(xml)
	if len(hosts) != 2 {
		t.Fatalf("expected 2 reservations, got %d: %+v", len(hosts), hosts)
	}
	if hosts[0].MAC != "52:54:00:aa:bb:cc" || hosts[0].IP != "192.168.126.5" || hosts[0].Hostname != "master-0-c1" {
		t.Errorf("reservation[0] parsed wrong: %+v", hosts[0])
	}
}

// TestParseLeases covers the best-effort `virsh net-dhcp-leases` table parser.
func TestParseLeases(t *testing.T) {
	out := ` Expiry Time           MAC address         Protocol   IP address            Hostname     Client ID
-----------------------------------------------------------------------------------------------------
 2026-06-08 18:00:00   52:54:00:11:22:33   ipv4       192.168.126.250/24    -            01:52:54:00:11:22:33
`
	leases := parseLeases(out)
	if len(leases) != 1 {
		t.Fatalf("expected 1 lease, got %d: %+v", len(leases), leases)
	}
	if leases[0].MAC != "52:54:00:11:22:33" || leases[0].IP != "192.168.126.250" {
		t.Errorf("lease parsed wrong: %+v", leases[0])
	}
	if leases[0].Hostname != "" {
		t.Errorf("expected empty hostname for '-', got %q", leases[0].Hostname)
	}
}

// TestBuildNetworkXML_WithDomain covers a (non-magic) network with an explicit
// short bridge name and a local DNS domain.
func TestBuildNetworkXML_WithDomain(t *testing.T) {
	xml := buildNetworkXML(interfaces.NetworkSpec{
		Name:   "n",
		Bridge: "virbr9",
		Subnet: "192.168.126",
		Domain: "demo.local",
	})
	if !strings.Contains(xml, "<bridge name='virbr9'") {
		t.Errorf("expected explicit bridge name:\n%s", xml)
	}
	if !strings.Contains(xml, "<domain name='demo.local'") {
		t.Errorf("expected <domain> element:\n%s", xml)
	}
}
