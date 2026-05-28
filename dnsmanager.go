package easyshift

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"
)

// DNSRecordTTL is the TTL written for every cluster A record. Short enough
// that easyshift delete + re-create on the same FQDN converges quickly;
// long enough that openshift-install's repeated DNS queries during
// bootstrap don't hammer the provider.
const DNSRecordTTL = 5 * time.Minute

// DNSProviderCloudflare is the provider name passed on the create command
// and stored on each cluster's record so Delete picks the right backend.
const DNSProviderCloudflare = "cloudflare"

// libdnsProvider is the subset of libdns capabilities easyshift needs. It
// matches what every libdns DNS provider implements; declaring it locally
// (instead of typing on libdns.Provider) keeps tests free of libdns imports.
type libdnsProvider interface {
	GetRecords(ctx context.Context, zone string) ([]libdns.Record, error)
	SetRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error)
	DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error)
}

// LibDNSManager is the production DNSManager. It delegates to a libdns
// provider (Cloudflare today, others trivially added in NewDNSManager).
type LibDNSManager struct {
	provider libdnsProvider
}

// NewDNSManager returns a DNSManager for the given provider name. token
// is the provider's API credential — for Cloudflare a token with
// Zone:DNS:Edit scope on the target zone.
func NewDNSManager(provider, token string) (*LibDNSManager, error) {
	if token == "" {
		return nil, fmt.Errorf("dns provider %q requires a non-empty token", provider)
	}
	switch provider {
	case DNSProviderCloudflare:
		return &LibDNSManager{provider: &cloudflare.Provider{APIToken: token}}, nil
	}
	return nil, fmt.Errorf("unsupported dns provider %q (supported: %s)",
		provider, DNSProviderCloudflare)
}

// Upsert creates or updates the three records OpenShift requires (api,
// api-int, *.apps), all pointing at ip.
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
// filter by name (rather than passing constructed records) because the
// underlying libdns providers match deletes on type+name+content — passing
// a placeholder IP would silently delete nothing. Best-effort: a missing
// record is not an error (delete is idempotent at the easyshift level).
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

// clusterRecords returns the libdns Address records for the cluster's
// three required names, all pointing at addr. The names are made relative
// to zone using libdns.RelativeName.
func clusterRecords(zone, fqdn string, addr netip.Addr) ([]libdns.Record, error) {
	if !isSubdomain(fqdn, zone) {
		return nil, fmt.Errorf("fqdn %q must be a subdomain of zone %q", fqdn, zone)
	}
	names := []string{
		"api." + fqdn,
		"api-int." + fqdn,
		"*.apps." + fqdn,
	}
	out := make([]libdns.Record, 0, len(names))
	for _, fqn := range names {
		out = append(out, libdns.Address{
			Name: libdns.RelativeName(fqn, zone),
			TTL:  DNSRecordTTL,
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

// isDNSNotFound returns true for libdns errors that indicate a record was
// already absent. libdns providers don't share an error type, so we match
// on string for now; this is the only place that needs updating when more
// providers are added.
func isDNSNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "no record")
}

// --- Token storage (mirrors the pull-secret pattern in pullsecret.go) ---

// DNSTokenPath returns the on-disk location of the token file for
// provider. Mode 0600 is enforced by WriteDNSToken.
func DNSTokenPath(configDir, provider string) string {
	return filepath.Join(configDir, provider+"-token")
}

// WriteDNSToken persists a provider token at DNSTokenPath with mode 0600.
// Leading/trailing whitespace is trimmed; an empty token is rejected so
// `easyshift dns set cloudflare /dev/null` fails loudly.
func WriteDNSToken(configDir, provider string, data []byte) error {
	t := strings.TrimSpace(string(data))
	if t == "" {
		return errors.New("dns token is empty")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(DNSTokenPath(configDir, provider), []byte(t), 0o600)
}

// ReadDNSToken returns the token for provider, with whitespace trimmed.
func ReadDNSToken(configDir, provider string) (string, error) {
	data, err := os.ReadFile(DNSTokenPath(configDir, provider))
	if err != nil {
		return "", fmt.Errorf("read dns token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// EnsureDNSToken returns a helpful error if the token file is missing,
// pointing at the CLI command that fixes it.
func EnsureDNSToken(configDir, provider string) error {
	if _, err := os.Stat(DNSTokenPath(configDir, provider)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no token for dns provider %q at %s; set it with: easyshift dns set %s <file>",
				provider, DNSTokenPath(configDir, provider), provider)
		}
		return err
	}
	return nil
}
