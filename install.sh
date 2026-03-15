#!/bin/sh
set -e

REPO="marmutapp/openmarmut"
BINARY="openmarmut"

# Detect OS.
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  mingw*|msys*|cygwin*) OS="windows" ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Detect architecture.
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Get latest release tag.
LATEST=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$LATEST" ]; then
  echo "Failed to fetch latest release." >&2
  exit 1
fi
VERSION="${LATEST#v}"

# Build download URL.
EXT="tar.gz"
if [ "$OS" = "windows" ]; then
  EXT="zip"
fi
ARCHIVE="${BINARY}_${VERSION}_${OS}_${ARCH}.${EXT}"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ARCHIVE}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${BINARY} ${LATEST} for ${OS}/${ARCH}..."
curl -sSL -o "${TMPDIR}/${ARCHIVE}" "$URL"
curl -sSL -o "${TMPDIR}/checksums.txt" "$CHECKSUMS_URL"

# Verify checksum.
cd "$TMPDIR"
if command -v sha256sum >/dev/null 2>&1; then
  grep "$ARCHIVE" checksums.txt | sha256sum -c --quiet
elif command -v shasum >/dev/null 2>&1; then
  grep "$ARCHIVE" checksums.txt | shasum -a 256 -c --quiet
else
  echo "Warning: cannot verify checksum (sha256sum/shasum not found)." >&2
fi

# Extract.
if [ "$EXT" = "zip" ]; then
  unzip -q "$ARCHIVE"
else
  tar xzf "$ARCHIVE"
fi

# Install binary.
INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  INSTALL_DIR="${HOME}/bin"
  mkdir -p "$INSTALL_DIR"
  echo "Installing to ${INSTALL_DIR} (add to PATH if needed)."
fi

cp "${BINARY}" "${INSTALL_DIR}/${BINARY}"
chmod +x "${INSTALL_DIR}/${BINARY}"

echo "${BINARY} ${LATEST} installed to ${INSTALL_DIR}/${BINARY}!"
echo "Run '${BINARY} --help' to get started."
