#!/usr/bin/env bash

# Script to update the Homebrew formula with checksums from a GitHub release
# Usage: ./hack/update-homebrew-formula.sh v1.2.3 [/path/to/tap-repo]

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <version> [tap-repo-path]" >&2
  exit 1
fi

VERSION="$1"
TAP_DIR="${2:-.}"
REPO="kelos-dev/kelos"
CHECKSUMS_FILE="/tmp/checksums.txt"

trap 'rm -f /tmp/checksums.txt' EXIT

echo "Fetching checksums for ${VERSION}..."

# Download checksums
gh release download "${VERSION}" \
  --repo "${REPO}" \
  --pattern "checksums.txt" \
  --dir /tmp

# Parse checksums
declare -A SHAS
while IFS= read -r line; do
  sha=$(echo "$line" | awk '{print $1}')
  filename=$(echo "$line" | awk '{print $2}')

  # Extract arch from filename (e.g., kelos-linux-amd64 -> linux-amd64)
  arch="${filename#kelos-}"

  SHAS["$arch"]="$sha"
done <"$CHECKSUMS_FILE"

# Verify we have all required checksums
required_archs=("darwin-amd64" "darwin-arm64" "linux-amd64" "linux-arm64")
for arch in "${required_archs[@]}"; do
  if [[ -z "${SHAS["$arch"]:-}" ]]; then
    echo "Error: Missing checksum for $arch" >&2
    exit 1
  fi
done

# Update formula
FORMULA_FILE="${TAP_DIR}/Formula/kelos.rb"

# Strip leading 'v' so Homebrew gets a bare version number (e.g., 1.2.3 not v1.2.3)
BARE_VERSION="${VERSION#v}"
# Note: sed -i without a backup suffix requires GNU sed. This script is intended to run
# in CI on Ubuntu. On macOS, use `sed -i '' ...` or install gnu-sed via Homebrew.
sed -i "s/version \"VERSION_PLACEHOLDER\"/version \"${BARE_VERSION}\"/" "$FORMULA_FILE"
sed -i "s/sha256 \"SHA256_MACOS_AMD64_PLACEHOLDER\"/sha256 \"${SHAS["darwin-amd64"]}\"/" "$FORMULA_FILE"
sed -i "s/sha256 \"SHA256_MACOS_ARM64_PLACEHOLDER\"/sha256 \"${SHAS["darwin-arm64"]}\"/" "$FORMULA_FILE"
sed -i "s/sha256 \"SHA256_LINUX_AMD64_PLACEHOLDER\"/sha256 \"${SHAS["linux-amd64"]}\"/" "$FORMULA_FILE"
sed -i "s/sha256 \"SHA256_LINUX_ARM64_PLACEHOLDER\"/sha256 \"${SHAS["linux-arm64"]}\"/" "$FORMULA_FILE"

echo "✓ Updated $FORMULA_FILE with version $VERSION"
echo "  Checksums:"
for arch in "${required_archs[@]}"; do
  echo "    $arch: ${SHAS["$arch"]}"
done
