package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// stdinReader is the reader used for confirmation prompts.
// It defaults to os.Stdin but can be overridden in tests.
var stdinReader io.Reader = os.Stdin

// confirmOverride prompts the user to confirm overriding an existing resource.
// It returns true if the user answers "y" or "yes" (case-insensitive).
// If the input is empty or cannot be read, it returns false.
func confirmOverride(resource string) (bool, error) {
	fmt.Fprintf(os.Stderr, "Resource %s already exists. Override? [y/N]: ", resource)

	reader := bufio.NewReader(stdinReader)
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, nil
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}
