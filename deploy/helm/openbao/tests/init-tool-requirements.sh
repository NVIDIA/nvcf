#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
bin_dir="$test_root/bin"
mkdir -p "$bin_dir"
for tool in dirname jq tr sort head; do
    ln -s "$(command -v "$tool")" "$bin_dir/$tool"
done
cat > "$bin_dir/kubectl" <<'MOCK'
#!/bin/sh
if [ "$1" = version ]; then
    printf '%s\n' '{"serverVersion":{"gitVersion":"v1.36.4"}}'
    exit 0
fi
printf '%s\n' "$*" >> "$KUBECTL_CALLS"
case "$*" in
    *containerStatuses*) printf '%s\n' true; exit 0 ;;
esac
exit 1
MOCK
chmod +x "$bin_dir/kubectl"
export KUBECTL_CALLS="$test_root/kubectl-calls"

for source_dir in "$project_dir" "$project_dir/helm/scripts"; do
    # Reproduce the chart's ConfigMap mounts without requiring a cluster.
    stage="$test_root/stage"
    mkdir -p "$stage/utils"
    cp "$source_dir/deploy.sh" "$stage/deploy.sh"
    if [ -d "$source_dir/utils" ]; then
        cp "$source_dir/utils/utils.sh" "$source_dir/utils/log.sh" "$stage/utils/"
    else
        cp "$source_dir/utils.sh" "$source_dir/log.sh" "$stage/utils/"
    fi
    ln -sf "$(type -P true)" "$bin_dir/jwker"
    : > "$KUBECTL_CALLS"
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" test openbao script > "$test_root/script.log" 2>&1; then
        echo 'Standalone mode unexpectedly succeeded without Helm' >&2
        exit 1
    fi
    grep -q 'helm check failed' "$test_root/script.log"
    test ! -s "$KUBECTL_CALLS"

    # Hook mode must reach Kubernetes initialization without checking Helm.
    # The fake API rejects initialization, so no cluster is contacted.
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" test openbao helm > "$test_root/hook.log" 2>&1; then
        echo 'Hook unexpectedly succeeded against the rejecting fake API' >&2
        exit 1
    fi
    grep -q 'jwker available' "$test_root/hook.log"
    test -s "$KUBECTL_CALLS"
    if grep -q 'helm check failed' "$test_root/hook.log"; then
        echo 'Hook mode still requires Helm' >&2
        exit 1
    fi

    rm "$bin_dir/jwker"
    : > "$KUBECTL_CALLS"
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" test openbao helm > "$test_root/missing-jwker.log" 2>&1; then
        echo 'Hook unexpectedly succeeded without jwker' >&2
        exit 1
    fi
    grep -q 'jwker is not available' "$test_root/missing-jwker.log"
    test ! -s "$KUBECTL_CALLS"
done

echo 'OpenBao initialization tool requirements passed'
