#!/usr/bin/env bash
# SciMesh installer: downloads a binary for this OS/architecture from the
# newest GitHub release, installs it locally, then starts it and opens its UI
# in the browser (SCIMESH_AUTO_START=0 installs only). One command:
#
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash -s worker
#
# The first form installs the coordinator (the whole platform in one binary:
# databases, userservice, local workers) and opens the admin console. The
# second installs a standalone worker agent that joins an existing
# coordinator, and opens its local setup wizard. Installed to ~/.local/bin
# (Linux/macOS) or %LOCALAPPDATA%\SciMesh (Windows).
set -eu

REPO="emil28092005/SciMesh"

# Public half of the Ed25519 key that signs SHA256SUMS.txt in releases. The
# private half lives in the repository secret SCIMESH_SIGNING_KEY. Verification
# uses openssl when available; without openssl the installer falls back to the
# checksum-only check with a warning.
SCIMESH_SIGNING_PUBKEY='MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA01rjmCme4W4zAgBwbO00LvwgnB1srlg0LbooRG8ej7iNxzOtJ8vjRFR2Cu7z7OKjoDo9/0GW3pvcwB+ndBB6yUwht33IRwdsnbioBI4M7LL+yC1ubi4fJ5bigOgZ9VsVqKdU3T9GYxmrfJF1UexiOg6HjoRLO3V4Id+3e/CiI5Sr8UMfJMXUfO3uiEs9RpstxpP1V/UU4YDicTF0QjkOESimEwwXBG4z3VcVmQtqkb7Q3413iekTdQ13093GKAKp0Q2ia1TpB2su6ELUhHAqhmK88cJ73Opy1uEVye0twov4BFTu5GkxgazNTuU//aYVWVpd/NAlD+VVSmpDsbfBBQIDAQAB'

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

# Verify the SHA-256 checksum from the release before installing. This guards
# against corrupted downloads and stale CDN caches; it does not protect
# against an active MITM on the same channel (the checksum file travels it
# too). Set SCIMESH_SKIP_VERIFY=1 to bypass.
if [ "${SCIMESH_SKIP_VERIFY:-0}" != "1" ]; then
  if SUMFILE=$(mktemp) && curl -fsSL -o "$SUMFILE" "https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS.txt"; then
    EXPECTED=$(awk '$2 == "'"$(basename "$URL")"'" {print $1}' "$SUMFILE" 2>/dev/null | head -1)
    SIGFILE="$SUMFILE.sig"
    if [ "${SCIMESH_SKIP_SIGNATURE:-0}" != "1" ] && command -v openssl >/dev/null 2>&1 \
        && curl -fsSL -o "$SIGFILE" "https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS.txt.sig" 2>/dev/null; then
      PUBKEY_FILE=$(mktemp)
      printf '%s\n' '-----BEGIN PUBLIC KEY-----' "$SCIMESH_SIGNING_PUBKEY" '-----END PUBLIC KEY-----' > "$PUBKEY_FILE"
      if openssl dgst -sha256 -verify "$PUBKEY_FILE" -signature "$SIGFILE" "$SUMFILE" >/dev/null 2>&1; then
        echo "Signature verified (RSA-2048/SHA-256)"
      else
        rm -f "$PUBKEY_FILE" "$SIGFILE" "$SUMFILE" "$TARGET.tmp"
        echo "ERROR: the release signature does not verify; the download channel may be tampered with." >&2
        echo "Retry later, or bypass with SCIMESH_SKIP_SIGNATURE=1." >&2
        exit 1
      fi
      rm -f "$PUBKEY_FILE"
    elif [ "${SCIMESH_SKIP_SIGNATURE:-0}" != "1" ] && ! command -v openssl >/dev/null 2>&1; then
      echo "WARNING: openssl not found; falling back to checksum verification only"
    fi
    rm -f "$SUMFILE" "$SIGFILE"
    if [ -n "$EXPECTED" ]; then
      ACTUAL=$(sha256sum "$TARGET.tmp" | awk '{print $1}')
      if [ "$ACTUAL" != "$EXPECTED" ]; then
        rm -f "$TARGET.tmp"
        echo "ERROR: checksum mismatch for $BINARY (got $ACTUAL, want $EXPECTED)" >&2
        echo "The download may be corrupted or served by a stale cache. Retry later, or" >&2
        echo "pin the version with SCIMESH_VERSION=${VERSION} and re-run." >&2
        exit 1
      fi
      echo "Checksum verified ($(echo "$EXPECTED" | cut -c1-12)…)"
    else
      echo "WARNING: no checksum entry for $(basename "$URL"); skipping verification"
    fi
  else
    echo "WARNING: could not fetch SHA256SUMS.txt; skipping verification"
  fi
fi

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
    echo "Starting the platform and opening the admin console in your browser..."
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
  echo "  SCIMESH_PIP_PACKAGE=<your wheel or index> worker-agent setup"
fi
