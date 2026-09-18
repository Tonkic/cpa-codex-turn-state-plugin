#!/usr/bin/env sh
# Install the built plugin into a CPA plugins tree.
#
# CPA hot-swaps a plugin when a new version-stamped file appears, so the file
# name carries the version and the previous build stays in place as a rollback.
# The plugins directory is owned by the CPA container user, therefore the copy
# goes through a throwaway container instead of requiring sudo.
#
# Usage: CPA_ROOT=$HOME/CLIProxyAPI ./scripts/install-linux.sh
set -eu

cd "$(dirname "$0")/.."

version="$(sed -n 's/.*pluginVersion *= *"\([^"]*\)".*/\1/p' plugin.go | head -1)"
cpa_root="${CPA_ROOT:-$HOME/CLIProxyAPI}"
target_dir="${cpa_root}/plugins/linux/amd64"
source="dist/linux-amd64/cpa-codex-turn-state-v${version}.so"

if [ ! -f "$source" ]; then
  echo "missing $source; run ./scripts/build-linux.sh first" >&2
  exit 1
fi
if [ ! -d "$target_dir" ]; then
  echo "missing $target_dir; set CPA_ROOT to the CLIProxyAPI checkout" >&2
  exit 1
fi

name="$(basename "$source")"
docker run --rm \
  -v "$PWD/dist/linux-amd64:/src:ro" \
  -v "$target_dir:/dst" \
  -e PLUGIN_FILE="$name" \
  alpine:3 sh -c 'cp "/src/$PLUGIN_FILE" "/dst/$PLUGIN_FILE" && chmod 0644 "/dst/$PLUGIN_FILE" && ls -l "/dst"'

echo
echo "installed ${name} into ${target_dir}"
echo "verify: curl -s -H 'Authorization: Bearer <management-key>' http://127.0.0.1:8317/v0/management/codex-turn-state/status"
echo "panel:  /v0/resource/plugins/cpa-codex-turn-state/panel"
