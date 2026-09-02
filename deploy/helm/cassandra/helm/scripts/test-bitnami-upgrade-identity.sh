#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Guard the immutable StatefulSet identity used by the published 0.15.5 chart.
# Kubernetes rejects a Helm upgrade when any volumeClaimTemplate metadata is
# removed, even though the PVC name and storage request remain unchanged.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="$(cd "${script_dir}/.." && pwd)"
render="$(mktemp)"
trap 'rm -f "${render}"' EXIT

release="cassandra"
helm template "${release}" "${chart_dir}" \
  --namespace plane-a-cassandra-system \
  --show-only templates/statefulset.yaml >"${render}"

if ! grep -Fqx -- '      serviceAccountName: default' "${render}"; then
  echo "upgraded StatefulSet must explicitly leave the removed Bitnami ServiceAccount" >&2
  exit 1
fi

vct="$({ sed -n '/^  volumeClaimTemplates:/,$p' "${render}"; } )"

assert_contains() {
  local expected="$1"
  if ! grep -Fqx -- "${expected}" <<<"${vct}"; then
    echo "missing published-0.15.5 volumeClaimTemplate identity: ${expected}" >&2
    sed -n '/^  volumeClaimTemplates:/,$p' "${render}" >&2
    exit 1
  fi
}

assert_contains '    - apiVersion: v1'
assert_contains '      kind: PersistentVolumeClaim'
assert_contains '          app.kubernetes.io/instance: cassandra'
assert_contains '          app.kubernetes.io/name: cassandra'
assert_contains '        name: data'

echo "Cassandra StatefulSet preserves the published 0.15.5 immutable PVC-template identity."
