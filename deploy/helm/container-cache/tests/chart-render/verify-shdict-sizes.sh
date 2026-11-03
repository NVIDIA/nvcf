#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
#
# Every nginx shared memory zone must be at least 1m. The slab allocator
# stores its bookkeeping in the first page(s) of a zone; on a 64K-page kernel
# (GB300 nodes) a 64k zone is a single page with no usable space, and nginx
# exits at start with "ngx_slab_alloc() failed: no memory". A 64k zone did
# exactly that and blocked a production rollout. Run from deploy/helm/container-cache.
set -euo pipefail

OUT_FILE="$(mktemp)"
trap 'rm -f "${OUT_FILE}"' EXIT

helm template c ./deploy > "${OUT_FILE}"

echo "Checking every shared zone is sized in megabytes..."
# lua_shared_dict NAME SIZE;   and   *_zone ... zone=NAME:SIZE
zones=$(grep -E 'lua_shared_dict[[:space:]]+[A-Za-z0-9_]+[[:space:]]+[0-9]+[kKmMgG]?;|zone=[A-Za-z0-9_]+:[0-9]+[kKmMgG]?' "${OUT_FILE}" \
  | sed -E 's/.*lua_shared_dict[[:space:]]+([A-Za-z0-9_]+)[[:space:]]+([0-9]+[kKmMgG]?);.*/\1 \2/; s/.*zone=([A-Za-z0-9_]+):([0-9]+[kKmMgG]?).*/\1 \2/' | sort -u)
if [ -z "${zones}" ]; then
  echo "FAILED: no shared zones found in the render" >&2
  exit 1
fi
# nginx size units: none = bytes, k = 1024, m = 1024^2, g = 1024^3.
to_bytes() {
  local n="${1%[kKmMgG]}" u="${1##*[0-9]}"
  case "${u}" in
    "")   echo "${n}" ;;
    k|K)  echo $((n * 1024)) ;;
    m|M)  echo $((n * 1024 * 1024)) ;;
    g|G)  echo $((n * 1024 * 1024 * 1024)) ;;
  esac
}
MIN_BYTES=$((1024 * 1024))
bad=0
while read -r name size; do
  if [ "$(to_bytes "${size}")" -lt "${MIN_BYTES}" ]; then
    echo "FAILED: zone ${name} is ${size}; must be at least 1m" >&2
    bad=1
  fi
done <<< "${zones}"
[ "${bad}" -eq 0 ]
echo "${zones}" | sed 's/^/  /'

echo "PASS: shared zone size checks"
