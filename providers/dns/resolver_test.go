package dns_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/providers/dns"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

// TestResolve_FiltersToIPs locks in that intermediate CNAME lines from
// `dig +short` are dropped and only literal IPs are returned.
func TestResolve_FiltersToIPs(t *testing.T) {
	cmd := &fakes.CommandRunner{Output: []byte("api.example.com.cdn.example.net.\n192.168.1.10\n192.168.1.11\n")}
	r := dns.NewDigDNSResolver(cmd)

	ips, err := r.Resolve(context.Background(), "api.demo.example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"192.168.1.10", "192.168.1.11"}; !reflect.DeepEqual(ips, want) {
		t.Errorf("ips: got %v want %v", ips, want)
	}
}

// TestResolve_ErrorHints verifies a missing dig binary gets an install hint
// (the resolver also backs `easyshift status`, which has no preflight) while
// other failures keep the plain dig error.
func TestResolve_ErrorHints(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "missing binary",
			err:  fmt.Errorf("command dig failed: %w", exec.ErrNotFound),
			want: "install bind-utils",
		},
		{
			name: "lookup failure",
			err:  errors.New("command dig failed: exit status 9"),
			want: "dig api.demo.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &fakes.CommandRunner{Err: tc.err}
			r := dns.NewDigDNSResolver(cmd)
			_, err := r.Resolve(context.Background(), "api.demo.example.com")
			if err == nil {
				t.Fatal("Resolve: expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err, tc.want)
			}
		})
	}
}
