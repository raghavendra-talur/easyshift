package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/app"
	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

// runTrust generates the local CA if needed and installs (or removes) it in
// the host trust stores. Separated from cobra wiring for testability, like
// the pull-secret flow.
func runTrust(ctx context.Context, cfg *config.Config, ts interfaces.TrustStoreInstaller, uninstall bool, out io.Writer) error {
	caPath, err := app.EnsureLocalCA(cfg)
	if err != nil {
		return err
	}
	marker := config.LocalCATrustedMarkerPath(cfg.ConfigDir)

	if uninstall {
		if err := ts.Uninstall(ctx, caPath); err != nil {
			return err
		}
		_ = os.Remove(marker)
		fmt.Fprintln(out, "easyshift local CA removed from the host trust stores")
		return nil
	}

	if err := ts.Install(ctx, caPath); err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte("trusted\n"), 0o600); err != nil {
		logrus.Warnf("write trust marker: %v", err)
	}
	fmt.Fprintf(out, "easyshift local CA (%s) installed into the host trust stores\n", caPath)
	fmt.Fprintln(out, "Browsers now trust the console of every easyshift cluster on this host.")
	return nil
}
