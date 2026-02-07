package main

import (
	"os"

	"github.com/axon-core/axon/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
