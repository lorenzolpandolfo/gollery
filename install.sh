#!/bin/sh
set -e

REPO="lorenzolpandolfo/gollery"
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64)
    ASSET="gollery-x86"
    ;;
  aarch64|arm64)
    ASSET="gollery-arm64"
    ;;
  armv7*|armv6*|arm)
    ASSET="gollery-arm32"
    ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

URL="https://github.com/$REPO/releases/latest/download/$ASSET"

echo "Downloading Gollery for $ARCH..."
curl -fsSL "$URL" -o gollery.tmp
chmod +x gollery.tmp
mv -f gollery.tmp gollery

echo "Gollery installed successfully!"
echo "Start it with:"
echo "  ./gollery"
