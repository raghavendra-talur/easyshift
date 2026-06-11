package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
	"github.com/TheEasyShift/easyshift/providers/csr"
	"github.com/TheEasyShift/easyshift/providers/dns"
	"github.com/TheEasyShift/easyshift/providers/exec"
	"github.com/TheEasyShift/easyshift/providers/fileserver"
	"github.com/TheEasyShift/easyshift/providers/host"
	"github.com/TheEasyShift/easyshift/providers/libvirt"
	"github.com/TheEasyShift/easyshift/providers/openshift"
	"github.com/TheEasyShift/easyshift/providers/tls"
)

// NewProductionDeps wires real implementations of every dependency, rooted
// at cfg.ConfigDir. host is the host IP that VMs can reach (for file-server
// URLs). This is the single place that knows the concrete provider types.
func NewProductionDeps(cfg *config.Config, hostIP string) (interfaces.Deps, error) {
	cmd := exec.NewExecCommandRunner()
	dl := exec.NewHTTPDownloader()

	httpRoot := filepath.Join(cfg.ConfigDir, "http")
	files, err := fileserver.NewHTTPFileServer(httpRoot, hostIP, cfg.WebPort)
	if err != nil {
		return interfaces.Deps{}, fmt.Errorf("init file server: %w", err)
	}

	return interfaces.Deps{
		Cmd:        cmd,
		Download:   dl,
		VM:         libvirt.NewLibvirtVMManager(cmd),
		Net:        libvirt.NewLibvirtNetworkProvisioner(cmd),
		Installer:  openshift.NewOpenShiftInstaller(cmd),
		Files:      files,
		CSR:        csr.NewOCCSRApprover(cmd),
		Hostname:   host.NewSSHHostnameInjector(cmd),
		Host:       host.NewSystemHostInspector(),
		DNS:        dns.NewDigDNSResolver(cmd),
		DNSManager: newProductionDNSManager(cfg),
		NewCertIssuer: func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error) {
			return tls.NewIssuer(opts)
		},
	}, nil
}

// newProductionDNSManager returns a libdns-backed DNSManager when a token is
// present, else a no-op (so the pipeline runs untouched when the user opts
// out of DNS automation; the per-stage DNSProvider check still gates calls).
func newProductionDNSManager(cfg *config.Config) interfaces.DNSManager {
	if _, err := os.Stat(config.DNSTokenPath(cfg.ConfigDir, config.DNSProviderCloudflare)); err == nil {
		if token, err := config.ReadDNSToken(cfg.ConfigDir, config.DNSProviderCloudflare); err == nil {
			if m, err := dns.NewManager(config.DNSProviderCloudflare, token); err == nil {
				return m
			}
		}
	}
	return nopDNSManager{}
}

// nopDNSManager is used when no DNS provider/token is configured.
type nopDNSManager struct{}

func (nopDNSManager) Upsert(_ context.Context, _, _, _ string) error { return nil }
func (nopDNSManager) Delete(_ context.Context, _, _ string) error    { return nil }
