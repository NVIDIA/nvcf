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

log_hash() {
  arch=$1
  hash=$2
  printf 'vault-plugin-secrets-jwt-linux-%s sha256=%s\n' "$arch" "$hash"
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
  if [ "$path" != "github.com/NVIDIA/nvcf/infra/openbao/plugins/vault-plugin-secrets-jwt/cmd/vault-plugin-secrets-jwt" ]; then
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

  # No vcs.revision assertion. It used to pin the external fork this plugin was
  # cloned from, which git could stamp because the build ran inside a clone.
  # Neither build path has git metadata now: this script builds from a copy in
  # a temp dir, and the image build COPYs the source into a layer. Requiring
  # the stamp fails both, and requiring a specific value pins a revision that
  # no longer exists. Provenance is instead carried by the assertions above -
  # module path, target triple, toolchain floor - plus the dependency versions
  # checked below, all of which are stamped without git.

  # Hashes are recorded, not asserted. They were pinned when the binary came
  # from a frozen external revision and could therefore be reproduced exactly.
  # Now any source edit in this repository legitimately changes them, so an
  # equality check would fail on every real change and teach people to update
  # the constant without reading it. The provenance that still holds is
  # asserted above: module path, target, toolchain, dependency versions.
  actual_hash=$(sha256 "$binary")
  log_hash "$arch" "$actual_hash"

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
  echo "  sha256: $actual_hash"

  rm -f "$metadata"
}

verify_binary amd64
verify_binary arm64
