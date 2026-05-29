package libvirt

import (
	"strings"
	"testing"

	"github.com/raghavendra-talur/easyshift/interfaces"
)

// TestBuildNetworkXML_MagicDNS covers the NAT magic-DNS shape: the bridge
// interface name is omitted (so the kernel's 15-char IFNAMSIZ limit can't be
// blown by a long cluster name — the regression that broke NAT), the DHCP
// reservation is present, and the <domain> element is left out so dnsmasq
// forwards the wildcard-service queries upstream.
func TestBuildNetworkXML_MagicDNS(t *testing.T) {
	xml := buildNetworkXML(interfaces.NetworkSpec{
		Name:            "easyshift-natdev", // 16 chars — invalid as a bridge ifname
		Subnet:          "192.168.126",
		ReserveMAC:      "52:54:00:2a:2c:e0",
		ReserveIP:       "192.168.126.5",
		ReserveHostname: "master-0",
	})
	if strings.Contains(xml, "<bridge name=") {
		t.Errorf("bridge interface name must be omitted (15-char limit); got:\n%s", xml)
	}
	if !strings.Contains(xml, `<host mac='52:54:00:2a:2c:e0' name='master-0' ip='192.168.126.5'/>`) {
		t.Errorf("missing DHCP reservation:\n%s", xml)
	}
	if strings.Contains(xml, "<domain") {
		t.Errorf("magic-DNS network must omit <domain>:\n%s", xml)
	}
}

// TestBuildNetworkXML_WithDomain covers the non-magic NAT shape: an explicit
// (short) bridge name and a local DNS domain are honored.
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
