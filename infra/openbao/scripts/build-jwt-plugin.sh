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

# The plugin source lives in this repository, at ../plugins. It used to be
# cloned from a fork hosted elsewhere at a pinned revision, which meant a
# public build could not reproduce the image without access to that fork.
plugin_src=${PLUGIN_SRC:-"$repo_root/plugins/vault-plugin-secrets-jwt"}
vault_api_version=${VAULT_API_VERSION:-v1.15.0}
vault_sdk_version=${VAULT_SDK_VERSION:-v0.15.2}
x_net_version=${X_NET_VERSION:-v0.55.0}
output_dir=${OUTPUT_DIR:-"$repo_root/files/plugins"}

# Only a work dir this script created is ours to remove. Deleting a
# caller-supplied WORK_DIR would destroy a directory the caller still owns.
if [ -n "${WORK_DIR:-}" ]; then
  work_dir=$WORK_DIR
  work_dir_is_ours=0
else
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-openbao-jwt-plugin.XXXXXX")
  work_dir_is_ours=1
fi
src_dir="$work_dir/source"
build_dir="$work_dir/build"

cleanup() {
  if [ -z "${KEEP_WORK_DIR:-}" ] && [ "$work_dir_is_ours" = "1" ]; then
    rm -rf "$work_dir"
  else
    echo "Keeping work dir: $work_dir"
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$build_dir" "$output_dir"

# Copy rather than build in place: the steps below run `go get` and
# `go mod tidy`, which would otherwise rewrite the committed go.mod and go.sum
# of a source tree under version control.
mkdir -p "$src_dir"
cp -R "$plugin_src/." "$src_dir/"

(
  cd "$src_dir"
  go get \
    "github.com/hashicorp/vault/api@${vault_api_version}" \
    "github.com/hashicorp/vault/sdk@${vault_sdk_version}" \
    "golang.org/x/net@${x_net_version}"
  go mod tidy
  go test ./...

  for arch in amd64 arm64; do
    binary="vault-plugin-secrets-jwt-linux-${arch}"
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build \
      -o "$build_dir/$binary" \
      ./cmd/vault-plugin-secrets-jwt
    chmod 775 "$build_dir/$binary"
  done
)

for arch in amd64 arm64; do
  install -m 775 "$build_dir/vault-plugin-secrets-jwt-linux-${arch}" "$output_dir/"
done

PLUGIN_DIR="$output_dir" "$script_dir/verify-jwt-plugin.sh"
