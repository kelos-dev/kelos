package sessionattachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

type attachmentExecutor struct {
	stream func(context.Context, remotecommand.StreamOptions) error
}

func (e attachmentExecutor) Stream(options remotecommand.StreamOptions) error {
	return e.stream(context.Background(), options)
}

func (e attachmentExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	return e.stream(ctx, options)
}

func testAttachmentClient(t *testing.T, stream func(context.Context, remotecommand.StreamOptions) error) *Client {
	t.Helper()
	client, err := New(&rest.Config{Host: "http://kubernetes.example"})
	if err != nil {
		t.Fatal(err)
	}
	client.newExecutor = func(_ *rest.Config, method string, endpoint *url.URL) (remotecommand.Executor, error) {
		if method != http.MethodPost || endpoint.Path != "/api/v1/namespaces/team-a/pods/chat-pod/exec" {
			t.Errorf("exec request = %s %s", method, endpoint)
		}
		if command := endpoint.Query()["command"]; !slices.Equal(command, []string{runtimeExecutable, "attachment", "get", "attachment-id"}) {
			t.Errorf("exec command = %q", command)
		}
		if container := endpoint.Query().Get("container"); container != "kelos-agent" {
			t.Errorf("exec container = %q, want kelos-agent", container)
		}
		return attachmentExecutor{stream: stream}, nil
	}
	return client
}

func TestDownloadStreamsBeforeExecCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	contents := bytes.Repeat([]byte("file contents\x00\n"), 4096)
	want := sessionruntime.Attachment{ID: "attachment-id", Name: "file.bin", MediaType: "application/octet-stream", SizeBytes: int64(len(contents))}
	metadata, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	client := testAttachmentClient(t, func(ctx context.Context, options remotecommand.StreamOptions) error {
		if options.Stdin != nil {
			t.Error("download exec has stdin")
		}
		prefix := append(append(metadata, '\n'), contents[:1024]...)
		if _, err := options.Stdout.Write(prefix); err != nil {
			return err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := options.Stdout.Write(contents[1024:])
		return err
	})
	attachment, body, err := client.Download(ctx, "team-a", "chat-pod", "attachment-id")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if attachment != want {
		t.Fatalf("attachment = %#v, want %#v", attachment, want)
	}
	close(release)
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, contents) {
		t.Fatalf("downloaded %d bytes with unexpected contents", len(got))
	}
}

func TestDownloadCloseCancelsExec(t *testing.T) {
	for _, blockedWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked_write=%t", blockedWrite), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			client := testAttachmentClient(t, func(ctx context.Context, options remotecommand.StreamOptions) error {
				defer func() { done <- ctx.Err() }()
				if _, err := io.WriteString(options.Stdout, "{\"id\":\"attachment-id\",\"sizeBytes\":1048576}\n"); err != nil {
					return err
				}
				if blockedWrite {
					_, err := options.Stdout.Write(make([]byte, 1024*1024))
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			})
			_, body, err := client.Download(ctx, "team-a", "chat-pod", "attachment-id")
			if err != nil {
				t.Fatal(err)
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("exec context error = %v, want context.Canceled", err)
				}
			case <-ctx.Done():
				t.Fatal("closing the download did not stop exec")
			}
		})
	}
}

func TestDownloadRejectsInvalidMetadata(t *testing.T) {
	for _, metadata := range []string{
		"not JSON\n",
		`{"id":"attachment-id"}`,
		"{\"id\":\"another-id\",\"sizeBytes\":0}\n",
		"{\"id\":\"attachment-id\",\"sizeBytes\":-1}\n",
		fmt.Sprintf("{\"id\":\"attachment-id\",\"sizeBytes\":%d}\n", sessionruntime.MaxAttachmentBytes+1),
		strings.Repeat(" ", 8192),
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		client := testAttachmentClient(t, func(_ context.Context, options remotecommand.StreamOptions) error {
			_, err := io.WriteString(options.Stdout, metadata)
			return err
		})
		_, body, err := client.Download(ctx, "team-a", "chat-pod", "attachment-id")
		cancel()
		if body != nil {
			_ = body.Close()
			t.Fatal("Download() returned a body for invalid metadata")
		}
		if err == nil {
			t.Fatal("Download() accepted invalid metadata")
		}
	}
}

func TestDownloadPropagatesExecErrors(t *testing.T) {
	for _, afterMetadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_metadata=%t", afterMetadata), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			wantErr := errors.New("transfer failed")
			client := testAttachmentClient(t, func(_ context.Context, options remotecommand.StreamOptions) error {
				if afterMetadata {
					if _, err := io.WriteString(options.Stdout, "{\"id\":\"attachment-id\",\"sizeBytes\":4}\npart"); err != nil {
						return err
					}
				}
				return wantErr
			})
			_, body, err := client.Download(ctx, "team-a", "chat-pod", "attachment-id")
			if afterMetadata {
				if err != nil {
					t.Fatal(err)
				}
				defer body.Close()
				var data []byte
				data, err = io.ReadAll(body)
				if string(data) != "part" {
					t.Fatalf("partial body = %q, want part", data)
				}
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("download error = %v, want %v", err, wantErr)
			}
		})
	}
}
