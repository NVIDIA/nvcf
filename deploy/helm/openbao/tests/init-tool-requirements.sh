#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

# Exercise the scripts and invocation shipped in the chart package.
chart_dir="$test_root/chart"
cp -R "$project_dir/helm" "$chart_dir"
helm dependency build "$chart_dir" >/dev/null
helm package "$chart_dir" --destination "$test_root" >/dev/null
rendered="$test_root/rendered.yaml"
helm template openbao "$test_root"/*.tgz --namespace test \
    -f "$project_dir/../../../tools/ci/helm-validate-values/openbao.yaml" > "$rendered"
init_job='select(.kind == "Job" and .metadata.name == "openbao-server-initialize-cluster")'
test "$(yq -r "$init_job | .metadata.annotations.\"helm.sh/hook\"" "$rendered")" = post-install
test "$(yq -r "$init_job | .spec.template.spec.containers[0].command[0]" "$rendered")" = /bin/bash
test "$(yq -r "$init_job | .spec.template.spec.containers[0].args[0]" "$rendered")" = -c
read -r -a chart_command <<< "$(yq -r "$init_job | .spec.template.spec.containers[0].args[1]" "$rendered")"
test "${chart_command[0]}" = /scripts/deploy.sh

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
    hook_args=(test openbao helm)
    if [ -f "$source_dir/utils/utils.sh" ]; then
        cp "$source_dir/deploy.sh" "$stage/deploy.sh"
        cp "$source_dir/utils/utils.sh" "$source_dir/utils/log.sh" "$stage/utils/"
        mkdir -p "$stage/helm/scripts/utils"
        cp "$project_dir/helm/scripts/utils/jwt-plugin.sh" "$stage/helm/scripts/utils/"
    else
        yq -r 'select(.kind == "ConfigMap" and .metadata.name == "openbao-server-init-script") | .data."deploy.sh"' "$rendered" > "$stage/deploy.sh"
        for script in utils log jwt-plugin; do
            yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"openbao-server-utils-script\") | .data.\"$script.sh\"" "$rendered" > "$stage/utils/$script.sh"
        done
        hook_args=("${chart_command[@]:1}")
    fi
    ln -sf "$(type -P true)" "$bin_dir/jwker"
    : > "$KUBECTL_CALLS"
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" test openbao script > "$test_root/script.log" 2>&1; then
        echo 'Standalone mode unexpectedly succeeded without Helm' >&2
        exit 1
    fi
    grep -q 'helm check failed' "$test_root/script.log"
    test ! -s "$KUBECTL_CALLS"

    # This checks prerequisites, not successful initialization. The fake API
    # rejects later operations, so no cluster is contacted.
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" "${hook_args[@]}" > "$test_root/hook.log" 2>&1; then
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
    if PATH="$bin_dir" "$BASH" "$stage/deploy.sh" "${hook_args[@]}" > "$test_root/missing-jwker.log" 2>&1; then
        echo 'Hook unexpectedly succeeded without jwker' >&2
        exit 1
    fi
    grep -q 'jwker is not available' "$test_root/missing-jwker.log"
    test ! -s "$KUBECTL_CALLS"
done

echo 'OpenBao initialization tool requirements passed'
