#!/usr/bin/env bash
# SciMesh installer: downloads a binary for this OS/architecture from the
# newest GitHub release and installs it locally. One command, no picking from
# a list of files:
#
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash -s worker
#
# The first form installs the coordinator (the whole platform in one binary:
# databases, userservice, local workers). The second installs a standalone
# worker agent that joins an existing coordinator. Installed to ~/.local/bin
# (Linux/macOS) or %LOCALAPPDATA%\SciMesh (Windows).
set -eu

REPO="emil28092005/SciMesh"
COMPONENT="${1:-coordinator}"
VERSION="${SCIMESH_VERSION:-latest}"
INSTALL_DIR="${SCIMESH_INSTALL_DIR:-$HOME/.local/bin}"
# Auto-start the component right after install and open its UI (the control
# room for the coordinator, the local setup wizard for the worker). Set
# SCIMESH_AUTO_START=0 to install only.
AUTO_START="${SCIMESH_AUTO_START:-1}"

case "$COMPONENT" in
  coordinator) BINARY="coordinator" ;;
  worker)      BINARY="worker-agent" ;;
  *) echo "unknown component: $COMPONENT (use 'coordinator' or 'worker')" >&2; exit 1 ;;
esac

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
TARGET="$INSTALL_DIR/$BINARY"

URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY}-${OS}-${ARCH}"
echo "Downloading $URL"
curl -fsSL -o "$TARGET.tmp" "$URL"
chmod +x "$TARGET.tmp"
mv "$TARGET.tmp" "$TARGET"

echo
echo "SciMesh $COMPONENT installed: $TARGET"
INSTALLED_VERSION=$("$TARGET" --version 2>/dev/null | awk '{print $2}')
"$TARGET" --version
if [ -n "$INSTALLED_VERSION" ] && [ "$INSTALLED_VERSION" != "${VERSION#v}" ]; then
  echo
  echo "WARNING: expected version $VERSION but got $INSTALLED_VERSION."
  echo "This is usually a stale download cache. Re-run the installer in a few"
  echo "minutes, or pin the version explicitly:"
  echo "  SCIMESH_VERSION=${VERSION} bash <(curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh)"
fi

if [ "$COMPONENT" = "coordinator" ]; then
  if [ "$AUTO_START" = "1" ]; then
    echo
    echo "Starting the platform and opening the control room in your browser..."
    echo "(stop it with Ctrl-C; it keeps your data in ~/.scimesh)"
    echo
    exec "$TARGET" serve --open
  fi
  echo
  echo "Start the platform (one command, everything embedded):"
  echo "  $TARGET serve --open"
  echo
  echo "Your data lives in ~/.scimesh. The admin login is printed on first start."
else
  if [ "$AUTO_START" = "1" ]; then
    echo
    echo "Starting the local setup wizard in your browser..."
    echo "(stop it with Ctrl-C; it keeps the configuration in ~/.scimesh-worker)"
    echo
    exec "$TARGET" setup
  fi
  echo
  echo "The worker needs Python 3 with the scimesh package, then a coordinator"
  echo "to connect to. Point the local wizard at it:"
  echo
  echo "  $TARGET setup"
  echo
  echo "Or run it with environment variables:"
  echo
  echo "  export COORDINATOR_URL=http://COORDINATOR_HOST:8080"
  echo "  export WORKER_AUTH_TOKEN=<worker token from the coordinator>"
  echo "  export WORK_DIR=~/scimesh-worker"
  echo "  $TARGET"
  echo
  echo "For a coordinator started with 'coordinator serve', the worker token is"
  echo "in ~/.scimesh/worker.token on that machine. Set SCIMESH_PIP_PACKAGE to"
  echo "install scimesh into a managed venv, or install it yourself:"
  echo "  pip install scimesh"
fi
