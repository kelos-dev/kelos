# Bedrock Credentials

This example demonstrates running a Claude Code task using AWS Bedrock instead of the Anthropic API directly. It uses the `none` credential type with `podOverrides` to inject provider-specific environment variables.

## Prerequisites

- AWS account with Bedrock access enabled for Claude models
- AWS IAM credentials with `bedrock:InvokeModel`, `bedrock:InvokeModelWithResponseStream`, and `bedrock:ListInferenceProfiles` permissions

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

## Option 2: IAM Roles for Service Accounts (IRSA)

On EKS, you can use IRSA instead of static credentials. The AWS SDK automatically picks up credentials from the projected service account token — no Secret needed.

### Prerequisites

1. Create an IAM role with Bedrock permissions
2. Create a Kubernetes ServiceAccount annotated with the IAM role:

   ```bash
   kubectl create serviceaccount bedrock-agent-sa
   kubectl annotate serviceaccount bedrock-agent-sa \
     eks.amazonaws.com/role-arn=arn:aws:iam::123456789012:role/bedrock-agent-role
   ```

3. Create the Task:

   ```bash
   kubectl apply -f task-irsa.yaml
   ```

## How it works

The `none` credential type tells Kelos not to inject any built-in credentials. Instead, you supply provider-specific env vars via `podOverrides.env` and optionally set `podOverrides.serviceAccountName` for workload identity.

This pattern works for any provider (Bedrock, Vertex AI, Azure OpenAI, etc.) — just change the environment variables.
