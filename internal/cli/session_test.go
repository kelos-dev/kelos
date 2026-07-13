package cli

import (
	"bytes"
	"context"
	"io"
	"testing"

	"k8s.io/client-go/rest"
)

func TestSessionConnectStreamsReadySession(t *testing.T) {
	restConfig := &rest.Config{Host: "https://kubernetes.invalid"}
	stdin := bytes.NewBufferString("hello\n")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	connected := false
	dependencies := sessionConnectDependencies{
		resolveConfig: func() (*rest.Config, string, error) {
			return restConfig, "team-a", nil
		},
		connect: func(_ context.Context, gotConfig *rest.Config, namespace, name string, gotStdin io.Reader, gotStdout, gotStderr io.Writer, color bool) error {
			connected = true
			if gotConfig != restConfig || namespace != "team-a" || name != "chat" {
				t.Fatalf("Session connection = config %p namespace %q name %q", gotConfig, namespace, name)
			}
			if gotStdin != stdin || gotStdout != stdout || gotStderr != stderr {
				t.Fatal("Session connection did not receive the command streams")
			}
			if !color {
				t.Fatal("Session connection did not enable forced color output")
			}
			return nil
		},
	}
	command := newSessionConnectCommandWithDependencies(&ClientConfig{}, dependencies)
	command.SetArgs([]string{"chat", "--color=always"})
	command.SetIn(stdin)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("session connect error = %v", err)
	}
	if !connected {
		t.Fatal("session connect did not start the terminal stream")
	}
}

func TestTerminalColorEnabled(t *testing.T) {
	tests := []struct {
		mode    string
		want    bool
		wantErr bool
	}{
		{mode: "always", want: true},
		{mode: "never", want: false},
		{mode: "auto", want: false},
		{mode: "sometimes", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			got, err := terminalColorEnabled(test.mode, &bytes.Buffer{})
			if (err != nil) != test.wantErr {
				t.Fatalf("terminalColorEnabled(%q) error = %v, wantErr %t", test.mode, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("terminalColorEnabled(%q) = %t, want %t", test.mode, got, test.want)
			}
		})
	}
}
