package easyshift

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	cfdns "github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

// CertIssuerOpts configures NewCertIssuer. All fields except DNSProvider and
// Token are optional; sensible Let's Encrypt defaults apply.
type CertIssuerOpts struct {
	// Email is the Let's Encrypt account email. Required.
	Email string
	// AccountDir is where the ACME account key + registration are stored
	// across runs (so we don't re-register on every cluster create).
	// Required.
	AccountDir string
	// DNSProvider names the DNS backend for the DNS-01 challenge. The same
	// provider names as NewDNSManager are accepted (cloudflare today).
	DNSProvider string
	// Token is the DNS provider's API token. Required.
	Token string
	// Staging selects Let's Encrypt's staging endpoint when true. Untrusted
	// roots, but no real-quota counted — use for development iterations.
	Staging bool
}

// LegoCertIssuer is the production CertIssuer. It uses go-acme/lego v4 with
// a DNS-01 challenge against the configured provider.
type LegoCertIssuer struct {
	client *lego.Client
}

// NewCertIssuer constructs a CertIssuer ready to call Issue. Registers the
// ACME account if no on-disk registration exists at opts.AccountDir, or
// re-uses the existing one. The DNS challenge provider is configured from
// opts.Token.
func NewCertIssuer(opts CertIssuerOpts) (*LegoCertIssuer, error) {
	if opts.Email == "" {
		return nil, errors.New("cert issuer: email required")
	}
	if opts.AccountDir == "" {
		return nil, errors.New("cert issuer: account dir required")
	}
	if opts.Token == "" {
		return nil, errors.New("cert issuer: dns token required")
	}

	user, err := loadOrCreateACMEUser(opts.AccountDir, opts.Email)
	if err != nil {
		return nil, fmt.Errorf("acme user: %w", err)
	}

	cfg := lego.NewConfig(user)
	if opts.Staging {
		cfg.CADirURL = lego.LEDirectoryStaging
	}
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("lego client: %w", err)
	}

	dnsProvider, err := newACMEDNSProvider(opts.DNSProvider, opts.Token)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetDNS01Provider(dnsProvider); err != nil {
		return nil, fmt.Errorf("set dns-01 provider: %w", err)
	}

	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("acme register: %w", err)
		}
		user.Registration = reg
		if err := saveACMEUser(opts.AccountDir, user); err != nil {
			return nil, fmt.Errorf("save acme user: %w", err)
		}
	}

	return &LegoCertIssuer{client: client}, nil
}

// Issue requests a cert for domains via DNS-01. The first domain becomes the
// CN; the rest are SANs. Returned PEM is the leaf + intermediates bundled.
func (l *LegoCertIssuer) Issue(_ context.Context, domains []string) (certPEM, keyPEM []byte, err error) {
	if len(domains) == 0 {
		return nil, nil, errors.New("issue: at least one domain required")
	}
	res, err := l.client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("obtain cert for %v: %w", domains, err)
	}
	return res.Certificate, res.PrivateKey, nil
}

// newACMEDNSProvider routes to the right lego DNS provider for the given
// provider name. New providers slot in with one extra case arm.
func newACMEDNSProvider(provider, token string) (*cfdns.DNSProvider, error) {
	switch provider {
	case DNSProviderCloudflare:
		cfg := cfdns.NewDefaultConfig()
		cfg.AuthToken = token
		p, err := cfdns.NewDNSProviderConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("cloudflare dns-01 provider: %w", err)
		}
		return p, nil
	}
	return nil, fmt.Errorf("unsupported tls dns provider %q (supported: %s)",
		provider, DNSProviderCloudflare)
}

// --- ACME user (account) persistence -----------------------------------

// acmeUser is the on-disk representation of a Let's Encrypt account. lego
// requires email + registration + private key on each client; we cache
// these so re-runs and renewals share the same account (otherwise every
// run consumes one of LE's "10 accounts per IP per 3 hours" budget).
type acmeUser struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration,omitempty"`
	key          *ecdsa.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func loadOrCreateACMEUser(dir, email string) (*acmeUser, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "account.key")
	jsonPath := filepath.Join(dir, "account.json")

	keyBytes, err := os.ReadFile(keyPath)
	switch {
	case err == nil:
		blk, _ := pem.Decode(keyBytes)
		if blk == nil {
			return nil, errors.New("acme account.key: invalid PEM")
		}
		key, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse acme account key: %w", err)
		}
		u := &acmeUser{Email: email, key: key}
		if data, err := os.ReadFile(jsonPath); err == nil {
			if err := json.Unmarshal(data, u); err != nil {
				return nil, fmt.Errorf("parse acme account.json: %w", err)
			}
		}
		// Email may have been changed by the caller; trust the new value but
		// keep the cached registration (lego won't re-register if it's set).
		u.Email = email
		return u, nil
	case os.IsNotExist(err):
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate acme key: %w", err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
			return nil, fmt.Errorf("write acme account.key: %w", err)
		}
		return &acmeUser{Email: email, key: key}, nil
	default:
		return nil, err
	}
}

func saveACMEUser(dir string, u *acmeUser) error {
	data, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "account.json"), data, 0o600)
}

// ACMEAccountDir returns the on-disk location for an ACME account,
// namespaced by provider AND staging/production. The endpoint distinction
// matters because Let's Encrypt's staging and production directories are
// separate services — an account registered on one is unknown to the
// other, and reusing a cached registration across endpoints breaks the
// next Obtain call.
func ACMEAccountDir(configDir, provider string, staging bool) string {
	env := "prod"
	if staging {
		env = "staging"
	}
	return filepath.Join(configDir, "acme", provider, env)
}
