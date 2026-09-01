// Package cli implements the kubectl-celld plugin commands.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCommand builds the plugin's command tree. Context, namespace, and
// kubeconfig come from the standard client-go loading rules, so kubectl's
// --context / -n flags work when invoked as `kubectl celld ...`.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubectl celld",
		Short: "Deploy and manage celld fleets on Kubernetes",
		Long: `Deploy and manage celld fleets on Kubernetes.

A celld fleet runs one public Worker application from an object-storage
bucket prefix. This plugin deploys Worker source to a fleet's bucket,
inspects fleet health, and bootstraps new CelldFleet resources.

Subcommands:
  deploy   Upload a new Worker version to a fleet's bucket
  status   Show fleet health, rollout, routes, and recent events
  init     Generate (and optionally apply) a CelldFleet manifest
  logs     Stream logs from a fleet's celld nodes`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newDeployCommand(),
		newStatusCommand(),
		newInitCommand(),
		newLogsCommand(),
	)
	return root
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
