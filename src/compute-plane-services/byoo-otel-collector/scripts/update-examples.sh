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

set -euo pipefail

export ESS_SECRETS_PATH="$(pwd)/examples/secrets"

local_secrets_path="$(pwd)/examples/secrets"
real_secrets_path="/etc/byoo-otel-collector/secrets"

mkdir -p _output
for input_file in testdata/*.json; do
    echo "=== Test $input_file ==="
    basename=$(basename "${input_file}" .json)
    for backend_type in vm k8s; do
        for compute_type in task function; do
            for req_type in container helm; do
                echo "Generating configs backend_type=${backend_type} request_type=${req_type} compute_type=${compute_type}..."
                go run scripts/generate-otelconfig.go $input_file _output/otelconfigs ${backend_type} ${req_type} ${compute_type}
                generated_file=$(ls _output/otelconfigs/config.${compute_type}_${backend_type}_${req_type}.yaml)
                cp $generated_file examples/otelconfigs/${backend_type}/config_${compute_type}_${req_type}_${basename}.yaml
                if [[ "$(uname -s)" == "Darwin" ]]; then
                    sed -i "" "s|${local_secrets_path}|${real_secrets_path}|g" examples/otelconfigs/${backend_type}/config_${compute_type}_${req_type}_${basename}.yaml
                else
                    sed -i "s|${local_secrets_path}|${real_secrets_path}|g" examples/otelconfigs/${backend_type}/config_${compute_type}_${req_type}_${basename}.yaml
                fi
                rm $generated_file
            done
        done
    done
done
