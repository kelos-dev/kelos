#!/usr/bin/env bash
# Download and install the axon CLI binary.
# Usage: curl -fsSL https://raw.githubusercontent.com/axon-core/axon/main/hack/install.sh | bash

set -euo pipefail

REPO="axon-core/axon"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  linux | darwin) ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

BINARY="axon-${OS}-${ARCH}"
BASE_URL="${AXON_RELEASE_URL:-https://github.com/${REPO}/releases/latest/download}"
URL="${BASE_URL}/${BINARY}"

echo "Downloading ${BINARY}..."
TMP="$(mktemp)"
if ! curl -fsSL -o "$TMP" "$URL"; then
  echo "Failed to download ${URL}" >&2
  rm -f "$TMP"
  exit 1
fi

chmod +x "$TMP"

if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP" "${INSTALL_DIR}/axon"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "$TMP" "${INSTALL_DIR}/axon"
fi

echo "axon installed to ${INSTALL_DIR}/axon"
