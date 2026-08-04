#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

go_bin=${GO:-go}
plugin_dir=${PLUGIN_DIR:-"$repo_root/files/plugins"}
required_go_version=${REQUIRED_GO_VERSION:-v1.25.0}
required_x_net_version=${REQUIRED_X_NET_VERSION:-v0.55.0}
required_vault_api_version=${REQUIRED_VAULT_API_VERSION:-v1.15.0}
required_vault_sdk_version=${REQUIRED_VAULT_SDK_VERSION:-v0.15.2}
required_plugin_revision=${REQUIRED_PLUGIN_REVISION:-183b3159512f6fcfe766c8a3d738f47a751bad5c}

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
    BEGIN {
      split(current, a, ".")
      split(required, b, ".")
      for (i = 1; i <= 3; i++) {
        av = a[i] + 0
        bv = b[i] + 0
        if (av > bv) exit 0
        if (av < bv) exit 1
      }
      exit 0
    }
  '
}

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

sha256() {
  file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{ print $1 }'
  else
    shasum -a 256 "$file" | awk '{ print $1 }'
  fi
}

expected_sha256() {
  arch=$1
  case "$arch" in
    amd64) echo "be2a2bcea1e028c6a6be43877facafd12509c07aa09ce2da982fa9117135d006" ;;
    arm64) echo "88a14ef10d3fc1a6290ffc78de3367de92de7cb56e9d45a097c7c945f13ec77d" ;;
    *)
      echo "unknown architecture: $arch" >&2
      exit 1
      ;;
  esac
}

verify_binary() {
  arch=$1
  binary="$plugin_dir/vault-plugin-secrets-jwt-linux-${arch}"
  metadata=$(mktemp)
  metadata_files="$metadata_files $metadata"

  if [ ! -x "$binary" ]; then
    echo "missing executable JWT plugin binary: $binary" >&2
    exit 1
  fi

  "$go_bin" version -m "$binary" > "$metadata"

  toolchain=$(sed -n '1p' "$metadata" | awk -F': ' '{ print $2 }')
  toolchain_version="v${toolchain#go}"
  if ! version_ge "$toolchain_version" "$required_go_version"; then
    echo "$binary was built with $toolchain; need Go ${required_go_version#v} or newer" >&2
    exit 1
  fi

  path=$(awk '$1 == "path" { print $2 }' "$metadata")
  if [ "$path" != "github.com/outfoxx/vault-plugin-secrets-jwt/cmd/vault-plugin-secrets-jwt" ]; then
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

  plugin_revision=$(build_value vcs.revision "$metadata")
  if [ "$plugin_revision" != "$required_plugin_revision" ]; then
    echo "$binary was built from revision $plugin_revision; expected $required_plugin_revision" >&2
    exit 1
  fi

  expected_hash=$(expected_sha256 "$arch")
  actual_hash=$(sha256 "$binary")
  if [ "$actual_hash" != "$expected_hash" ]; then
    echo "$binary has sha256 $actual_hash; expected $expected_hash" >&2
    exit 1
  fi

  x_net_version=$(dep_version golang.org/x/net "$metadata")
  vault_api_version=$(dep_version github.com/hashicorp/vault/api "$metadata")
  vault_sdk_version=$(dep_version github.com/hashicorp/vault/sdk "$metadata")

  if ! version_ge "$x_net_version" "$required_x_net_version"; then
    echo "$binary embeds golang.org/x/net $x_net_version; need $required_x_net_version or newer" >&2
    exit 1
  fi
  if [ "$vault_api_version" != "$required_vault_api_version" ]; then
    echo "$binary embeds github.com/hashicorp/vault/api $vault_api_version; expected $required_vault_api_version" >&2
    exit 1
  fi
  if [ "$vault_sdk_version" != "$required_vault_sdk_version" ]; then
    echo "$binary embeds github.com/hashicorp/vault/sdk $vault_sdk_version; expected $required_vault_sdk_version" >&2
    exit 1
  fi

  echo "verified $binary"
  echo "  go: $toolchain"
  echo "  x/net: $x_net_version"
  echo "  vault/api: $vault_api_version"
  echo "  vault/sdk: $vault_sdk_version"
  echo "  vcs.revision: $plugin_revision"
  echo "  sha256: $actual_hash"

  rm -f "$metadata"
}

verify_binary amd64
verify_binary arm64
