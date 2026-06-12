// Package applytlscerts issues the cluster's api + *.apps serving certs and
// patches APIServer + IngressController to serve them. With TLSEmail set it
// uses Let's Encrypt (ACME DNS-01); otherwise it signs with the host-local
// easyshift CA so every cluster gets a cert chain the host can trust.
package applytlscerts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage issues and applies the cluster's TLS certificates.
type Stage struct {
	newCertIssuer  func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error)
	newLocalIssuer func(caDir string) (interfaces.CertIssuer, error)
	cmd            interfaces.CommandRunner
}

// New returns the apply-tls-certs stage. Both issuer params are factories:
// the ACME one because per-cluster settings (email, staging) aren't known
// until create, the local one to mirror that shape for wiring symmetry.
func New(
	newCertIssuer func(opts interfaces.CertIssuerOpts) (interfaces.CertIssuer, error),
	newLocalIssuer func(caDir string) (interfaces.CertIssuer, error),
	cmd interfaces.CommandRunner,
) *Stage {
	return &Stage{newCertIssuer: newCertIssuer, newLocalIssuer: newLocalIssuer, cmd: cmd}
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
	var issuer interfaces.CertIssuer
	if sc.Cluster.TLSEmail != "" {
		token, err := config.ReadDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
		if err != nil {
			return err
		}
		issuer, err = s.newCertIssuer(interfaces.CertIssuerOpts{
			Email:       sc.Cluster.TLSEmail,
			AccountDir:  config.ACMEAccountDir(sc.Config.ConfigDir, sc.Cluster.DNSProvider, sc.Cluster.TLSStaging),
			DNSProvider: sc.Cluster.DNSProvider,
			Token:       token,
			Staging:     sc.Cluster.TLSStaging,
		})
		if err != nil {
			return fmt.Errorf("cert issuer: %w", err)
		}
	} else {
		var err error
		issuer, err = s.newLocalIssuer(config.LocalCADir(sc.Config.ConfigDir))
		if err != nil {
			return fmt.Errorf("local cert issuer: %w", err)
		}
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

	// The admin kubeconfig's embedded internal CA no longer validates
	// api.<fqdn> once the named certificate is served. LE path: drop the CA
	// so the system trust store takes over. Local path: append our CA to the
	// bundle (keeping the internal CA valid during the apiserver rollout).
	// Both are best-effort: the cluster is up, so only warn on failure.
	if sc.Cluster.TLSEmail != "" {
		if err := s.makeKubeconfigPublic(ctx, oc, kubeconfig, sc.Cluster.Name); err != nil {
			logrus.Warnf("apply-tls-certs: could not make %s trust the public cert automatically "+
				"(use --insecure-skip-tls-verify, or `oc config unset clusters.%s.certificate-authority-data`): %v",
				kubeconfig, sc.Cluster.Name, err)
		}
	} else {
		caPath := config.LocalCACertPath(sc.Config.ConfigDir)
		if err := s.appendLocalCAToKubeconfig(ctx, oc, kubeconfig, sc.Cluster.Name, caPath); err != nil {
			// The merged user context (next stage) embeds this bundle, so on a
			// real cluster a failed append breaks `oc` once the apiserver
			// rollout completes — fail loudly; the stage is retry-safe. With
			// no kubeconfig on disk at all (fakes/--simulate) there is
			// nothing to fix up, so only warn.
			if _, statErr := os.Stat(kubeconfig); statErr == nil {
				return fmt.Errorf("add easyshift CA to admin kubeconfig: %w", err)
			}
			logrus.Warnf("apply-tls-certs: could not add the easyshift CA to %s "+
				"(oc may report certificate errors for api.%s): %v", kubeconfig, sc.Cluster.FQDN(), err)
		}
	}
	return nil
}

// makeKubeconfigPublic drops the embedded internal CA from the admin kubeconfig
// so `oc` validates api.<fqdn> against the system trust store (the Let's
// Encrypt cert chains to a public root) instead of failing with "certificate
// signed by unknown authority". The original — needed for the internal api-int
// endpoint and as break-glass — is preserved alongside as <kubeconfig>.internal-ca.
// Idempotent: once the CA is gone the file no longer matches, so resumes skip it.
func (s *Stage) makeKubeconfigPublic(ctx context.Context, oc, kubeconfig, clusterEntry string) error {
	data, err := os.ReadFile(kubeconfig)
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	if !bytes.Contains(data, []byte("certificate-authority-data")) {
		return nil // already public-trust
	}
	backup := kubeconfig + ".internal-ca"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return fmt.Errorf("back up kubeconfig: %w", err)
		}
	}
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"config", "unset", "clusters."+clusterEntry+".certificate-authority-data"); err != nil {
		return fmt.Errorf("strip internal CA from kubeconfig: %w", err)
	}
	logrus.Infof("rewrote %s to validate the Let's Encrypt cert via system trust "+
		"(internal-CA copy saved at %s)", kubeconfig, backup)
	return nil
}

// appendLocalCAToKubeconfig appends the easyshift local CA to the admin
// kubeconfig's certificate-authority-data so `oc` validates the local-CA
// serving cert on api.<fqdn>. The internal CA is kept in the bundle (the
// apiserver serves the old cert until its rollout completes). Idempotent:
// once our CA is in the bundle, resumes change nothing.
func (s *Stage) appendLocalCAToKubeconfig(ctx context.Context, oc, kubeconfig, clusterEntry, caCertPath string) error {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("read local CA cert: %w", err)
	}
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		return fmt.Errorf("no PEM block in %s", caCertPath)
	}

	out, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "view", "--raw",
		"-o", `jsonpath={.clusters[?(@.name=="`+clusterEntry+`")].cluster.certificate-authority-data}`)
	if err != nil {
		return fmt.Errorf("read kubeconfig CA bundle: %w", err)
	}
	b64bundle := strings.TrimSpace(string(out))
	if b64bundle == "" {
		return fmt.Errorf("no certificate-authority-data found for cluster entry %q in %s", clusterEntry, kubeconfig)
	}
	bundle, err := base64.StdEncoding.DecodeString(b64bundle)
	if err != nil {
		return fmt.Errorf("decode kubeconfig CA bundle: %w", err)
	}
	for rest := bundle; ; {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		if bytes.Equal(block.Bytes, caBlock.Bytes) {
			return nil // already trusted
		}
		rest = r
	}

	orig, err := os.ReadFile(kubeconfig)
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	backup := kubeconfig + ".internal-ca"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(backup, orig, 0o600); err != nil {
			return fmt.Errorf("back up kubeconfig: %w", err)
		}
	}

	if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	newBundle := append(append([]byte{}, bundle...), pem.EncodeToMemory(caBlock)...)
	if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "set",
		"clusters."+clusterEntry+".certificate-authority-data",
		base64.StdEncoding.EncodeToString(newBundle)); err != nil {
		return fmt.Errorf("append CA to kubeconfig: %w", err)
	}
	logrus.Infof("added the easyshift local CA to %s (original saved at %s)", kubeconfig, backup)
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
