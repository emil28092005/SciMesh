#!/usr/bin/env bash
# SciMesh installer: downloads the coordinator binary for this OS/architecture
# from the latest GitHub release and installs it locally. One command, no
# picking from a list of files:
#
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash
#
# Installed to ~/.local/bin/coordinator (Linux/macOS). Then run:
#
#   coordinator serve --open
set -eu

REPO="emil28092005/SciMesh"
VERSION="${SCIMESH_VERSION:-latest}"
INSTALL_DIR="${SCIMESH_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)      echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  echo "Resolving the newest SciMesh release (including pre-releases)..."
  # GitHub's /releases/latest only sees stable releases; the API list is
  # newest-first across all channels. Without jq, pull the first tag_name.
  RESOLVED=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases?per_page=1" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
  if [ -n "$RESOLVED" ]; then
    VERSION="$RESOLVED"
    echo "  -> $VERSION"
  else
    echo "  -> falling back to the stable latest release"
  fi
fi

mkdir -p "$INSTALL_DIR"
TARGET="$INSTALL_DIR/coordinator"

URL="https://github.com/${REPO}/releases/download/${VERSION}/coordinator-${OS}-${ARCH}"
echo "Downloading $URL"
curl -fsSL -o "$TARGET.tmp" "$URL"
chmod +x "$TARGET.tmp"
mv "$TARGET.tmp" "$TARGET"

echo
echo "SciMesh installed: $TARGET"
"$TARGET" --version
echo
echo "Start the platform (one command, everything embedded):"
echo "  $TARGET serve --open"
echo
echo "Your data lives in ~/.scimesh. The admin login is printed on first start."
