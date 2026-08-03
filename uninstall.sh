#!/usr/bin/env bash
# SciMesh uninstaller: stops the running component, removes its binary, and —
# when asked — deletes its data directory.
#
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/uninstall.sh | bash -s coordinator
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/uninstall.sh | bash -s worker
#   curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/uninstall.sh         # both
#
# Data (jobs, artifacts, users, secrets, the managed venv) is kept by default.
# Pass --purge (or set SCIMESH_PURGE=1) to delete it without asking; without
# it the script prompts interactively. Because `curl | bash` pipes have no
# interactive stdin, the default is always "keep data".
set -eu

COMPONENT="${1:-all}"
PURGE=0
for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=1 ;;
  esac
done
[ "${SCIMESH_PURGE:-0}" = "1" ] && PURGE=1

INSTALL_DIR="${SCIMESH_INSTALL_DIR:-$HOME/.local/bin}"

remove_component() {
  case "$1" in
    coordinator)
      echo "Stopping the coordinator (serve)…"
      pkill -x coordinator 2>/dev/null || true
      echo "Removing $INSTALL_DIR/coordinator…"
      rm -f "$INSTALL_DIR/coordinator"
      ;;
    worker)
      echo "Stopping the worker agent and its setup wizard…"
      pkill -x worker-agent 2>/dev/null || true
      echo "Removing $INSTALL_DIR/worker-agent…"
      rm -f "$INSTALL_DIR/worker-agent"
      ;;
    *) echo "unknown component: $1 (use 'coordinator', 'worker' or 'all')" >&2; exit 1 ;;
  esac
}

remove_data() {
  local dir="$1" label="$2"
  if [ ! -d "$dir" ]; then
    return 0
  fi
  if [ "$PURGE" = "1" ]; then
    rm -rf "$dir"
    echo "Deleted $dir"
    return 0
  fi
  # In a pipe (curl | bash) stdin is exhausted, so the prompt defaults to keep.
  printf "Delete %s (%s)? [y/N] " "$dir" "$label"
  read -r answer || answer=""
  case "$answer" in
    y|Y|yes|YES) rm -rf "$dir"; echo "Deleted $dir" ;;
    *) echo "Keeping $dir" ;;
  esac
}

case "$COMPONENT" in
  coordinator)
    remove_component coordinator
    remove_data "$HOME/.scimesh" "secrets, databases, artifacts, users, managed venv"
    ;;
  worker)
    remove_component worker
    remove_data "$HOME/.scimesh-worker" "worker config, runtime venv, logs"
    ;;
  all)
    remove_component coordinator
    remove_component worker
    remove_data "$HOME/.scimesh" "secrets, databases, artifacts, users, managed venv"
    remove_data "$HOME/.scimesh-worker" "worker config, runtime venv, logs"
    ;;
  *) echo "unknown component: $COMPONENT (use 'coordinator', 'worker' or 'all')" >&2; exit 1 ;;
esac

echo
echo "SciMesh $COMPONENT uninstalled."
echo "Pass --purge to also delete the data directories without asking."
