#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
# shellcheck source=infra/openbao/scripts/semver.sh
. "$script_dir/semver.sh"

go_bin=${GO:-go}
bao_dir=${BAO_DIR:-"$repo_root/files/openbao"}
arches=${ARCHES:-"amd64 arm64"}
required_go_version=${REQUIRED_GO_VERSION:-v1.27.0}
required_x_crypto_version=${REQUIRED_X_CRYPTO_VERSION:-v0.56.0}
required_grpc_version=${REQUIRED_GRPC_VERSION:-v1.83.1}
required_go_archive_version=${REQUIRED_GO_ARCHIVE_VERSION:-v0.3.0}

metadata_files=
cleanup_metadata_files() {
  # shellcheck disable=SC2086
  rm -f $metadata_files
}
trap cleanup_metadata_files EXIT

dep_version() {
  module=$1
  metadata=$2
  awk -v module="$module" '$1 == "dep" && $2 == module { print $3 }' "$metadata"
}

build_value() {
  key=$1
  metadata=$2
  awk -v key="$key" '$1 == "build" && $2 ~ ("^" key "=") { sub("^" key "=", "", $2); print $2 }' "$metadata"
}

verify_binary() {
  arch=$1
  binary="$bao_dir/bao-linux-${arch}"
  metadata=$(mktemp)
  metadata_files="$metadata_files $metadata"

  if [ ! -x "$binary" ]; then
    echo "missing executable OpenBao binary: $binary" >&2
    exit 1
  fi

  "$go_bin" version -m "$binary" > "$metadata"

  toolchain=$(sed -n '1p' "$metadata" | awk -F': ' '{ print $2 }')
  toolchain_version="v${toolchain#go}"
  if ! semver_ge "$toolchain_version" "$required_go_version"; then
    echo "$binary was built with $toolchain; need Go ${required_go_version#v} or newer" >&2
    exit 1
  fi

  path=$(awk '$1 == "path" { print $2 }' "$metadata")
  if [ "$path" != "github.com/openbao/openbao" ]; then
    echo "$binary has unexpected module path: $path" >&2
    exit 1
  fi

  goos=$(build_value GOOS "$metadata")
  goarch=$(build_value GOARCH "$metadata")
  cgo_enabled=$(build_value CGO_ENABLED "$metadata")
  if [ "$goos" != "linux" ] || [ "$goarch" != "$arch" ] || [ "$cgo_enabled" != "0" ]; then
    echo "$binary has unexpected target metadata: GOOS=$goos GOARCH=$goarch CGO_ENABLED=$cgo_enabled" >&2
    exit 1
  fi

  x_crypto_version=$(dep_version golang.org/x/crypto "$metadata")
  grpc_version=$(dep_version google.golang.org/grpc "$metadata")
  go_archive_version=$(dep_version github.com/moby/go-archive "$metadata")

  if ! semver_ge "$x_crypto_version" "$required_x_crypto_version"; then
    echo "$binary embeds golang.org/x/crypto $x_crypto_version; need $required_x_crypto_version or newer" >&2
    exit 1
  fi
  if ! semver_ge "$grpc_version" "$required_grpc_version"; then
    echo "$binary embeds google.golang.org/grpc $grpc_version; need $required_grpc_version or newer" >&2
    exit 1
  fi
  if ! semver_ge "$go_archive_version" "$required_go_archive_version"; then
    echo "$binary embeds github.com/moby/go-archive $go_archive_version; need $required_go_archive_version or newer" >&2
    exit 1
  fi

  echo "verified $binary"
  echo "  go: $toolchain"
  echo "  x/crypto: $x_crypto_version"
  echo "  grpc: $grpc_version"
  echo "  go-archive: $go_archive_version"

  rm -f "$metadata"
}

for arch in $arches; do
  verify_binary "$arch"
done
