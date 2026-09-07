package sessionattachment

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

const runtimeExecutable = "/kelos/bin/kelos-session-runtime"

// Client transfers attachments through the Session Pod exec subresource.
type Client struct {
	clientset   kubernetes.Interface
	restConfig  *rest.Config
	newExecutor func(*rest.Config, string, *url.URL) (remotecommand.Executor, error)
}

// New creates a Session attachment client.
func New(restConfig *rest.Config) (*Client, error) {
	if restConfig == nil {
		return nil, errors.New("Kubernetes REST configuration must not be nil")
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes client: %w", err)
	}
	return &Client{clientset: clientset, restConfig: restConfig, newExecutor: remotecommand.NewSPDYExecutor}, nil
}

// Upload stores one attachment in a Session Pod.
func (c *Client) Upload(ctx context.Context, namespace, podName, name string, source io.Reader) (sessionruntime.Attachment, error) {
	var stdout bytes.Buffer
	if err := c.stream(ctx, namespace, podName, []string{runtimeExecutable, "attachment", "put", "--name", name}, source, &stdout); err != nil {
		return sessionruntime.Attachment{}, err
	}
	var attachment sessionruntime.Attachment
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&attachment); err != nil {
		return sessionruntime.Attachment{}, fmt.Errorf("decoding Session attachment metadata: %w", err)
	}
	if attachment.ID == "" || attachment.Name == "" || attachment.SizeBytes < 0 || attachment.SizeBytes > sessionruntime.MaxAttachmentBytes {
		return sessionruntime.Attachment{}, errors.New("Session attachment metadata is invalid")
	}
	return attachment, nil
}

// Download opens an attachment stream from a Session Pod. The caller must close it.
func (c *Client) Download(ctx context.Context, namespace, podName, id string) (sessionruntime.Attachment, io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	stdout, writer := io.Pipe()
	go func() {
		err := c.stream(ctx, namespace, podName, []string{runtimeExecutable, "attachment", "get", id}, nil, writer)
		_ = writer.CloseWithError(err)
	}()
	data := &attachmentStream{Reader: bufio.NewReader(stdout), pipe: stdout, cancel: cancel}
	metadata, err := data.ReadSlice('\n')
	if err != nil {
		_ = data.Close()
		return sessionruntime.Attachment{}, nil, fmt.Errorf("reading Session attachment metadata: %w", err)
	}
	var attachment sessionruntime.Attachment
	if err := json.Unmarshal(metadata, &attachment); err != nil {
		_ = data.Close()
		return sessionruntime.Attachment{}, nil, fmt.Errorf("decoding Session attachment metadata: %w", err)
	}
	if attachment.ID != id || attachment.SizeBytes < 0 || attachment.SizeBytes > sessionruntime.MaxAttachmentBytes {
		_ = data.Close()
		return sessionruntime.Attachment{}, nil, errors.New("Session attachment metadata is invalid")
	}
	return attachment, data, nil
}

type attachmentStream struct {
	*bufio.Reader
	pipe   *io.PipeReader
	cancel context.CancelFunc
}

func (s *attachmentStream) Close() error {
	s.cancel()
	return s.pipe.Close()
}

func (c *Client) stream(ctx context.Context, namespace, podName string, command []string, stdin io.Reader, stdout io.Writer) error {
	request := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   command,
		Stdin:     stdin != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, clientgoscheme.ParameterCodec)
	executor, err := c.newExecutor(c.restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("creating Session attachment exec connection: %w", err)
	}
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: &stderr}); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("transferring Session attachment: %s: %w", message, err)
		}
		return fmt.Errorf("transferring Session attachment: %w", err)
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		return fmt.Errorf("transferring Session attachment: %s", message)
	}
	return nil
}
