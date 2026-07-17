#!/bin/sh
# agentic installer — downloads the latest release binary to ~/.local/bin.
set -e

REPO="maorbril/agentic"
INSTALL_DIR="${AGENTIC_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin | linux) ;;
  *) echo "unsupported OS: $os (use install.ps1 on Windows)" >&2; exit 1 ;;
esac

echo "Fetching latest release..."
tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
if [ -z "$tag" ]; then
  echo "could not determine latest release of $REPO" >&2
  exit 1
fi

asset="agentic-$os-$arch"
url="https://github.com/$REPO/releases/download/$tag/$asset"
mkdir -p "$INSTALL_DIR"
echo "Downloading $asset ($tag)..."
curl -fsSL -o "$INSTALL_DIR/agentic" "$url"
chmod +x "$INSTALL_DIR/agentic"

echo "Installed agentic $tag to $INSTALL_DIR/agentic"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "NOTE: $INSTALL_DIR is not on your PATH — add it to your shell profile." ;;
esac
echo "Next: agentic setup"
