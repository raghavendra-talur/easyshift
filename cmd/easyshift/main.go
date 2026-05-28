package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/raghavendra-talur/easyshift"
	"github.com/raghavendra-talur/easyshift/fakes"
	"github.com/spf13/cobra"
)

// annotationNeedsFileServer marks subcommands that need the HTTP file server
// running. Only commands that may have VMs fetch ignition or images set this;
// read-only commands like list/status skip it so they don't race the create
// command for port 9393.
const annotationNeedsFileServer = "easyshift/needs-file-server"

func main() {
	// easyshift does not require running as root. Phase 1 (bridge mode) needs
	// only libvirt-group membership; the libvirt-reachability preflight on
	// stages that touch qemu:///system will surface the right error if the
	// user is missing permissions. Phase 2 (NAT mode) will write under /etc/
	// and run privileged commands via sudo from the relevant stages.

	var (
		debug    bool
		simulate bool
	)

	configDir := easyshift.DefaultConfigDir()
	cfg, err := easyshift.LoadConfig(configDir)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	var mgr *easyshift.ClusterManager
	var deps easyshift.Deps
	var simBundle *fakes.Bundle // set only when --simulate

	rootCmd := &cobra.Command{
		Use:   "easyshift",
		Short: "easyshift - Easy OpenShift Cluster Management",
		Long: `easyshift is a tool for creating and managing OpenShift clusters.
It provides a simple interface for cluster lifecycle management.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if simulate {
				// Swap in a throwaway config dir so we never touch real state
				// (config.json, ~/.config/easyshift/clusters/*, ACME accounts, etc.).
				simCfg, simDeps, bundle, err := newSimulationEnv(cfg)
				if err != nil {
					return fmt.Errorf("simulate: %w", err)
				}
				cfg, deps, simBundle = simCfg, simDeps, bundle
				if err := easyshift.InitLogging(cfg.LogFile, debug); err != nil {
					return err
				}
				mgr = easyshift.NewClusterManager(cfg, deps)
				return nil
			}
			if err := easyshift.InitLogging(cfg.LogFile, debug); err != nil {
				return err
			}
			host, err := primaryHostIP()
			if err != nil {
				return fmt.Errorf("detect host IP: %w", err)
			}
			deps, err = easyshift.NewProductionDeps(cfg, host)
			if err != nil {
				return err
			}
			if cmd.Annotations[annotationNeedsFileServer] == "true" {
				if err := deps.Files.Start(context.Background()); err != nil {
					return fmt.Errorf("start file server: %w", err)
				}
			}
			mgr = easyshift.NewClusterManager(cfg, deps)
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			if simulate && simBundle != nil {
				fmt.Fprintln(os.Stdout)
				simBundle.WriteTrace(os.Stdout)
				return nil
			}
			if cmd.Annotations[annotationNeedsFileServer] == "true" && deps.Files != nil {
				_ = deps.Files.Stop(context.Background())
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&simulate, "simulate", "S", false,
		"Run against in-memory fakes instead of real libvirt/Cloudflare/Let's Encrypt. "+
			"Uses a throwaway config dir; prints a trace of every operation the real run would have performed.")

	rootCmd.AddCommand(
		newCreateCommand(&mgr),
		newStartCommand(&mgr),
		newStopCommand(&mgr),
		newDeleteCommand(&mgr),
		newListCommand(&mgr),
		newStatusCommand(&mgr),
		newPullSecretCommand(cfg),
		newDNSCommand(cfg),
	)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func newCreateCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	var (
		name        string
		baseDomain  string
		ocpVersion  string
		masterCount int
		workerCount int
		masterRAM   int
		workerRAM   int
		networkMode string
		bridge      string
		masterMAC   string
		masterIP    string
		machineCIDR string
		storagePool string
		dnsProvider string
		dnsZone     string
		tlsEmail    string
		tlsStaging  bool
	)

	cmd := &cobra.Command{
		Use:         "create",
		Short:       "Create a new OpenShift cluster",
		Annotations: map[string]string{annotationNeedsFileServer: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return (*mgr).Create(context.Background(), &easyshift.ClusterConfig{
				Name:        name,
				Domain:      baseDomain,
				OCPVersion:  ocpVersion,
				MasterCount: masterCount,
				WorkerCount: workerCount,
				MasterRAM:   masterRAM,
				WorkerRAM:   workerRAM,
				NetworkMode: networkMode,
				Bridge:      bridge,
				MasterMAC:   masterMAC,
				MasterIP:    masterIP,
				MachineCIDR: machineCIDR,
				StoragePool: storagePool,
				DNSProvider: dnsProvider,
				DNSZone:     dnsZone,
				TLSEmail:    tlsEmail,
				TLSStaging:  tlsStaging,
			})
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Cluster name")
	cmd.Flags().StringVarP(&baseDomain, "base-domain", "D", "local",
		"OpenShift baseDomain; the cluster's API and ingress live under <name>.<base-domain> "+
			"(e.g. api.<name>.<base-domain>). In bridge mode you must create matching DNS records.")
	cmd.Flags().StringVarP(&ocpVersion, "version", "v", easyshift.DefaultOCPVersion, "OpenShift version")
	cmd.Flags().IntVarP(&masterCount, "masters", "m", 1, "Number of master nodes (must be 1)")
	cmd.Flags().IntVarP(&workerCount, "workers", "w", 0, "Number of worker nodes (Phase 1: must be 0; add later via addnode)")
	cmd.Flags().IntVar(&masterRAM, "master-ram", 32768, "Master node RAM in MB")
	cmd.Flags().IntVar(&workerRAM, "worker-ram", 16384, "Worker node RAM in MB")
	cmd.Flags().StringVar(&networkMode, "network-mode", easyshift.NetworkModeNAT,
		"Network mode: 'nat' (libvirt NAT + HAProxy on host) or 'bridge' (attach to a host Linux bridge on the LAN)")
	cmd.Flags().StringVar(&bridge, "bridge", "",
		"Name of an existing host Linux bridge (e.g. br0); required when --network-mode=bridge")
	cmd.Flags().StringVar(&masterMAC, "master-mac", "",
		"MAC address you reserved at the router for the master VM; required in bridge mode")
	cmd.Flags().StringVar(&masterIP, "master-ip", "",
		"IP the router will hand to --master-mac; required in bridge mode")
	cmd.Flags().StringVar(&machineCIDR, "machine-cidr", "",
		"Override the LAN CIDR for networking.machineNetwork in install-config; defaults to /24 of --master-ip")
	cmd.Flags().StringVar(&storagePool, "storage-pool", easyshift.DefaultStoragePool,
		"libvirt storage pool for the master disk and boot ISO (run `virsh pool-list --all` to see yours)")
	cmd.Flags().StringVar(&dnsProvider, "dns-provider", "",
		"Public DNS provider to use for api/api-int/*.apps records (currently: 'cloudflare'). "+
			"Empty disables automation: you create the records yourself. Token must be set first via `easyshift dns set <provider>`.")
	cmd.Flags().StringVar(&dnsZone, "dns-zone", "",
		"Parent DNS zone (defaults to --base-domain). Override when the zone is a parent of --base-domain.")
	cmd.Flags().StringVar(&tlsEmail, "tls-email", "",
		"ACME account email; non-empty enables Let's Encrypt cert issuance for api.<fqdn> and *.apps.<fqdn> "+
			"via DNS-01. Requires --dns-provider (the same token is reused for the challenge).")
	cmd.Flags().BoolVar(&tlsStaging, "tls-staging", false,
		"Use Let's Encrypt's staging endpoint (untrusted certs, but no production rate limits). "+
			"Recommended while iterating; flip to false for the final run.")

	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newStartCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "start [cluster-name]",
		Short: "Start a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return (*mgr).Start(context.Background(), args[0])
		},
	}
}

func newStopCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "stop [cluster-name]",
		Short: "Stop a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return (*mgr).Stop(context.Background(), args[0])
		},
	}
}

func newDeleteCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "delete [cluster-name]",
		Short: "Delete a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return (*mgr).Delete(context.Background(), args[0])
		},
	}
}

func newListCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters := (*mgr).List()
			if len(clusters) == 0 {
				fmt.Println("No clusters found")
				return nil
			}
			fmt.Println("Clusters:")
			for _, c := range clusters {
				fmt.Printf("- %s.%s  state=%s  version=%s  nodes=%dm/%dw\n",
					c.Name, c.Domain, c.State, c.OCPVersion, c.MasterCount, c.WorkerCount)
			}
			return nil
		},
	}
}

func newStatusCommand(mgr **easyshift.ClusterManager) *cobra.Command {
	return &cobra.Command{
		Use:   "status <cluster-name>",
		Short: "Show diagnostic info for a cluster (VM, DNS, API reachability)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			rep, err := (*mgr).Status(context.Background(), args[0])
			if err != nil {
				return err
			}
			rep.Print(os.Stdout)
			return nil
		},
	}
}

func newPullSecretCommand(cfg *easyshift.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull-secret",
		Short: "Manage the OpenShift pull secret used for cluster installs",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <file>",
		Short: "Store the pull secret from the given file (or '-' for stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var data []byte
			var err error
			if args[0] == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				return fmt.Errorf("read pull secret source: %w", err)
			}
			if err := easyshift.WritePullSecret(cfg.ConfigDir, data); err != nil {
				return err
			}
			fmt.Printf("pull secret stored at %s\n", easyshift.PullSecretPath(cfg.ConfigDir))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the path of the persisted pull secret",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := easyshift.EnsurePullSecret(cfg.ConfigDir); err != nil {
				return err
			}
			fmt.Println(easyshift.PullSecretPath(cfg.ConfigDir))
			return nil
		},
	})
	return cmd
}

func newDNSCommand(cfg *easyshift.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Manage credentials for the public DNS provider used to create cluster records",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <provider> <file>",
		Short: "Store an API token for the given provider (use '-' for stdin). Currently supported: cloudflare",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			provider, src := args[0], args[1]
			var data []byte
			var err error
			if src == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(src)
			}
			if err != nil {
				return fmt.Errorf("read token source: %w", err)
			}
			if err := easyshift.WriteDNSToken(cfg.ConfigDir, provider, data); err != nil {
				return err
			}
			fmt.Printf("%s token stored at %s\n", provider, easyshift.DNSTokenPath(cfg.ConfigDir, provider))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <provider>",
		Short: "Print the path of the persisted token for the given provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := easyshift.EnsureDNSToken(cfg.ConfigDir, args[0]); err != nil {
				return err
			}
			fmt.Println(easyshift.DNSTokenPath(cfg.ConfigDir, args[0]))
			return nil
		},
	})
	return cmd
}

// newSimulationEnv returns a Config rooted at a throwaway temp dir and a Deps
// wired with fake implementations. The real cfg is read only for defaults
// (WebPort) — config.json on disk and persistent state are not touched.
// A fake pull secret and DNS token are pre-planted so the create pipeline
// makes it past the preflight checks that would otherwise require real
// credentials.
func newSimulationEnv(real *easyshift.Config) (*easyshift.Config, easyshift.Deps, *fakes.Bundle, error) {
	tmp, err := os.MkdirTemp("", "easyshift-sim-*")
	if err != nil {
		return nil, easyshift.Deps{}, nil, err
	}
	cfg := easyshift.NewDefaultConfig(tmp)
	cfg.WebPort = real.WebPort
	if err := os.MkdirAll(cfg.ConfigDir, 0o700); err != nil {
		return nil, easyshift.Deps{}, nil, err
	}
	// Pull-secret preflight rejects an absent file; plant a syntactically
	// valid stand-in so the pipeline progresses.
	if err := easyshift.WritePullSecret(cfg.ConfigDir, []byte(`{"auths":{"sim":{"auth":"c2ltdWxhdGVk"}}}`)); err != nil {
		return nil, easyshift.Deps{}, nil, err
	}
	// Token for the cloudflare DNS provider — same reason.
	if err := easyshift.WriteDNSToken(cfg.ConfigDir, easyshift.DNSProviderCloudflare, []byte("simulated-token")); err != nil {
		return nil, easyshift.Deps{}, nil, err
	}
	deps, bundle := fakes.All()
	return cfg, deps, bundle, nil
}

// primaryHostIP returns the first non-loopback IPv4 address on the host.
func primaryHostIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}
