#!/bin/bash

set -euo pipefail

echo "==> Installing Go tool dependencies..."
make -C /workspaces/kelos controller-gen envtest yamlfmt shfmt 2>/dev/null || true

echo "==> Downloading Go modules..."
cd /workspaces/kelos && go mod download

echo "==> Building kelos CLI..."
make -C /workspaces/kelos build WHAT=cmd/kelos 2>/dev/null || true

cat <<'MSG'

==> Done! To get started:
1. tailscale up --accept-routes
2. tsh login --proxy=anomalo.teleport.sh:443 --auth=google
3. gimme-creds
4. claude-bedrock
MSG
