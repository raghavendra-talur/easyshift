package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/TheEasyShift/easyshift/config"
)

// printCreateSummary tells the user how to reach the cluster Create just
// finished: the kubeconfig context that is now current, the console URL,
// where the kubeadmin password lives (path only — never the secret), and a
// one-time `easyshift trust` hint for local-CA clusters.
func printCreateSummary(w io.Writer, cfg *config.Config, c *config.ClusterConfig) {
	fqdn := c.FQDN()
	fmt.Fprintf(w, "\nCluster %s is ready.\n", c.Name)
	fmt.Fprintf(w, "  kubectl/oc: context %q is merged into your kubeconfig and set as current\n", c.Name)
	fmt.Fprintf(w, "  console:    https://console-openshift-console.apps.%s\n", fqdn)
	fmt.Fprintf(w, "  kubeadmin:  password file %s\n",
		filepath.Join(config.ClusterDir(cfg.ConfigDir, c.Name), "auth", "kubeadmin-password"))
	if c.TLSEmail == "" {
		if _, err := os.Stat(config.LocalCATrustedMarkerPath(cfg.ConfigDir)); err != nil {
			fmt.Fprintf(w, "  tip:        run `easyshift trust` once to remove browser TLS warnings (uses sudo)\n")
		}
	}
}
