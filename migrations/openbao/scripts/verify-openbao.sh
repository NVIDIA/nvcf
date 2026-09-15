#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)

go_bin=${GO:-go}
bao_dir=${BAO_DIR:-"$repo_root/files/openbao"}
arches=${ARCHES:-"amd64 arm64"}
required_x_crypto_version=${REQUIRED_X_CRYPTO_VERSION:-v0.56.0}
required_grpc_version=${REQUIRED_GRPC_VERSION:-v1.83.1}
required_go_archive_version=${REQUIRED_GO_ARCHIVE_VERSION:-v0.3.0}

metadata_files=
cleanup_metadata_files() {
  # shellcheck disable=SC2086
  rm -f $metadata_files
}
trap cleanup_metadata_files EXIT

version_ge() {
  current=${1#v}
  required=${2#v}
  awk -v current="$current" -v required="$required" '
    function is_numeric(value) {
      return value ~ /^[0-9]+$/
    }

    function valid_core(value, parts, count, i) {
      count = split(value, parts, ".")
      if (count != 3) return 0
      for (i = 1; i <= count; i++) {
        if (!is_numeric(parts[i])) return 0
        if (length(parts[i]) > 1 && substr(parts[i], 1, 1) == "0") return 0
      }
      return 1
    }

    function valid_identifiers(value, reject_numeric_leading_zero, parts, count, i) {
      if (value == "") return 0
      count = split(value, parts, ".")
      for (i = 1; i <= count; i++) {
        if (parts[i] == "" || parts[i] !~ /^[0-9A-Za-z-]+$/) return 0
        if (reject_numeric_leading_zero && is_numeric(parts[i]) && \
            length(parts[i]) > 1 && substr(parts[i], 1, 1) == "0") return 0
      }
      return 1
    }

    function compare_identifier(left, right) {
      if (is_numeric(left) && is_numeric(right)) {
        if ((left + 0) > (right + 0)) return 1
        if ((left + 0) < (right + 0)) return -1
        return 0
      }
      if (is_numeric(left)) return -1
      if (is_numeric(right)) return 1
      if (left > right) return 1
      if (left < right) return -1
      return 0
    }

    BEGIN {
      current_plus = index(current, "+")
      required_plus = index(required, "+")
      if (current_plus) {
        current_build = substr(current, current_plus + 1)
        current = substr(current, 1, current_plus - 1)
        if (!valid_identifiers(current_build, 0)) exit 2
      }
      if (required_plus) {
        required_build = substr(required, required_plus + 1)
        required = substr(required, 1, required_plus - 1)
        if (!valid_identifiers(required_build, 0)) exit 2
      }

      current_dash = index(current, "-")
      required_dash = index(required, "-")
      current_core = current_dash ? substr(current, 1, current_dash - 1) : current
      required_core = required_dash ? substr(required, 1, required_dash - 1) : required
      current_pre = current_dash ? substr(current, current_dash + 1) : ""
      required_pre = required_dash ? substr(required, required_dash + 1) : ""

      if (!valid_core(current_core, a) || !valid_core(required_core, b)) exit 2
      if (current_dash && !valid_identifiers(current_pre, 1)) exit 2
      if (required_dash && !valid_identifiers(required_pre, 1)) exit 2
      for (i = 1; i <= 3; i++) {
        av = a[i] + 0
        bv = b[i] + 0
        if (av > bv) exit 0
        if (av < bv) exit 1
      }

      # A stable release sorts after every prerelease with the same core.
      if (current_pre == "" && required_pre == "") exit 0
      if (current_pre == "") exit 0
      if (required_pre == "") exit 1

      current_count = split(current_pre, current_ids, ".")
      required_count = split(required_pre, required_ids, ".")
      count = current_count > required_count ? current_count : required_count
      for (i = 1; i <= count; i++) {
        if (i > current_count) exit 1
        if (i > required_count) exit 0
        comparison = compare_identifier(current_ids[i], required_ids[i])
        if (comparison > 0) exit 0
        if (comparison < 0) exit 1
      }
      exit 0
    }
  '
}

if [ "${1:-}" = "--version-ge" ]; then
  if [ "$#" -ne 3 ]; then
    echo "usage: $0 --version-ge CURRENT REQUIRED" >&2
    exit 2
  fi
  version_ge "$2" "$3"
  exit
fi

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

  if ! version_ge "$x_crypto_version" "$required_x_crypto_version"; then
    echo "$binary embeds golang.org/x/crypto $x_crypto_version; need $required_x_crypto_version or newer" >&2
    exit 1
  fi
  if ! version_ge "$grpc_version" "$required_grpc_version"; then
    echo "$binary embeds google.golang.org/grpc $grpc_version; need $required_grpc_version or newer" >&2
    exit 1
  fi
  if ! version_ge "$go_archive_version" "$required_go_archive_version"; then
    echo "$binary embeds github.com/moby/go-archive $go_archive_version; need $required_go_archive_version or newer" >&2
    exit 1
  fi

  echo "verified $binary"
  echo "  x/crypto: $x_crypto_version"
  echo "  grpc: $grpc_version"
  echo "  go-archive: $go_archive_version"

  rm -f "$metadata"
}

for arch in $arches; do
  verify_binary "$arch"
done
