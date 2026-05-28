package dns

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"

	"github.com/raghavendra-talur/easyshift/config"
)

// dnsRecordTTL is the TTL written for every cluster A record. Short enough
// that delete + re-create on the same FQDN converges quickly; long enough
// that openshift-install's repeated bootstrap DNS queries don't hammer the
// provider.
const dnsRecordTTL = 5 * time.Minute

// libdnsProvider is the subset of libdns capabilities easyshift needs.
type libdnsProvider interface {
	GetRecords(ctx context.Context, zone string) ([]libdns.Record, error)
	SetRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error)
	DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error)
}

// LibDNSManager is the production DNSManager, delegating to a libdns provider
// (Cloudflare today; others slot in with one extra case in NewManager).
type LibDNSManager struct {
	provider libdnsProvider
}

// NewManager returns a DNSManager for the given provider name. token is the
// provider's API credential — for Cloudflare a token with Zone:DNS:Edit
// scope on the target zone.
func NewManager(provider, token string) (*LibDNSManager, error) {
	if token == "" {
		return nil, fmt.Errorf("dns provider %q requires a non-empty token", provider)
	}
	switch provider {
	case config.DNSProviderCloudflare:
		return &LibDNSManager{provider: &cloudflare.Provider{APIToken: token}}, nil
	}
	return nil, fmt.Errorf("unsupported dns provider %q (supported: %s)",
		provider, config.DNSProviderCloudflare)
}

// Upsert creates or updates api/api-int/*.apps, all pointing at ip.
func (m *LibDNSManager) Upsert(ctx context.Context, zone, fqdn, ip string) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("parse master IP %q: %w", ip, err)
	}
	records, err := clusterRecords(zone, fqdn, addr)
	if err != nil {
		return err
	}
	if _, err := m.provider.SetRecords(ctx, zone, records); err != nil {
		return fmt.Errorf("upsert dns records in zone %s: %w", zone, err)
	}
	return nil
}

// Delete removes the three records Upsert created. We list the zone and
// filter by name (not by constructing records) because libdns providers
// match deletes on type+name+content — a placeholder IP would delete
// nothing. Idempotent: a missing record is not an error.
func (m *LibDNSManager) Delete(ctx context.Context, zone, fqdn string) error {
	if !isSubdomain(fqdn, zone) {
		return fmt.Errorf("fqdn %q must be a subdomain of zone %q", fqdn, zone)
	}
	all, err := m.provider.GetRecords(ctx, zone)
	if err != nil {
		return fmt.Errorf("list dns records in zone %s: %w", zone, err)
	}
	want := map[string]bool{
		libdns.RelativeName("api."+fqdn, zone):     true,
		libdns.RelativeName("api-int."+fqdn, zone): true,
		libdns.RelativeName("*.apps."+fqdn, zone):  true,
	}
	var toDelete []libdns.Record
	for _, r := range all {
		rr := r.RR()
		if rr.Type == "A" && want[rr.Name] {
			toDelete = append(toDelete, r)
		}
	}
	if len(toDelete) == 0 {
		return nil
	}
	if _, err := m.provider.DeleteRecords(ctx, zone, toDelete); err != nil {
		if isDNSNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete dns records in zone %s: %w", zone, err)
	}
	return nil
}

func clusterRecords(zone, fqdn string, addr netip.Addr) ([]libdns.Record, error) {
	if !isSubdomain(fqdn, zone) {
		return nil, fmt.Errorf("fqdn %q must be a subdomain of zone %q", fqdn, zone)
	}
	names := []string{"api." + fqdn, "api-int." + fqdn, "*.apps." + fqdn}
	out := make([]libdns.Record, 0, len(names))
	for _, fqn := range names {
		out = append(out, libdns.Address{
			Name: libdns.RelativeName(fqn, zone),
			TTL:  dnsRecordTTL,
			IP:   addr,
		})
	}
	return out, nil
}

func isSubdomain(fqdn, zone string) bool {
	fqdn = strings.TrimSuffix(fqdn, ".")
	zone = strings.TrimSuffix(zone, ".")
	if fqdn == zone {
		return false
	}
	return strings.HasSuffix(fqdn, "."+zone)
}

// isDNSNotFound treats "already absent" provider errors as delete success.
func isDNSNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "no record")
}
