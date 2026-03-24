# Bedrock Credentials

This example demonstrates running a Claude Code task using AWS Bedrock instead of the Anthropic API directly.

## Prerequisites

- AWS account with Bedrock access enabled for Claude models
- AWS IAM credentials with `bedrock:InvokeModel` permissions

## Setup

1. Create the Secret with your AWS credentials:

   ```bash
   kubectl create secret generic bedrock-credentials \
     --from-literal=AWS_ACCESS_KEY_ID=<your-access-key> \
     --from-literal=AWS_SECRET_ACCESS_KEY=<your-secret-key> \
     --from-literal=AWS_REGION=us-east-1
   ```

2. Create the Task:

   ```bash
   kubectl apply -f task.yaml
   ```

## Using the CLI

You can also use `kelos run` with a config file:

```yaml
# ~/.kelos/config.yaml
bedrock:
  accessKeyID: AKIAIOSFODNN7EXAMPLE
  secretAccessKey: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
  region: us-east-1
```

```bash
kelos run -p "Fix the bug"
```

Or with a pre-created secret:

```bash
kelos run -p "Fix the bug" --credential-type bedrock --secret bedrock-credentials
```

## Optional Fields

- `AWS_SESSION_TOKEN`: Required when using temporary credentials (e.g. from STS AssumeRole)
- `ANTHROPIC_BEDROCK_BASE_URL`: Custom Bedrock endpoint URL

## IAM Roles for Service Accounts (IRSA)

On EKS, you can use IRSA instead of static credentials. In that case, use `podOverrides.env` to set only the required environment variables:

```yaml
spec:
  credentials:
    type: api-key
    secretRef:
      name: dummy-secret  # Required by schema; not used by Bedrock
  podOverrides:
    env:
      - name: CLAUDE_CODE_USE_BEDROCK
        value: "1"
      - name: AWS_REGION
        value: us-east-1
```

Note: First-class IRSA support (making `secretRef` optional for bedrock) is planned for a future release.
