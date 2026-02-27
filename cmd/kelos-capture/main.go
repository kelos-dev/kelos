package main

import (
	"os"

	"github.com/kelos-dev/kelos/internal/capture"
)

func main() {
	os.Exit(capture.Run())
}
