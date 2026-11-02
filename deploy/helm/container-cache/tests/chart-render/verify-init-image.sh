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
# Every image the chart runs must come from values, so one registry org and one
# pull secret cover the whole pod. A hard-coded public base image for the
# ownership init container failed to pull once the pod carried an org-scoped
# pull secret and left the StatefulSet stuck mid-rollout.
# Run from deploy/helm/container-cache.
set -euo pipefail

OUT_FILE="$(mktemp)"
trap 'rm -f "${OUT_FILE}"' EXIT

assert_has() {
  if ! grep -F -q -- "$1" "${OUT_FILE}"; then
    echo "FAILED: expected pattern not found: $1" >&2
    exit 1
  fi
}

assert_not_has() {
  if grep -F -q -- "$1" "${OUT_FILE}"; then
    echo "FAILED: unexpected pattern found: $1" >&2
    exit 1
  fi
}

echo "Checking no image is hard-coded to a public registry org..."
helm template c ./deploy > "${OUT_FILE}"
assert_not_has 'nvcr.io/nvidia/'

echo "Checking the init container defaults to the certificates image..."
helm template c ./deploy --set images.certificates=reg.example/org/certs:9 > "${OUT_FILE}"
# Two containers run from that image: init-chown-data and certs.
count=$(grep -F -c 'image: "reg.example/org/certs:9"' "${OUT_FILE}" || true)
count=$((count + $(grep -F -c 'image:  reg.example/org/certs:9' "${OUT_FILE}" || true)))
if [ "${count}" -lt 2 ]; then
  echo "FAILED: expected init-chown-data and certs to both use the certificates image, found ${count}" >&2
  exit 1
fi

echo "Checking the init image is overridable..."
helm template c ./deploy --set images.init=reg.example/org/init:1 > "${OUT_FILE}"
assert_has 'image: "reg.example/org/init:1"'

echo "Checking the init command prefers distribution coreutils..."
assert_has 'export PATH=/usr/bin:/bin:$PATH && chmod 755 /proxy_cache /container_cache'

echo "PASS: init image render checks"
