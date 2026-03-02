#!/usr/bin/env sh
set -eu

REPO="cosmobean/runic"
VERSION="${1:-latest}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/runic-${OS}-${ARCH}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/runic-${OS}-${ARCH}"
fi

DEST="${HOME}/.local/bin/runic"
mkdir -p "$(dirname "$DEST")"
curl -fsSL "$URL" -o "$DEST"
chmod +x "$DEST"
echo "installed to $DEST"
