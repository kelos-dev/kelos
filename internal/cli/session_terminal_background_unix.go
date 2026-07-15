//go:build darwin || linux

package cli

import (
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const sessionTUIBackgroundProbeTimeout = 100 * time.Millisecond

func probeSessionTUIDefaultColors(input io.Reader, output io.Writer) *sessionTUIDefaultColors {
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	if !inputOK || !outputOK || !term.IsTerminal(int(inputFile.Fd())) || !term.IsTerminal(int(outputFile.Fd())) {
		return nil
	}

	state, err := term.MakeRaw(int(inputFile.Fd()))
	if err != nil {
		return nil
	}
	defer func() { _ = term.Restore(int(inputFile.Fd()), state) }()

	return querySessionTUIDefaultColors(inputFile, outputFile, sessionTUIBackgroundProbeTimeout)
}

func querySessionTUIDefaultColors(input *os.File, output io.Writer, timeout time.Duration) *sessionTUIDefaultColors {
	flags, err := unix.FcntlInt(input.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return nil
	}
	if err := unix.SetNonblock(int(input.Fd()), true); err != nil {
		return nil
	}
	defer func() { _, _ = unix.FcntlInt(input.Fd(), unix.F_SETFL, flags) }()

	if _, err := io.WriteString(output, "\x1b]10;?\x1b\\\x1b]11;?\x1b\\"); err != nil {
		return nil
	}

	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 0, 256)
	chunk := make([]byte, 256)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		pollTimeout := max(1, int(remaining.Milliseconds()))
		pollFDs := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		ready, err := unix.Poll(pollFDs, pollTimeout)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return nil
		}
		if ready == 0 {
			return nil
		}

		count, err := unix.Read(int(input.Fd()), chunk)
		if count > 0 {
			buffer = append(buffer, chunk[:count]...)
			if len(buffer) > sessionTUIBackgroundProbeMaxBytes {
				return nil
			}
			if colors := parseSessionTUIDefaultColors(buffer); colors != nil {
				return colors
			}
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil
		}
		if count == 0 && err == nil {
			return nil
		}
	}
}
