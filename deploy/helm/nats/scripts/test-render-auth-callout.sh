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
release_name=${RELEASE_NAME:-nvcf-nats}
namespace=${NAMESPACE:-nats-system}
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
assert_contains "$default_render" "# Source: helm-nvcf-nats/templates/nats-auth-callout-nkeys-secret.yaml"
assert_contains "$default_render" "name: nats-auth-callout-nkeys"
assert_contains "$default_render" "nkey_signature:"
# secrets.json is no longer rendered into the Secret — vault-agent assembles
# it at the auth-callout pod's startup from KV.
assert_not_contains "$default_render" "secrets.json:"
assert_not_contains "$default_render" "nkey_mappings"
# nkey-bao-access RBAC must grant openbao-migrations read on both the
# shared-worker Secret and the auth-callout Secret so 19_setup_nats-auth-callout.sh
# can mirror seeds into KV.
assert_contains "$default_render" "- nats-nkeys"
assert_contains "$default_render" "- nats-auth-callout-nkeys"
# The post-install hook from the previous design must be gone.
assert_not_contains "$default_render" "nats-auth-callout-secrets-json-hook"

no_create_render="$tmpdir/no-create.yaml"
render --set nats.authCallout.createNkeySecret=false > "$no_create_render"
assert_not_contains "$no_create_render" "nkey_signature:"
assert_contains "$no_create_render" "name: nats-auth-callout-nkeys"

legacy_no_create_render="$tmpdir/legacy-no-create.yaml"
render --set authCallout.createNkeySecret=false > "$legacy_no_create_render"
assert_not_contains "$legacy_no_create_render" "nkey_signature:"
assert_contains "$legacy_no_create_render" "name: nats-auth-callout-nkeys"

custom_render="$tmpdir/custom-secret-name.yaml"
render --set nats.authCallout.secretName=custom-auth-callout-nkeys > "$custom_render"
assert_contains "$custom_render" "name: custom-auth-callout-nkeys"
assert_contains "$custom_render" "- custom-auth-callout-nkeys"
assert_not_contains "$custom_render" "name: nats-auth-callout-nkeys"
