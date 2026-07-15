//go:build !darwin && !linux

package cli

import "io"

func probeSessionTUIDefaultColors(_ io.Reader, _ io.Writer) *sessionTUIDefaultColors {
	return nil
}
