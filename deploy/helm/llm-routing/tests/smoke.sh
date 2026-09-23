#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
context=${1:?Usage: smoke.sh CONTEXT [NAMESPACE] [LOCAL_PORT]}
namespace=${2:-llm-routing}
port=${3:-18080}
script_dir=$(cd "$(dirname "$0")" && pwd)
log=$(mktemp)
kubectl --context "$context" -n "$namespace" port-forward svc/llm-api-gateway "$port:8080" > "$log" 2>&1 &
forward_pid=$!
trap 'kill "$forward_pid" 2>/dev/null || true; rm -f "$log"' EXIT
for attempt in $(seq 1 30); do
  if ! kill -0 "$forward_pid" 2>/dev/null; then
    cat "$log" >&2
    exit 1
  fi
  if grep -q 'Forwarding from' "$log"; then break; fi
  sleep 1
done
grep -q 'Forwarding from' "$log"
python3 "$script_dir/smoke.py" "http://127.0.0.1:$port"
