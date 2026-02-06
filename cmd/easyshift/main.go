package main

import (
	"fmt"
	"log"
	"os"
	"os/user"

	"github.com/raghavendra-talur/easyshift"
	"github.com/spf13/cobra"
)

var (
	debug bool
	cfg   *easyshift.Config
)

func main() {
	// Check if running as root
	if u, err := user.Current(); err != nil || u.Uid != "0" {
		fmt.Println("Error: easyshift must be run as root")
		os.Exit(1)
	}

	cfg = easyshift.GetConfig()

	rootCmd := &cobra.Command{
		Use:   "easyshift",
		Short: "easyshift - Easy OpenShift Cluster Management",
		Long: `easyshift is a tool for creating and managing OpenShift clusters.
It provides a simple interface for cluster lifecycle management.`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			easyshift.InitLogging(debug)
		},
	}

	// Global flags
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")

	// Add commands
	rootCmd.AddCommand(
		newCreateCommand(),
		newStartCommand(),
		newStopCommand(),
		newDeleteCommand(),
		newListCommand(),
		// newStatusCommand(),
	)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func newCreateCommand() *cobra.Command {
	var (
		name        string
		domain      string
		ocpVersion  string
		masterCount int
		workerCount int
		masterRAM   int
		workerRAM   int
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new OpenShift cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return easyshift.CreateCluster(&easyshift.ClusterConfig{
				Name:        name,
				Domain:      domain,
				OCPVersion:  ocpVersion,
				MasterCount: masterCount,
				WorkerCount: workerCount,
				MasterRAM:   masterRAM,
				WorkerRAM:   workerRAM,
			})
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Cluster name")
	cmd.Flags().StringVarP(&domain, "domain", "D", "local", "Cluster domain")
	cmd.Flags().StringVarP(&ocpVersion, "version", "v", easyshift.DefaultOCPVersion, "OpenShift version")
	cmd.Flags().IntVarP(&masterCount, "masters", "m", 1, "Number of master nodes")
	cmd.Flags().IntVarP(&workerCount, "workers", "w", 0, "Number of worker nodes")
	cmd.Flags().IntVar(&masterRAM, "master-ram", 32768, "Master node RAM in MB")
	cmd.Flags().IntVar(&workerRAM, "worker-ram", 16384, "Worker node RAM in MB")

	cmd.MarkFlagRequired("name")

	return cmd
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start [cluster-name]",
		Short: "Start a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return easyshift.StartCluster(args[0])
		},
	}
}

func newStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [cluster-name]",
		Short: "Stop a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return easyshift.StopCluster(args[0])
		},
	}
}

func newDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete [cluster-name]",
		Short: "Delete a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return easyshift.DeleteCluster(args[0])
		},
	}
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			return easyshift.ListClusters()
		},
	}
}

//func newStatusCommand() *cobra.Command {
//	return &cobra.Command{
//		Use:   "status [cluster-name]",
//		Short: "Show cluster status",
//		Args:  cobra.ExactArgs(1),
//		RunE: func(cmd *cobra.Command, args []string) error {
//			return easyshift.ShowClusterStatus(args[0])
//		},
//	}
//}
