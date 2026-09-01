// Command kubectl-celld is a kubectl plugin for managing celld fleets: deploy
// Workers, inspect fleet status, and bootstrap new fleets. Invoke it through
// kubectl as `kubectl celld <command>` (the binary must be on PATH as
// kubectl-celld) or directly as `celldctl`.
package main

import (
	"fmt"
	"os"

	"github.com/anthaathi/celld-deploy/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
