package main

import (
	"os"

	"github.com/gjkim42/axon/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
