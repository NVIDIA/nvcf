# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Turns the recipe charts' published catalog (index.json, from
# deploy/helm/llm-routing/recipes in NVIDIA/nvcf) into the UI's ConfigMap
# content, adding what the export doesn't carry yet:
# - each chart's OCI repository, which the deploy snippets install from
#   (default oci://nvcr.io/org, a placeholder until the charts are published);
# - a one-line description per model, for its card (the UI mocks' copy);
# and standardizes the runtime's version: the export gives runtimes built from
# source a git `revision` instead, which becomes `version` ("llama.cpp f872b59").
# Values the export already has are kept.
#
#   jq -f helm/recipes/catalog.jq index.json > recipes.json
#   jq --arg repository oci://registry.example.com/recipes -f helm/recipes/catalog.jq index.json

{
  "qwen3.8-27b": "Instruction-tuned, 27B parameters",
  "qwen3.8-27b-nvfp4": "Instruction-tuned, 27B parameters",
  "qwen3.8-flash-next": "Preview build",
  "glm-5.3": "General reasoning, long context",
  "nemotron-5-nano-12b": "Compact reasoning model",
  "nemotron-5-super-49b": "49B parameters, agentic tasks",
  "deepseek-v4-flash": "Low-latency serving build"
} as $descriptions
| ($ARGS.named.repository // "oci://nvcr.io/org") as $repository
| .recipes |= map(
    (if .description == null and $descriptions[.id] != null
     then .description = $descriptions[.id] else . end)
    | .runtime |= (
        if . != null and .version == null and .revision != null
        then .version = "\(.backend) \(.revision[0:7])" | del(.revision)
        else . end
      )
    | .profiles |= map(
        .deployment.chart.repository = (.deployment.chart.repository // $repository)
      )
  )
