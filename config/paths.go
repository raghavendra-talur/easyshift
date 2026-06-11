package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DNSProviderCloudflare is the provider name passed on the create command
// and stored on each cluster so teardown picks the right backend.
const DNSProviderCloudflare = "cloudflare"

// ClusterDir is the working directory for a cluster's openshift-install
// artifacts (install-config, ignition, auth/kubeconfig, staged ISO, certs).
func ClusterDir(configDir, name string) string {
	return filepath.Join(configDir, "clusters", name)
}

// BinariesDir returns the per-version cache root for openshift-install /
// oc / coreos-installer. The same binaries get reused across clusters.
func BinariesDir(configDir, version string) string {
	return filepath.Join(configDir, "bin", version)
}

// RHCOSCacheDir returns the per-version cache root for RHCOS artifacts.
// Files here are shared across clusters of the same version.
func RHCOSCacheDir(configDir, version string) string {
	return filepath.Join(configDir, "rhcos", version)
}

// ClusterDNSNames returns the DNS names a bridge-mode cluster needs, all of
// which must resolve to the master IP. The wildcard *.apps is probed via a
// synthetic console hostname because a literal "*" lookup isn't valid DNS.
func ClusterDNSNames(fqdn string) []string {
	return []string{
		"api." + fqdn,
		"api-int." + fqdn,
		"console-openshift-console.apps." + fqdn,
	}
}

// --- DNS provider token storage (mirrors the pull-secret pattern) -------

// DNSTokenPath returns the on-disk location of the token file for provider.
func DNSTokenPath(configDir, provider string) string {
	return filepath.Join(configDir, provider+"-token")
}

// WriteDNSToken persists a provider token at DNSTokenPath with mode 0600.
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

// EnsureDNSToken returns a helpful error if the token file is missing.
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

// ACMEAccountDir is the on-disk home for an ACME account, namespaced by
// provider AND staging/production. The endpoint distinction matters because
// Let's Encrypt's staging and production directories are separate services —
// an account registered on one is unknown to the other.
func ACMEAccountDir(configDir, provider string, staging bool) string {
	env := "prod"
	if staging {
		env = "staging"
	}
	return filepath.Join(configDir, "acme", provider, env)
}

// ValidatePullSecretJSON parses the persisted pull secret and verifies it is
// JSON with the required "auths" key. Run as a preflight so a malformed
// secret fails fast instead of mid-install.
func ValidatePullSecretJSON(configDir string) error {
	data, err := os.ReadFile(PullSecretPath(configDir))
	if err != nil {
		return fmt.Errorf("read pull secret: %w", err)
	}
	return ValidatePullSecretBytes(data)
}

// ValidatePullSecretBytes verifies data is JSON with the required "auths"
// key. Used to vet a fetched secret before it is written to disk.
func ValidatePullSecretBytes(data []byte) error {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("pull secret is not valid JSON: %w", err)
	}
	if _, ok := parsed["auths"]; !ok {
		return fmt.Errorf("pull secret is missing required 'auths' key (download a fresh secret from console.redhat.com)")
	}
	return nil
}
