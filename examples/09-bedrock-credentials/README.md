# Bedrock Credentials

This example demonstrates running a Claude Code task using AWS Bedrock instead of the Anthropic API directly.

## Prerequisites

- AWS account with Bedrock access enabled for Claude models
- AWS IAM credentials with `bedrock:InvokeModel` permissions

## Option 1: Static Credentials (Secret)

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

### Optional Secret Keys

- `AWS_SESSION_TOKEN`: Required when using temporary credentials (e.g. from STS AssumeRole)
- `ANTHROPIC_BEDROCK_BASE_URL`: Custom Bedrock endpoint URL

### CLI with Static Credentials

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

## Option 2: IAM Roles for Service Accounts (IRSA)

On EKS, you can use IRSA instead of static credentials. The AWS SDK automatically picks up credentials from the projected service account token — no Secret needed.

### Prerequisites

1. Create an IAM role with `bedrock:InvokeModel` permissions
2. Create a Kubernetes ServiceAccount annotated with the IAM role:

   ```bash
   kubectl create serviceaccount bedrock-agent-sa
   kubectl annotate serviceaccount bedrock-agent-sa \
     eks.amazonaws.com/role-arn=arn:aws:iam::123456789012:role/bedrock-agent-role
   ```

3. Create the Task with `region` and `serviceAccountName` (no `secretRef`):

   ```bash
   kubectl apply -f task-irsa.yaml
   ```

### CLI with IRSA

```yaml
# ~/.kelos/config.yaml
bedrock:
  region: us-east-1
  serviceAccountName: bedrock-agent-sa
```

```bash
kelos run -p "Fix the bug"
```

Or with flags:

```bash
kelos run -p "Fix the bug" --credential-type bedrock --region us-east-1 --service-account bedrock-agent-sa
```
