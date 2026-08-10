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

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
umbrella_root="$(cd "$repo_root/../../.." && pwd)"
values_file="$umbrella_root/tools/ci/helm-validate-values/gateway-routes-vanity.yaml"
ci_render="$(mktemp)"
default_render="$(mktemp)"
trap 'rm -f "$ci_render" "$default_render"' EXIT

helm lint "$repo_root/chart" -f "$values_file"
helm template nvcf-gateway-routes "$repo_root/chart" \
  -f "$values_file" \
  > "$ci_render"

grep -q 'name: ci-vanity-gateway' "$ci_render"
grep -q 'ci-vanity.localhost' "$ci_render"
grep -q 'name: ci-vanity-backend' "$ci_render"
grep -q 'namespace: ci-vanity' "$ci_render"
grep -q 'port: 9090' "$ci_render"
grep -q 'ci.nvidia.com/validated: enabled' "$ci_render"

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.vanityGateway.enabled=true \
  > "$default_render"

grep -q 'vanity.localhost' "$default_render"

echo "Vanity Gateway route render checks passed."
