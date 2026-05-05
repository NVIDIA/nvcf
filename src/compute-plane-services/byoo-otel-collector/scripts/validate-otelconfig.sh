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

OTEL_COLLECTOR_VERSION=${OTEL_COLLECTOR_VERSION:-"968b0f9f"}

set -euo pipefail

export ESS_SECRETS_PATH="$(pwd)/examples/secrets"
mkdir -p _output

if command -v ./_output/bin/otelcol-contrib > /dev/null 2>&1; then
    echo "No need to download"
else
    # Set NGC CLI environment variables
    if [ -z "${NGC_CLI_API_KEY:-}" ]; then
        echo "Error: NGC_CLI_API_KEY environment variable for production is required"
        exit 1
    fi
    # Set NGC CLI environment variables
    export NGC_CLI_ORG=qtfpt1h0bieu
    export NGC_CLI_TEAM=nvcf-core
    echo "Downloading otelcol-contrib from NGC otelcol-contrib:${OTEL_COLLECTOR_VERSION}"
    mkdir -p _output/bin
    otel_path=$(ngc registry resource download-version qtfpt1h0bieu/nvcf-core/otelcol-contrib:${OTEL_COLLECTOR_VERSION} | grep Downloaded | grep -o '/.*$') # Download otelcol-contrib from NGC
    mv ${otel_path}/otelcol-contrib _output/bin/
    chmod +x _output/bin/otelcol-contrib
fi

validate_otel_config() {
    local config_path="$1"

    # Temporarily disable automatic exit on error
    set +e
    timeout_output=$(timeout 2 ./_output/bin/otelcol-contrib --config="$config_path" 2>&1)
    exit_code=$?
    # Re-enable automatic exit on error
    set -e

    # Analyze exit code and output
    if [[ $exit_code -eq 0 || $exit_code -eq 124 || $exit_code -eq 143 ]]; then
        # normal startup terminated by timeout
        echo "Configuration check passed"
    else
        echo "Unknown error (exit code: $exit_code)"
        echo "$timeout_output"
        return 1
    fi
}

####### GFN tests

# Since in-cluster files won't exist in local tests,
# replace them in embedded configs with local paths.
mkdir -p _output/test/
touch _output/test/token

for input_file in testdata/*.json; do
    if [ -f "$input_file" ]; then
        echo "=== Test $input_file ==="

        ### Task
        echo "Generating configs for GFN container task..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs vm container task
        sed -i 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' _output/otelconfigs/config.task_vm_container.yaml

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_vm_container.yaml
        validate_otel_config _output/otelconfigs/config.task_vm_container.yaml
        rm _output/otelconfigs/config.task_vm_container.yaml

        ### Function
        echo "Generating configs for GFN container function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs vm container function
        sed -i 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' _output/otelconfigs/config.function_vm_container.yaml

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_vm_container.yaml
        # validate_otel_config _output/otelconfigs/config.function_vm_container.yaml
        # rm _output/otelconfigs/config.function_vm_container.yaml
    fi
done

rm -rf _output/test
rm -rf _output/otelconfigs

# Since in-cluster files won't exist in local tests,
# replace them in embedded configs with local paths.
mkdir -p _output/test/
cp internal/otelconfig/templates/config-vm-helm.yaml.tmpl internal/otelconfig/templates/config-vm-helm.yaml.tmpl.bk
sed -i 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' internal/otelconfig/templates/config-vm-helm.yaml.tmpl
touch _output/test/token

for input_file in testdata/*.json; do
    if [ -f "$input_file" ]; then
        echo "=== Test $input_file ==="

        echo "Generating configs for GFN helm function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs vm helm task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_vm_helm.yaml
        validate_otel_config _output/otelconfigs/config.task_vm_helm.yaml
        rm _output/otelconfigs/config.task_vm_helm.yaml

        echo "Generating configs for GFN helm function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs vm helm function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_vm_helm.yaml
        validate_otel_config _output/otelconfigs/config.function_vm_helm.yaml
        rm _output/otelconfigs/config.function_vm_helm.yaml
    fi
done

mv internal/otelconfig/templates/config-vm-helm.yaml.tmpl.bk internal/otelconfig/templates/config-vm-helm.yaml.tmpl

####### NON-GFN tests

# Since in-cluster files won't exist in local tests,
# replace them in embedded configs with local paths.
mkdir -p _output/test/
cp internal/otelconfig/templates/config-k8s-container.yaml.tmpl internal/otelconfig/templates/config-k8s-container.yaml.tmpl.bk
sed -i 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' internal/otelconfig/templates/config-k8s-container.yaml.tmpl
touch _output/test/token

for input_file in testdata/*.json; do
    if [ -f "$input_file" ]; then
        echo "=== Test $input_file ==="

        echo "Generating configs for non-GFN container function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs k8s container task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_k8s_container.yaml
        validate_otel_config _output/otelconfigs/config.task_k8s_container.yaml
        rm _output/otelconfigs/config.task_k8s_container.yaml

        echo "Generating configs for non-GFN helm function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs k8s container function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_k8s_container.yaml
        validate_otel_config _output/otelconfigs/config.function_k8s_container.yaml
        rm _output/otelconfigs/config.function_k8s_container.yaml
    fi
done

mv internal/otelconfig/templates/config-k8s-container.yaml.tmpl.bk internal/otelconfig/templates/config-k8s-container.yaml.tmpl

rm -rf _output/test
rm -rf _output/otelconfigs

# Since in-cluster files won't exist in local tests,
# replace them in embedded configs with local paths.
mkdir -p _output/test/
cp internal/otelconfig/templates/config-k8s-helm.yaml.tmpl internal/otelconfig/templates/config-k8s-helm.yaml.tmpl.bk
sed -i 's@/var/run/secrets/kubernetes.io/serviceaccount/@_output/test/@g' internal/otelconfig/templates/config-k8s-helm.yaml.tmpl
touch _output/test/token

for input_file in testdata/*.json; do
    if [ -f "$input_file" ]; then
        echo "=== Test $input_file ==="

        echo "Generating configs for non-GFN helm function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs k8s helm task

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.task_k8s_helm.yaml
        validate_otel_config _output/otelconfigs/config.task_k8s_helm.yaml
        rm _output/otelconfigs/config.task_k8s_helm.yaml

        echo "Generating configs for non-GFN helm function..."
        go run scripts/generate-otelconfig.go $input_file _output/otelconfigs k8s helm function

        echo "Validate configs ..."
        ./_output/bin/otelcol-contrib validate --config=_output/otelconfigs/config.function_k8s_helm.yaml
        validate_otel_config _output/otelconfigs/config.function_k8s_helm.yaml
        rm _output/otelconfigs/config.function_k8s_helm.yaml
    fi
done

mv internal/otelconfig/templates/config-k8s-helm.yaml.tmpl.bk internal/otelconfig/templates/config-k8s-helm.yaml.tmpl

rm -rf _output/test
rm -rf _output/otelconfigs
