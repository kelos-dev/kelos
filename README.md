# Axon

[![CI](https://github.com/gjkim42/axon/actions/workflows/ci.yaml/badge.svg)](https://github.com/gjkim42/axon/actions/workflows/ci.yaml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gjkim42/axon)](https://github.com/gjkim42/axon)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

Kubernetes controller for running AI agents (e.g. Claude Code) as Jobs.

## Overview

Axon manages AI agent workloads on Kubernetes. Create a `Task` resource and Axon will run the specified agent as a Kubernetes Job, tracking its progress and status.

Currently supported agents:
- `claude-code` - Anthropic's Claude Code CLI

## Quick Start

### Prerequisites

- Kubernetes cluster (1.28+)
- kubectl configured

### Installation

```bash
# Install CRDs and controller
kubectl apply -f install-crd.yaml
kubectl apply -f install.yaml
```

Or install directly from GitHub:
```bash
kubectl apply -f https://raw.githubusercontent.com/gjkim42/axon/main/install-crd.yaml
kubectl apply -f https://raw.githubusercontent.com/gjkim42/axon/main/install.yaml
```

To uninstall:
```bash
kubectl delete -f install.yaml
kubectl delete -f install-crd.yaml
```

### Create a Task

#### Using API Key

1. Create a Secret with your API key:
```bash
kubectl create secret generic anthropic-api-key \
  --from-literal=ANTHROPIC_API_KEY=<your-api-key>
```

2. Create a Task:
```yaml
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: hello-world
spec:
  type: claude-code
  prompt: "Create a hello world program in Python"
  credentials:
    type: api-key
    secretRef:
      name: anthropic-api-key
```

#### Using OAuth

1. Create a Secret with your OAuth token:
```bash
kubectl create secret generic claude-oauth \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your-oauth-token>
```

2. Create a Task:
```yaml
apiVersion: axon.io/v1alpha1
kind: Task
metadata:
  name: hello-world
spec:
  type: claude-code
  prompt: "Create a hello world program in Python"
  credentials:
    type: oauth
    secretRef:
      name: claude-oauth
```

#### Apply and Watch

```bash
kubectl apply -f task.yaml
kubectl get tasks -w
```

## Task Spec

| Field | Description | Required |
|-------|-------------|----------|
| `type` | Agent type (`claude-code`) | Yes |
| `prompt` | Task prompt for the agent | Yes |
| `credentials.type` | `api-key` or `oauth` | Yes |
| `credentials.secretRef.name` | Secret name with credentials | Yes |
| `model` | Model override (e.g., `claude-sonnet-4-20250514`) | No |

## Task Status

| Phase | Description |
|-------|-------------|
| `Pending` | Task accepted, Job not yet running |
| `Running` | Job is actively running |
| `Succeeded` | Job completed successfully |
| `Failed` | Job failed |

## Development

```bash
# Generate code and CRDs
make update

# Verify (generate, fmt, vet, tidy check)
make verify

# Run tests
make test        # unit tests
make test-e2e    # e2e tests

# Build
make build       # binary
make image       # docker image
```

## License

Apache 2.0
