#!/usr/bin/env bash
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

CLUSTER_NAME="ncp-local"

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

if ! k3d cluster get "$CLUSTER_NAME" &> /dev/null; then
    log_info "Cluster '$CLUSTER_NAME' does not exist. Nothing to tear down."
    exit 0
fi

HELMFILE_ENV="${1:-}"
if command -v helmfile &> /dev/null && [ -n "$HELMFILE_ENV" ]; then
    log_info "Running helmfile destroy with HELMFILE_ENV=$HELMFILE_ENV..."
    HELMFILE_ENV="$HELMFILE_ENV" helmfile destroy 2>/dev/null || log_info "No helmfile state found or helmfile destroy skipped."
elif command -v helmfile &> /dev/null; then
    log_info "Skipping helmfile destroy (pass your environment name as argument: ./teardown.sh <name>)"
fi

log_info "Deleting k3d cluster '$CLUSTER_NAME'..."
k3d cluster delete "$CLUSTER_NAME"
log_info "Cluster '$CLUSTER_NAME' deleted."
