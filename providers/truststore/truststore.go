// Package truststore installs the easyshift local CA into the host's trust
// stores: the system store (sudo: update-ca-trust on Fedora/RHEL,
// update-ca-certificates on Debian-family, `security` on macOS) and — when
// certutil is available — the NSS databases Chrome and Firefox actually
// read on Linux. All execution goes through CommandRunner so --simulate
// traces it and tests assert exact invocations; sudo prompts on /dev/tty,
// so captured output does not break password entry.
package truststore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/interfaces"
)

const (
	anchorName = "easyshift-local-ca.crt"
	// nssNickname is the label used for NSS database entries and the macOS
	// keychain CN match. Must equal providers/localca's caCommonName: darwin
	// uninstall matches the keychain entry by CN.
	nssNickname = "easyshift local CA"

	fedoraAnchors = "/etc/pki/ca-trust/source/anchors"
	debianAnchors = "/usr/local/share/ca-certificates"
)

// Installer is the real interfaces.TrustStoreInstaller. The probe fields are
// injectable so tests pin the platform.
type Installer struct {
	cmd        interfaces.CommandRunner
	goos       string
	home       string
	pathExists func(string) bool
	lookPath   func(string) (string, error)
}

// New returns an Installer for the current host.
func New(cmd interfaces.CommandRunner) *Installer {
	home, _ := os.UserHomeDir()
	return &Installer{
		cmd:  cmd,
		goos: runtime.GOOS,
		home: home,
		pathExists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		lookPath: exec.LookPath,
	}
}

// Install adds the CA to the system store (fatal on failure) and to any NSS
// databases found (best-effort).
func (t *Installer) Install(ctx context.Context, caCertPath string) error {
	if err := t.installSystem(ctx, caCertPath); err != nil {
		return err
	}
	t.eachNSSDB(ctx, func(dir string) error {
		_, err := t.cmd.Run(ctx, "certutil", "-A", "-d", "sql:"+dir, "-t", "C,,", "-n", nssNickname, "-i", caCertPath)
		return err
	})
	return nil
}

// Uninstall reverses Install, tolerating absence at every step.
func (t *Installer) Uninstall(ctx context.Context, caCertPath string) error {
	switch t.goos {
	case "darwin":
		// remove-trusted-cert undoes the admin trust settings that
		// add-trusted-cert -d wrote; delete-certificate then removes the cert
		// itself. "not found" outputs are tolerated so uninstall stays
		// idempotent; anything else (e.g. sudo denied) propagates.
		if out, err := t.cmd.Run(ctx, "sudo", "security", "remove-trusted-cert", "-d", caCertPath); err != nil {
			if !strings.Contains(string(out), "could not be found") {
				return fmt.Errorf("remove trust settings: %w", err)
			}
		}
		if out, err := t.cmd.Run(ctx, "sudo", "security", "delete-certificate", "-c", nssNickname,
			"/Library/Keychains/System.keychain"); err != nil {
			if !strings.Contains(string(out), "Unable to delete certificate") {
				logrus.Warnf("remove CA from system keychain: %v", err)
			}
		}
	default:
		switch {
		case t.pathExists(fedoraAnchors):
			if _, err := t.cmd.Run(ctx, "sudo", "rm", "-f", filepath.Join(fedoraAnchors, anchorName)); err != nil {
				return err
			}
			if _, err := t.cmd.Run(ctx, "sudo", "update-ca-trust", "extract"); err != nil {
				return err
			}
		case t.pathExists(debianAnchors):
			if _, err := t.cmd.Run(ctx, "sudo", "rm", "-f", filepath.Join(debianAnchors, anchorName)); err != nil {
				return err
			}
			if _, err := t.cmd.Run(ctx, "sudo", "update-ca-certificates"); err != nil {
				return err
			}
		}
	}
	t.eachNSSDB(ctx, func(dir string) error {
		_, err := t.cmd.Run(ctx, "certutil", "-D", "-d", "sql:"+dir, "-n", nssNickname)
		return err
	})
	return nil
}

func (t *Installer) installSystem(ctx context.Context, caCertPath string) error {
	if t.goos == "darwin" {
		_, err := t.cmd.Run(ctx, "sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain", caCertPath)
		return err
	}
	switch {
	case t.pathExists(fedoraAnchors):
		if _, err := t.cmd.Run(ctx, "sudo", "cp", caCertPath, filepath.Join(fedoraAnchors, anchorName)); err != nil {
			return err
		}
		_, err := t.cmd.Run(ctx, "sudo", "update-ca-trust", "extract")
		return err
	case t.pathExists(debianAnchors):
		if _, err := t.cmd.Run(ctx, "sudo", "cp", caCertPath, filepath.Join(debianAnchors, anchorName)); err != nil {
			return err
		}
		_, err := t.cmd.Run(ctx, "sudo", "update-ca-certificates")
		return err
	default:
		return fmt.Errorf("no known system trust store found (looked for %s and %s)", fedoraAnchors, debianAnchors)
	}
}

// eachNSSDB runs fn for every NSS database dir on the host (no sudo: the
// databases are user-owned). Missing certutil downgrades to an info message.
func (t *Installer) eachNSSDB(ctx context.Context, fn func(dir string) error) {
	if _, err := t.lookPath("certutil"); err != nil {
		logrus.Info("certutil not found; browsers may still warn about the cluster console. " +
			"Install nss-tools (Fedora), libnss3-tools (Debian/Ubuntu), or `brew install nss` (macOS), " +
			"then re-run `easyshift trust`.")
		return
	}
	if t.home == "" {
		logrus.Debug("home directory unknown; skipping NSS database update")
		return
	}
	var dirs []string
	if d := filepath.Join(t.home, ".pki", "nssdb"); t.pathExists(d) {
		dirs = append(dirs, d)
	}
	for _, glob := range []string{
		filepath.Join(t.home, ".mozilla", "firefox", "*"),
		// Ubuntu ships Firefox as a snap since 22.04; flatpak is common elsewhere.
		filepath.Join(t.home, "snap", "firefox", "common", ".mozilla", "firefox", "*"),
		filepath.Join(t.home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox", "*"),
		filepath.Join(t.home, "Library", "Application Support", "Firefox", "Profiles", "*"),
	} {
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			if t.pathExists(filepath.Join(m, "cert9.db")) {
				dirs = append(dirs, m)
			}
		}
	}
	for _, dir := range dirs {
		if err := fn(dir); err != nil {
			logrus.Warnf("certutil in %s: %v", dir, err)
		}
	}
}
