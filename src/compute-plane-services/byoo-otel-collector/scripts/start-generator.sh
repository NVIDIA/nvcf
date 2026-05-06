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


PROJECT_ROOT=$(pwd)
cd generator
uv sync
SOURCE_CONFIG=${PROJECT_ROOT}/generator/source-config.yaml

DOC_DIR="${PROJECT_ROOT}/generator/doc"
GEN_DIR="${PROJECT_ROOT}/generator/gen"
CONFIG_TEMPLATE_DIR="${PROJECT_ROOT}/internal/otelconfig/templates"
CMD=(uv run -m generator -c "${SOURCE_CONFIG}" -do "${DOC_DIR}" -to "${GEN_DIR}")
echo "${CMD[@]}"
${CMD[@]}
for file in $(ls "${CONFIG_TEMPLATE_DIR}/"); do
    echo "overwriting ${file} with ${GEN_DIR}/generated_src-${file}"
    cp "${GEN_DIR}/generated_src-${file}" "${CONFIG_TEMPLATE_DIR}/${file}"
done
