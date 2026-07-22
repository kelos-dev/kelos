package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/internal/sessionreset"
)

func newSessionResetCommand(cfg *ClientConfig) *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:   "reset NAME",
		Short: "Reset a Session by replacing its workspace and Pod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cl, namespace, err := cfg.NewClient()
			if err != nil {
				return err
			}
			if !yes {
				confirmed, err := confirmSessionReset(cmd.InOrStdin(), cmd.ErrOrStderr(), namespace, name)
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("resetting Session %q aborted", name)
				}
			}
			return runSessionReset(cmd.Context(), cl, namespace, name, cmd.OutOrStdout())
		},
	}
	command.Flags().BoolVarP(&yes, "yes", "y", false, "skip the destructive reset confirmation")
	command.ValidArgsFunction = completeSessionNames(cfg)
	return command
}

func confirmSessionReset(input io.Reader, output io.Writer, namespace, name string) (bool, error) {
	fmt.Fprintf(output, "Reset Session %s/%s? This permanently deletes its conversation history and workspace changes. [y/N]: ", namespace, name)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("reading Session %q reset confirmation: %w", name, err)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}

func runSessionReset(ctx context.Context, cl client.Client, namespace, name string, output io.Writer) error {
	_, requested, err := sessionreset.Request(ctx, cl, client.ObjectKey{Namespace: namespace, Name: name}, string(uuid.NewUUID()))
	if err != nil {
		return err
	}
	if !requested {
		fmt.Fprintf(output, "session/%s reset already requested\n", name)
		return nil
	}
	fmt.Fprintf(output, "session/%s reset requested\n", name)
	return nil
}
