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
	if !strings.Contains(xml, "<range start='192.168.126.5' end='192.168.126.254'/>") {
		t.Errorf("expected DHCP range:\n%s", xml)
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
