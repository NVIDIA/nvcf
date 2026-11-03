#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Checks an nvstor-peer image layer (tar.gz).
#
#   image_layer_test.sh runtime LAYER        the rdma-core runtime layer
#   image_layer_test.sh binary LAYER PATH    the layer holding the binary at PATH
#
# runtime: libibverbs, the mlx5 provider and its driver file, libnl and the
# dpkg status.d entries are present; nothing lands on the merged-/usr symlinks
# (/lib, /bin, /sbin, /lib64) and no development file is shipped.
# binary: PATH is a regular file, executable by its owner, group and others.
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }

[ $# -ge 2 ] || fail "usage: $0 runtime LAYER | binary LAYER PATH"
mode=$1 layer=$2
[ -f "$layer" ] || fail "$layer: no such file"
# One line per entry: type+mode, then the path without a leading ./ or /,
# then the link target for a symlink.
listing=$(tar -tvzf "$layer" | awk '{
  path = $6; sub(/^\.?\//, "", path)
  target = ""; if ($7 == "->") target = $8
  print $1, path, target
}')

has() { # has TYPE REGEX: an entry of TYPE (- file, l symlink) whose path matches REGEX.
  local kind=file
  [ "$1" = l ] && kind=symlink
  grep -Eq "^$1[^ ]* $2( |\$)" <<<"$listing" || fail "no $kind matching $2 in $layer"
}

case "$mode" in
  runtime)
    m='usr/lib/(x86_64|aarch64)-linux-gnu'
    has - "$m/libibverbs\.so\.1\.[0-9.]+"
    has l "$m/libibverbs\.so\.1"
    has - "$m/libmlx5\.so\.1\.[0-9.]+"
    has l "$m/libibverbs/libmlx5-rdmav[0-9]+\.so"
    has - 'etc/libibverbs\.d/mlx5\.driver'
    has l "$m/libnl-3\.so\.200"
    has l "$m/libnl-route-3\.so\.200"
    for pkg in libibverbs1 ibverbs-providers libnl-3-200 libnl-route-3-200; do
      has - "var/lib/dpkg/status\.d/$pkg"
    done
    # The provider symlink must resolve inside the layer.
    target=$(grep -E "^l[^ ]* $m/libibverbs/libmlx5-rdmav[0-9]+\.so " <<<"$listing" | awk '{print $3}')
    [[ "$target" == ../libmlx5.so.1.* ]] || fail "libmlx5 provider links to '$target'"
    has - "$m/${target#../}"
    if grep -Eq '^[^ ]+ (lib|lib64|bin|sbin)(/| |$)' <<<"$listing"; then
      fail "entries under /lib, /lib64, /bin or /sbin would replace the base image's merged-/usr symlinks"
    fi
    if grep -Eq '^[^ ]+ (usr/include/|[^ ]*/libibverbs\.so |[^ ]*\.a )' <<<"$listing"; then
      fail "development files in the runtime layer"
    fi
    ;;
  binary)
    [ $# -eq 3 ] || fail "binary needs PATH"
    path=${3#/}
    line=$(grep -E "^[^ ]+ $path( |\$)" <<<"$listing") || fail "$3 not in $layer"
    [[ "$line" == -??x??x??x* ]] || fail "$3 is not a regular file executable by all: $line"
    ;;
  *) fail "unknown mode $mode" ;;
esac
echo "PASS: $mode $layer"
