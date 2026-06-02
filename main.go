package main

import (
	"os"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
