#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu
namespace=${JWT_SERVER_NAMESPACE:?}
statefulset=${JWT_SERVER_STATEFULSET:?}
state=$(kubectl -n "$namespace" get statefulset "$statefulset" -o json)
selector=$(printf '%s' "$state" | jq -er '
  .spec.selector.matchLabels | select(type == "object" and length > 0) |
  to_entries | map("\(.key)=\(.value)") | join(",")')
expected=$(printf '%s' "$state" | jq -er '.spec.replicas | select(type == "number" and . > 0)')
pods=$(kubectl -n "$namespace" get pods -l "$selector" --field-selector=status.phase=Running -o json)
members=$(printf '%s' "$pods" | jq -ce '
  .items | map(select(.status.phase == "Running" and .metadata.deletionTimestamp == null))')
count=$(printf '%s' "$members" | jq 'length')
if [ "$count" != "$expected" ]; then
    echo "Expected $expected running, non-terminating members for StatefulSet $statefulset; found $count" >&2
    exit 1
fi
versions_dir=${JWT_SHARED_VERSIONS_DIR:-/versions}
. "$versions_dir/manifest.env"
target_sha=$(cat "$versions_dir/target.sha")
for pod in $(printf '%s' "$members" | jq -r '.[].metadata.name'); do
    kubectl -n "$namespace" exec "$pod" -c openbao -- sh -ec '
      . /openbao/jwt-plugin-versions.env
      test "$JWT_PLUGIN_TARGET_VERSION" = "$1"
      actual=$(sha256sum "/openbao/plugins/vault-plugin-secrets-jwt-${JWT_PLUGIN_TARGET_VERSION}" | awk "{print \$1}")
      test "$actual" = "$2"
      jwt-plugin-lifecycle image-check
    ' sh "$JWT_PLUGIN_TARGET_VERSION" "$target_sha" || {
        echo "Rotate member $pod to the compatible bridge before enabling versioned mode" >&2
        exit 1
    }
done
