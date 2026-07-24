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

chart_dir=${CHART_DIR:-.}
release_name=${RELEASE_NAME:-function-autoscaler}
namespace=${NAMESPACE:-nvcf}
tmpdir=$(mktemp -d)

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

export HELM_CONFIG_HOME=${HELM_CONFIG_HOME:-"$tmpdir/helm-config"}
export HELM_CACHE_HOME=${HELM_CACHE_HOME:-"$tmpdir/helm-cache"}
export HELM_DATA_HOME=${HELM_DATA_HOME:-"$tmpdir/helm-data"}

render() {
  helm template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --values "$chart_dir/values.yaml" \
    --set functionautoscaler.image.repository=rs-autoscaler \
    "$@"
}

assert_contains() {
  file=$1
  pattern=$2

  if ! grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output to contain: $pattern" >&2
    return 1
  fi
}

assert_not_contains() {
  file=$1
  pattern=$2

  if grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output not to contain: $pattern" >&2
    return 1
  fi
}

default_render="$tmpdir/default.yaml"
render > "$default_render"
assert_contains "$default_render" "secrets.json.tmpl: |-"
assert_not_contains "$default_render" "nvcfApiToken"
assert_not_contains "$default_render" "NVCF_API_TOKEN_BLOCK"
assert_not_contains "$default_render" "services/nvcf-api/jwt/sign/nvcf-autoscaler-service"

static_token_render="$tmpdir/static-token.yaml"
render --set functionautoscaler.vault.staticNvcfApiToken.enabled=true > "$static_token_render"
assert_contains "$static_token_render" "services/nvcf-api/jwt/sign/nvcf-autoscaler-service"
assert_contains "$static_token_render" '"nvcfApiToken": "{{ .Data.token }}",'
assert_not_contains "$static_token_render" "NVCF_API_TOKEN_BLOCK"

custom_path_render="$tmpdir/custom-path.yaml"
render \
  --set functionautoscaler.vault.staticNvcfApiToken.enabled=true \
  --set functionautoscaler.vault.staticNvcfApiToken.path=services/custom/jwt/sign/autoscaler \
  > "$custom_path_render"
assert_contains "$custom_path_render" "services/custom/jwt/sign/autoscaler"
assert_not_contains "$custom_path_render" "services/nvcf-api/jwt/sign/nvcf-autoscaler-service"
