// Package upsertdns creates the cluster's public A records (api, api-int,
// *.apps) via the configured DNS provider, and removes them on rollback.
// No-op when DNSProvider is unset (the user manages DNS by hand).
package upsertdns

import (
	"context"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage manages the cluster's public DNS records.
type Stage struct {
	dns interfaces.DNSManager
}

// New returns the upsert-dns stage, injected with the DNS manager.
func New(dns interfaces.DNSManager) *Stage { return &Stage{dns: dns} }

func (*Stage) Name() string { return "upsert-dns" }

// Preflight verifies a token exists for the provider before any side effects.
func (*Stage) Preflight(_ context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	return config.EnsureDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
}

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	return s.dns.Upsert(ctx, sc.Cluster.DNSZoneOrDomain(), sc.Cluster.FQDN(), sc.Cluster.MasterIP)
}

func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	return s.dns.Delete(ctx, sc.Cluster.DNSZoneOrDomain(), sc.Cluster.FQDN())
}
