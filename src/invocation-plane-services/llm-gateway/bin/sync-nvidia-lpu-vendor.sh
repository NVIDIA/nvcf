#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Populate nvidia-lpu-vendor/* from the module cache (full NVIDIA LPU module trees).
# Downloads each module explicitly so this works before replace targets exist.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ ! -f go.mod ]]; then
  echo "error: run from llm-api-gateway root (go.mod not found)" >&2
  exit 1
fi

mkdir -p "$ROOT/nvidia-lpu-vendor"

GOMODCACHE="$(go env GOMODCACHE)"

list_file=$(mktemp)
trap 'rm -f "$list_file"' EXIT
awk '
  /^[[:space:]]*require[[:space:]]*\(/ { inreq = 1; next }
  /^[[:space:]]*\)[[:space:]]*$/ {
    if (inreq) {
      inreq = 0
    }
    next
  }
  inreq && $1 ~ /^github.com\/nvidia-lpu\// && NF >= 2 && $2 != "=>" {
    ver = $2
    sub(/\/\/.*/, "", ver)
    print $1, ver
  }
' "$ROOT/go.mod" >"$list_file"
if [[ ! -s "$list_file" ]]; then
  echo "error: no github.com/nvidia-lpu entries found in go.mod require blocks" >&2
  exit 1
fi

while read -r mod ver; do
  [[ -z "${mod:-}" ]] && continue
  go mod download "${mod}@${ver}"
  cache_dir="${GOMODCACHE}/${mod}@${ver}"
  if [[ ! -d "$cache_dir" ]]; then
    echo "error: module cache dir missing for ${mod}@${ver} at ${cache_dir}" >&2
    exit 1
  fi
  name="${mod##github.com/nvidia-lpu/}"
  dest="$ROOT/nvidia-lpu-vendor/$name"
  if [[ -e "$dest" ]]; then
    # Module cache trees are read-only; cp -a preserves that, so chmod before rm.
    chmod -R u+w "$dest"
  fi
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -a "$cache_dir"/. "$dest"/
  echo "synced ${mod}@${ver} -> nvidia-lpu-vendor/$name"
done <"$list_file"
