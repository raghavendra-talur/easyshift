package config

import (
	"strings"
	"testing"
)

func TestDeriveGateway(t *testing.T) {
	cases := []struct {
		cidr, want string
		wantErr    bool
	}{
		{"192.168.50.0/24", "192.168.50.1", false},
		{"10.0.0.0/16", "10.0.0.1", false},
		{"172.16.4.0/22", "172.16.4.1", false},
		{"not-a-cidr", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := DeriveGateway(tc.cidr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("DeriveGateway(%q): expected error, got %q", tc.cidr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("DeriveGateway(%q): %v", tc.cidr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("DeriveGateway(%q) = %q, want %q", tc.cidr, got, tc.want)
		}
	}
}

// TestStaticNetworkKeyfile_Explicit checks that explicit gateway/DNS are
// rendered verbatim and that the connection matches on the (uppercased) MAC.
func TestStaticNetworkKeyfile_Explicit(t *testing.T) {
	c := &ClusterConfig{
		Name:        "dr1",
		MasterMAC:   "52:54:00:de:ad:02",
		MasterIP:    "192.168.50.236",
		MachineCIDR: "192.168.50.0/24",
		Gateway:     "192.168.50.254",
		DNS:         "1.1.1.1,8.8.8.8",
	}
	kf, err := c.StaticNetworkKeyfile()
	if err != nil {
		t.Fatalf("StaticNetworkKeyfile: %v", err)
	}
	wants := []string{
		"mac-address=52:54:00:DE:AD:02",
		"method=manual",
		"address1=192.168.50.236/24,192.168.50.254",
		"dns=1.1.1.1;8.8.8.8;",
		"[ipv6]\nmethod=disabled",
	}
	for _, w := range wants {
		if !strings.Contains(kf, w) {
			t.Errorf("keyfile missing %q\n---\n%s", w, kf)
		}
	}
}

// TestStaticNetworkKeyfile_Defaults confirms gateway falls back to the .1 of
// the machine CIDR and DNS falls back to the gateway when both are unset.
func TestStaticNetworkKeyfile_Defaults(t *testing.T) {
	c := &ClusterConfig{
		Name:        "dr2",
		MasterMAC:   "52:54:00:de:ad:03",
		MasterIP:    "192.168.50.237",
		MachineCIDR: "192.168.50.0/24",
	}
	kf, err := c.StaticNetworkKeyfile()
	if err != nil {
		t.Fatalf("StaticNetworkKeyfile: %v", err)
	}
	if !strings.Contains(kf, "address1=192.168.50.237/24,192.168.50.1") {
		t.Errorf("expected derived gateway .1 in\n%s", kf)
	}
	if !strings.Contains(kf, "dns=192.168.50.1;") {
		t.Errorf("expected DNS to default to gateway in\n%s", kf)
	}
}

func TestStaticNetworkKeyfile_RequiresMACAndIP(t *testing.T) {
	for _, c := range []*ClusterConfig{
		{Name: "x", MasterIP: "192.168.50.1", MachineCIDR: "192.168.50.0/24"},
		{Name: "x", MasterMAC: "52:54:00:de:ad:02", MachineCIDR: "192.168.50.0/24"},
	} {
		if _, err := c.StaticNetworkKeyfile(); err == nil {
			t.Errorf("expected error for missing MAC/IP, got nil (cfg %+v)", c)
		}
	}
}
