// Package applytlscerts issues Let's Encrypt certs (api + *.apps) via ACME
// DNS-01, plants them as TLS secrets, and patches APIServer +
// IngressController to serve them. No-op when TLSEmail is unset.
package applytlscerts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// Stage issues and applies the cluster's TLS certificates.
type Stage struct {
	newCertIssuer func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error)
	cmd           interfaces.CommandRunner
}

// New returns the apply-tls-certs stage. newCertIssuer is a factory because
// the per-cluster ACME settings (email, staging) aren't known until create.
func New(newCertIssuer func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error), cmd interfaces.CommandRunner) *Stage {
	return &Stage{newCertIssuer: newCertIssuer, cmd: cmd}
}

func (*Stage) Name() string { return "apply-tls-certs" }

// Preflight requires a DNS provider (reused for the DNS-01 challenge) when
// TLS is enabled, and fails fast before consuming LE rate-limit budget.
func (*Stage) Preflight(_ context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.TLSEmail == "" {
		return nil
	}
	if sc.Cluster.DNSProvider == "" {
		return fmt.Errorf("TLS issuance requires --dns-provider (used for the ACME DNS-01 challenge)")
	}
	return config.EnsureDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
}

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	if sc.Cluster.TLSEmail == "" {
		return nil
	}
	token, err := config.ReadDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
	if err != nil {
		return err
	}
	issuer, err := s.newCertIssuer(interfaces.CertIssuerOpts{
		Email:       sc.Cluster.TLSEmail,
		AccountDir:  config.ACMEAccountDir(sc.Config.ConfigDir, sc.Cluster.DNSProvider, sc.Cluster.TLSStaging),
		DNSProvider: sc.Cluster.DNSProvider,
		Token:       token,
		Staging:     sc.Cluster.TLSStaging,
	})
	if err != nil {
		return fmt.Errorf("cert issuer: %w", err)
	}

	tlsDir := filepath.Join(sc.ClusterDir(), "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	fqdn := sc.Cluster.FQDN()
	apiCert := filepath.Join(tlsDir, "api.crt")
	apiKey := filepath.Join(tlsDir, "api.key")
	if err := issueToFiles(ctx, issuer, []string{"api." + fqdn}, apiCert, apiKey); err != nil {
		return fmt.Errorf("issue api cert: %w", err)
	}
	appsCert := filepath.Join(tlsDir, "apps.crt")
	appsKey := filepath.Join(tlsDir, "apps.key")
	if err := issueToFiles(ctx, issuer, []string{"*.apps." + fqdn}, appsCert, appsKey); err != nil {
		return fmt.Errorf("issue apps cert: %w", err)
	}

	oc := sc.OCBinaryPath()
	kubeconfig := sc.KubeconfigPath()
	apiSecret := sc.Cluster.Name + "-api-cert"
	appsSecret := sc.Cluster.Name + "-apps-cert"

	if err := s.applyTLSSecret(ctx, oc, kubeconfig, "openshift-config", apiSecret, apiCert, apiKey); err != nil {
		return err
	}
	if err := s.applyTLSSecret(ctx, oc, kubeconfig, "openshift-ingress", appsSecret, appsCert, appsKey); err != nil {
		return err
	}

	apiPatch := fmt.Sprintf(
		`{"spec":{"servingCerts":{"namedCertificates":[{"names":["api.%s"],"servingCertificate":{"name":"%s"}}]}}}`,
		fqdn, apiSecret)
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"patch", "apiserver/cluster", "--type=merge", "-p", apiPatch); err != nil {
		return fmt.Errorf("patch apiserver/cluster: %w", err)
	}

	ingressPatch := fmt.Sprintf(`{"spec":{"defaultCertificate":{"name":"%s"}}}`, appsSecret)
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"-n", "openshift-ingress-operator", "patch", "ingresscontroller/default",
		"--type=merge", "-p", ingressPatch); err != nil {
		return fmt.Errorf("patch ingresscontroller/default: %w", err)
	}
	return nil
}

// Rollback is a no-op: deleting the cluster tears down the API/ingress
// objects anyway, and the cert files go with the cluster dir.
func (*Stage) Rollback(_ context.Context, _ *interfaces.StageContext) error { return nil }

func issueToFiles(ctx context.Context, issuer interfaces.CertIssuer, domains []string, certPath, keyPath string) error {
	cert, key, err := issuer.Issue(ctx, domains)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, cert, 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyPath, key, 0o600)
}

// applyTLSSecret creates-or-updates a kubernetes.io/tls Secret by piping
// `oc apply` against dry-run YAML (idempotent across re-runs).
func (s *Stage) applyTLSSecret(ctx context.Context, oc, kubeconfig, namespace, name, certPath, keyPath string) error {
	out, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"-n", namespace,
		"create", "secret", "tls", name,
		"--cert="+certPath, "--key="+keyPath,
		"--dry-run=client", "-o", "yaml")
	if err != nil {
		return fmt.Errorf("render tls secret %s/%s: %w", namespace, name, err)
	}
	tmp := filepath.Join(filepath.Dir(certPath), name+".secret.yaml")
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write rendered secret %s: %w", tmp, err)
	}
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "apply", "-f", tmp); err != nil {
		return fmt.Errorf("apply tls secret %s/%s: %w", namespace, name, err)
	}
	return nil
}
