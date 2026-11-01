#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# Include guard: Ensures this script's contents are only processed once.
if [[ -n "${_UTILS_SH_SOURCED:-}" ]]; then
  return 0
fi
readonly _UTILS_SH_SOURCED=1

# Colors
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly BLUE='\033[0;34m'
readonly NC='\033[0m' # No Color

# Timestamp format
timestamp() {
    date '+%Y-%m-%d %H:%M:%S'
}

# Log levels with emojis
log_info() {
    echo -e "${GREEN}[INFO]${NC} ℹ️  $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} ⚠️  $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} ❌ $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} 🚀 $1"
}

log_success() {
    echo -e "${GREEN}[DONE]${NC} ✅ $1"
}

# Section separator (no timestamp)
log_section() {
    echo -e "\n${BLUE}=================================${NC}"
    echo -e "${BLUE}    $1${NC}"
    echo -e "${BLUE}=================================${NC}\n"
}
