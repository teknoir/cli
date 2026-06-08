#!/bin/sh
set -e

# tnctl install script
# This script detects the platform and downloads the latest release of tnctl from GitHub.

REPO="teknoir/cli"
BINARY_NAME="tnctl"
INSTALL_DIR="/usr/local/bin"

if [ -n "$TNCTL_INSTALL_DIR" ]; then
    INSTALL_DIR="$TNCTL_INSTALL_DIR"
fi

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    darwin) OS="darwin" ;;
    linux) OS="linux" ;;
    msys*|cygwin*|mingw*) OS="windows" ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="x86_64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported Architecture: $ARCH"; exit 1 ;;
esac

# Determine extension
EXT="tar.gz"
if [ "$OS" = "windows" ]; then
    EXT="zip"
fi

# Get latest version from GitHub
VERSION=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$VERSION" ]; then
    echo "Could not find latest version for $REPO"
    exit 1
fi

echo "Downloading $BINARY_NAME $VERSION for $OS/$ARCH..."

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/${BINARY_NAME}_${OS}_${ARCH}.${EXT}"
TMP_DIR=$(mktemp -d)
TEMP_FILE="$TMP_DIR/${BINARY_NAME}.${EXT}"

curl -L -o "$TEMP_FILE" "$DOWNLOAD_URL"

if [ "$EXT" = "tar.gz" ]; then
    tar -xzf "$TEMP_FILE" -C "$TMP_DIR"
else
    unzip "$TEMP_FILE" -d "$TMP_DIR"
fi

echo "Installing to $INSTALL_DIR/$BINARY_NAME..."
if [ ! -d "$INSTALL_DIR" ]; then
    mkdir -p "$INSTALL_DIR" || sudo mkdir -p "$INSTALL_DIR"
fi

if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
    chmod +x "$INSTALL_DIR/$BINARY_NAME"
else
    sudo mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
    sudo chmod +x "$INSTALL_DIR/$BINARY_NAME"
fi

echo "$BINARY_NAME $VERSION installed successfully!"
rm -rf "$TMP_DIR"
