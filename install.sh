#!/usr/bin/env bash
set -euo pipefail

REPO="quantumcycle/spex"
BIN_NAME="spex"
INSTALL_DIR="/usr/local/bin"

# Detect OS
case "$(uname -s)" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)
    echo "Unsupported OS: $(uname -s)" >&2
    exit 1
    ;;
esac

# Detect architecture
case "$(uname -m)" in
  x86_64)          ARCH="amd64" ;;
  arm64|aarch64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

BINARY="${BIN_NAME}-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${BINARY}"

echo "Downloading ${BINARY}..."
curl -fsSL "$URL" -o "/tmp/${BIN_NAME}"
chmod +x "/tmp/${BIN_NAME}"

echo "Installing to ${INSTALL_DIR}/${BIN_NAME} (may require sudo)..."
if [ -w "$INSTALL_DIR" ]; then
  mv "/tmp/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
else
  sudo mv "/tmp/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
fi

echo "Installed $(${INSTALL_DIR}/${BIN_NAME} --version 2>/dev/null || echo ${BIN_NAME}) to ${INSTALL_DIR}/${BIN_NAME}"
