package main

import (
	"os"

	"github.com/axon-core/axon/internal/capture"
)

func main() {
	os.Exit(capture.Run())
}
