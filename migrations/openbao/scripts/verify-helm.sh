#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
go_bin=${GO:-go}
helm_dir=${HELM_DIR:-/out}
metadata=$(mktemp)
trap 'rm -f "$metadata"' EXIT

require_version() {
  module=$1
  required=$2
  optional=${3:-false}
  installed=$(awk -v module="$module" '$1 == "dep" && $2 == module { print $3 }' "$metadata")
  if [ -z "$installed" ] && [ "$optional" = true ]; then
    return
  fi
  if ! "$script_dir/verify-openbao.sh" --version-ge "$installed" "$required"; then
    echo "Helm embeds $module ${installed:-<missing>}; need $required or newer" >&2
    exit 1
  fi
}

for arch in ${ARCHES:-amd64 arm64}; do
  binary="$helm_dir/helm-linux-$arch"
  if [ ! -x "$binary" ]; then
    echo "missing executable Helm binary: $binary" >&2
    exit 1
  fi
  "$go_bin" version -m "$binary" > "$metadata"
  path=$(awk '$1 == "path" { print $2 }' "$metadata")
  goos=$(awk '$1 == "build" && $2 ~ /^GOOS=/ { sub(/^GOOS=/, "", $2); print $2 }' "$metadata")
  goarch=$(awk '$1 == "build" && $2 ~ /^GOARCH=/ { sub(/^GOARCH=/, "", $2); print $2 }' "$metadata")
  cgo=$(awk '$1 == "build" && $2 ~ /^CGO_ENABLED=/ { sub(/^CGO_ENABLED=/, "", $2); print $2 }' "$metadata")
  if [ "$path" != helm.sh/helm/v3/cmd/helm ] || [ "$goos" != linux ] || [ "$goarch" != "$arch" ] || [ "$cgo" != 0 ]; then
    echo "$binary has unexpected identity or target: path=$path GOOS=$goos GOARCH=$goarch CGO_ENABLED=$cgo" >&2
    exit 1
  fi
  toolchain=$(awk 'NR == 1 { sub(/^go/, "", $NF); print $NF }' "$metadata")
  if ! "$script_dir/verify-openbao.sh" --version-ge "$toolchain" 1.26.6; then
    echo "$binary has an unpatched Go toolchain: $toolchain" >&2
    exit 1
  fi
  # A replacement module could make the apparent dependency version misleading.
  if awk '$1 == "=>" { found = 1 } END { exit !found }' "$metadata"; then
    echo "$binary has unexpected module replacements" >&2
    exit 1
  fi
  require_version golang.org/x/crypto v0.52.0
  require_version golang.org/x/net v0.55.0
  require_version oras.land/oras-go/v2 v2.6.2
  # Helm 3.22 no longer links these modules. Reject old copies if reintroduced.
  require_version google.golang.org/grpc v1.82.2 true
  require_version github.com/containerd/containerd v1.7.33 true
  require_version github.com/moby/spdystream v0.5.1 true
  echo "verified $binary"
done
