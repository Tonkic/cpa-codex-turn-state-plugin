#!/usr/bin/env sh
# Build the CPA native plugin for linux/amd64.
#
# The plugin is a cgo c-shared object, so the build runs inside the same
# toolchain CPA's plugin store uses. Nothing else on the host is required
# beyond a working Docker daemon.
#
# Usage: ./scripts/build-linux.sh [--skip-tests]
set -eu

skip_tests=0
for argument in "$@"; do
  case "$argument" in
    --skip-tests) skip_tests=1 ;;
    *) echo "unknown argument: $argument" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."

version="$(sed -n 's/.*pluginVersion *= *"\([^"]*\)".*/\1/p' plugin.go | head -1)"
if [ -z "$version" ]; then
  echo "cannot read pluginVersion from plugin.go" >&2
  exit 1
fi

image="${CPA_PLUGIN_BUILDER_IMAGE:-golang:1.26-bookworm}"
platform="${CPA_PLUGIN_BUILDER_PLATFORM:-linux/amd64}"
output="dist/linux-amd64/cpa-codex-turn-state-v${version}.so"
# Reuse one module cache volume so repeated builds stay offline-friendly.
modcache="${CPA_PLUGIN_GOMOD_VOLUME:-cpa-plugin-gomod}"

mkdir -p "$(dirname "$output")"

echo "building ${output} with ${image} for ${platform}"
if [ "$skip_tests" -eq 0 ]; then
  docker run --rm --platform "$platform" -v "$PWD:/src" -v "$modcache:/go/pkg/mod" -w /src \
    -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 "$image" sh -c 'go vet ./... && go test ./...'
fi

docker run --rm --platform "$platform" -v "$PWD:/src" -v "$modcache:/go/pkg/mod" -w /src \
  -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 "$image" \
  go build -buildvcs=false -trimpath -ldflags="-s -w" -buildmode=c-shared -o "$output" .

ls -l "$output" "${output%.so}.h"
echo "next: ./scripts/install-linux.sh"
