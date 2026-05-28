package easyshift

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// DigDNSResolver is the real DNSResolver, shelling out to `dig +short`.
// dig respects /etc/resolv.conf, so it queries the same servers the user's
// LAN normally uses — which is also what the master VM will eventually
// query for api-int.<name>.<domain>.
type DigDNSResolver struct {
	cmd CommandRunner
}

// NewDigDNSResolver returns a DNSResolver backed by the `dig` binary.
func NewDigDNSResolver(cmd CommandRunner) *DigDNSResolver {
	return &DigDNSResolver{cmd: cmd}
}

// Resolve returns the A records for name. CNAME chains are followed by
// `dig +short` itself; only final IPv4 addresses are returned to callers.
func (r *DigDNSResolver) Resolve(ctx context.Context, name string) ([]string, error) {
	out, err := r.cmd.Run(ctx, "dig", "+short", "+timeout=2", name)
	if err != nil {
		return nil, fmt.Errorf("dig %s: %w", name, err)
	}
	var ips []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if net.ParseIP(line) == nil {
			// dig may emit intermediate CNAME lines before the final A
			// record(s). Skip anything that isn't a literal IP.
			continue
		}
		ips = append(ips, line)
	}
	return ips, nil
}
