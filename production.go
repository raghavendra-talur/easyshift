package easyshift

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// NewProductionDeps wires up real implementations of every Deps interface,
// rooted at cfg.ConfigDir. host is used to construct file-server URLs and
// should be the host IP that VMs can reach.
//
// Binary paths are no longer baked into the Installer or CSRApprover here —
// stages derive them per-call from the resolved cluster.OCPVersion. This
// lets a single Deps service multiple clusters on different OCP versions.
func NewProductionDeps(cfg *Config, host string) (Deps, error) {
	cmd := NewExecCommandRunner()
	dl := NewHTTPDownloader()

	httpRoot := filepath.Join(cfg.ConfigDir, "http")
	files, err := NewHTTPFileServer(httpRoot, host, cfg.WebPort)
	if err != nil {
		return Deps{}, fmt.Errorf("init file server: %w", err)
	}

	return Deps{
		Cmd:        cmd,
		Download:   dl,
		VM:         NewLibvirtVMManager(cmd),
		Net:        NewLibvirtNetworkProvisioner(cmd),
		Installer:  NewOpenShiftInstaller(cmd),
		Files:      files,
		CSR:        NewOCCSRApprover(cmd),
		Hostname:   NewSSHHostnameInjector(cmd),
		Host:       NewSystemHostInspector(),
		DNS:        NewDigDNSResolver(cmd),
		DNSManager: NewProductionDNSManager(cfg),
		NewCertIssuer: func(opts CertIssuerOpts) (CertIssuer, error) {
			return NewCertIssuer(opts)
		},
	}, nil
}

// NewProductionDNSManager returns a DNSManager backed by libdns when a
// token is available, otherwise a no-op manager (so the create pipeline
// can run untouched when the user opts out of DNS automation). The
// per-stage logic still checks ClusterConfig.DNSProvider before invoking
// any DNS calls; this is just the wiring layer.
func NewProductionDNSManager(cfg *Config) DNSManager {
	// Today we only support Cloudflare. When the user wires up more
	// providers, the choice will move to per-cluster config; for now the
	// presence of <configDir>/cloudflare-token implies cloudflare.
	if _, err := os.Stat(DNSTokenPath(cfg.ConfigDir, DNSProviderCloudflare)); err == nil {
		token, err := ReadDNSToken(cfg.ConfigDir, DNSProviderCloudflare)
		if err == nil {
			if m, err := NewDNSManager(DNSProviderCloudflare, token); err == nil {
				return m
			}
		}
	}
	return NopDNSManager{}
}

// NopDNSManager is the DNSManager used when no provider/token is
// configured. Its methods return nil; cluster lifecycle proceeds with the
// user managing records themselves (the preflight DNS check still runs).
type NopDNSManager struct{}

// Upsert is a no-op.
func (NopDNSManager) Upsert(_ context.Context, _, _, _ string) error { return nil }

// Delete is a no-op.
func (NopDNSManager) Delete(_ context.Context, _, _ string) error { return nil }
