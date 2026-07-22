#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

OTEL_COLLECTOR_VERSION=${OTEL_COLLECTOR_VERSION:-}

set -euo pipefail

MODE="${1:-all}"

export ESS_SECRETS_PATH="$(pwd)/examples/secrets"
mkdir -p _output

ensure_otelcol_binary() {
    if [ -x ./_output/bin/otelcol-contrib ]; then
        echo "No need to download"
        return
    fi

    mkdir -p _output/bin
    if [ -z "${OTEL_COLLECTOR_VERSION:-}" ]; then
        echo "Building otelcol-contrib from checked-in otelcol module"
        GOWORK=off CGO_ENABLED="${CGO_ENABLED:-0}" GOMAXPROCS="${GOMAXPROCS:-2}" \
            go -C otelcol build -trimpath -o ../_output/bin/otelcol-contrib .
        return
    fi

    if [ -z "${NGC_CLI_API_KEY:-}" ]; then
        echo "Error: NGC_CLI_API_KEY environment variable for production is required"
        exit 1
    fi

    export NGC_CLI_ORG=qtfpt1h0bieu
    export NGC_CLI_TEAM=nvcf-core
    echo "Downloading otelcol-contrib from NGC otelcol-contrib:${OTEL_COLLECTOR_VERSION}"
    otel_path=$(ngc registry resource download-version qtfpt1h0bieu/nvcf-core/otelcol-contrib:${OTEL_COLLECTOR_VERSION} | grep Downloaded | grep -o '/.*$')
    mv "${otel_path}/otelcol-contrib" _output/bin/
    chmod +x _output/bin/otelcol-contrib
}

run_with_timeout() {
    local seconds="$1"
    shift

    if command -v timeout >/dev/null 2>&1; then
        timeout "${seconds}" "$@"
        return
    fi
    if command -v gtimeout >/dev/null 2>&1; then
        gtimeout "${seconds}" "$@"
        return
    fi

    "$@" &
    local pid=$!
    (
        sleep "${seconds}"
        kill -TERM "${pid}" 2>/dev/null
        sleep 1
        kill -KILL "${pid}" 2>/dev/null
    ) &
    local timer=$!

    wait "${pid}"
    local exit_code=$?
    kill "${timer}" 2>/dev/null || true
    wait "${timer}" 2>/dev/null || true
    return "${exit_code}"
}

validate_otel_config() {
    local config_path="$1"

    if [[ "$(uname -s)" != "Linux" ]]; then
        echo "Runtime startup validation skipped on non-Linux host"
        return
    fi

    set +e
    timeout_output=$(run_with_timeout 2 ./_output/bin/otelcol-contrib --config="$config_path" 2>&1)
    exit_code=$?
    set -e

    if [[ $exit_code -eq 0 || $exit_code -eq 124 || $exit_code -eq 143 ]]; then
        echo "Configuration check passed"
    else
        echo "Unknown error (exit code: $exit_code)"
        echo "$timeout_output"
        return 1
    fi
}

reset_runtime_dirs() {
    rm -rf _output/test _output/otelconfigs
    mkdir -p _output/test
    touch _output/test/token
}

patch_template() {
    local template_path="$1"
    cp "${template_path}" "${template_path}.bk"
    perl -0pi -e 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' "${template_path}"
}

restore_template() {
    local template_path="$1"
    mv "${template_path}.bk" "${template_path}"
}

run_vm_container() {
    reset_runtime_dirs

    for input_file in testdata/*.json; do
        [ -f "$input_file" ] || continue
        echo "=== Test $input_file ==="

        echo "Generating configs for GFN container task..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs vm container task
        perl -0pi -e 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' _output/otelconfigs/config.task_vm_container.yaml

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_vm_container.yaml
        validate_otel_config _output/otelconfigs/config.task_vm_container.yaml
        rm _output/otelconfigs/config.task_vm_container.yaml

        echo "Generating configs for GFN container function..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs vm container function
        perl -0pi -e 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' _output/otelconfigs/config.function_vm_container.yaml

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_vm_container.yaml
        # Runtime startup validation is intentionally skipped here to
        # preserve the existing CI behavior for this config shape.
    done
}

run_vm_helm() {
    local template_path="internal/otelconfig/templates/config-vm-helm.yaml.tmpl"

    reset_runtime_dirs
    patch_template "${template_path}"

    for input_file in testdata/*.json; do
        [ -f "$input_file" ] || continue
        echo "=== Test $input_file ==="

        echo "Generating configs for GFN helm task..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs vm helm task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_vm_helm.yaml
        validate_otel_config _output/otelconfigs/config.task_vm_helm.yaml
        rm _output/otelconfigs/config.task_vm_helm.yaml

        echo "Generating configs for GFN helm function..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs vm helm function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_vm_helm.yaml
        validate_otel_config _output/otelconfigs/config.function_vm_helm.yaml
        rm _output/otelconfigs/config.function_vm_helm.yaml
    done

    restore_template "${template_path}"
}

run_k8s_container() {
    local template_path="internal/otelconfig/templates/config-k8s-container.yaml.tmpl"

    reset_runtime_dirs
    patch_template "${template_path}"

    for input_file in testdata/*.json; do
        [ -f "$input_file" ] || continue
        echo "=== Test $input_file ==="

        echo "Generating configs for non-GFN container task..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs k8s container task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_k8s_container.yaml
        validate_otel_config _output/otelconfigs/config.task_k8s_container.yaml
        rm _output/otelconfigs/config.task_k8s_container.yaml

        echo "Generating configs for non-GFN container function..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs k8s container function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_k8s_container.yaml
        validate_otel_config _output/otelconfigs/config.function_k8s_container.yaml
        rm _output/otelconfigs/config.function_k8s_container.yaml
    done

    restore_template "${template_path}"
}

run_k8s_helm() {
    local template_path="internal/otelconfig/templates/config-k8s-helm.yaml.tmpl"

    reset_runtime_dirs
    patch_template "${template_path}"

    for input_file in testdata/*.json; do
        [ -f "$input_file" ] || continue
        echo "=== Test $input_file ==="

        echo "Generating configs for non-GFN helm task..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs k8s helm task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_k8s_helm.yaml
        validate_otel_config _output/otelconfigs/config.task_k8s_helm.yaml
        rm _output/otelconfigs/config.task_k8s_helm.yaml

        echo "Generating configs for non-GFN helm function..."
        go run scripts/generate-otelconfig.go "$input_file" _output/otelconfigs k8s helm function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_k8s_helm.yaml
        validate_otel_config _output/otelconfigs/config.function_k8s_helm.yaml
        rm _output/otelconfigs/config.function_k8s_helm.yaml
    done

    restore_template "${template_path}"
}

ensure_otelcol_binary

case "${MODE}" in
    all)
        run_vm_container
        run_vm_helm
        run_k8s_container
        run_k8s_helm
        ;;
    vm-container)
        run_vm_container
        ;;
    vm-helm)
        run_vm_helm
        ;;
    k8s-container)
        run_k8s_container
        ;;
    k8s-helm)
        run_k8s_helm
        ;;
    *)
        echo "Usage: $0 [all|vm-container|vm-helm|k8s-container|k8s-helm]" >&2
        exit 1
        ;;
esac

rm -rf _output/test _output/otelconfigs
