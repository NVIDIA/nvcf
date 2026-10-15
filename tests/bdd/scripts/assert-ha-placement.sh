#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Reads `kubectl get pods -o json` on stdin and asserts that at least the
# given number of replicas (default 2) are Running and Ready, each on a
# distinct node. Prints one ha-placement=ok marker line on success.

set -euo pipefail

if ! command -v jq >/dev/null 2>&1; then
  echo "required tool not found: jq" >&2
  exit 127
fi

min_replicas="${1:-2}"
if ! [[ "${min_replicas}" =~ ^[1-9][0-9]*$ ]]; then
  echo "minimum replica count must be a positive integer: ${min_replicas}" >&2
  exit 2
fi

pods_json="$(cat)"

summary="$(jq -r '
  [.items[]] as $pods
  | ($pods | length) as $total
  | ([$pods[] | select(
        .status.phase == "Running"
        and any(.status.conditions[]?; .type == "Ready" and .status == "True")
      )] | length) as $ready
  | ([$pods[] | .spec.nodeName // empty] | unique | length) as $nodes
  | "\($total) \($ready) \($nodes)"
' <<<"${pods_json}")" || {
  echo "input is not a kubectl pod list" >&2
  exit 1
}

read -r total ready nodes <<<"${summary}"

if (( total < min_replicas )); then
  echo "expected at least ${min_replicas} pods, found ${total}" >&2
  exit 1
fi
if (( ready != total )); then
  echo "expected all ${total} pods Running and Ready, found ${ready}" >&2
  exit 1
fi
if (( nodes != total )); then
  echo "expected ${total} pods on ${total} distinct nodes, found ${nodes} nodes" >&2
  exit 1
fi

printf 'ha-placement=ok replicas=%s nodes=%s\n' "${total}" "${nodes}"
