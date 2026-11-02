#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

if [[ $# != 1 ]]; then
    echo 'Usage: run-live.sh /path/to/lab/environment.sh' >&2
    exit 2
fi
task_environment="$(realpath "$1")"
task_out="$(dirname "$task_environment")"
task_root="$(git rev-parse --show-toplevel)"
# The environment file is produced by prepare-lab.sh and contains no tokens.
source "$task_environment"
task_port="${NVCA_BDD_GATEWAY##*:}"
kubectl --kubeconfig "$NVCA_BDD_KUBECONFIG" -n nvca-upgrade-bdd \
    port-forward service/fixture "$task_port:8080" --address=127.0.0.1 \
    > "$task_out/port-forward.log" 2>&1 &
task_pf_pid=$!
trap 'kill "$task_pf_pid" 2>/dev/null || true; wait "$task_pf_pid" 2>/dev/null || true' EXIT
task_ready=false
for task_attempt in {1..30}; do
    if ! kill -0 "$task_pf_pid" 2>/dev/null; then
        echo 'The lab port-forward exited. Inspect port-forward.log.' >&2
        exit 1
    fi
    if grep -q '^Forwarding from' "$task_out/port-forward.log" && \
            curl --silent --fail "$NVCA_BDD_GATEWAY/v1/health" >/dev/null; then
        task_ready=true
        break
    fi
    sleep 1
done
if [[ "$task_ready" != true ]]; then
    echo 'Lab API did not become reachable within 30 seconds.' >&2
    exit 1
fi
cd "$task_root/tests/bdd"
go test -run '^TestNVCAUpgrade$' -count=1 -timeout=20m -v
