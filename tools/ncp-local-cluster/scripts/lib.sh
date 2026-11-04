#!/usr/bin/env bash
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

# This script contains shared utility functions for logging and other common tasks
# to be sourced by other scripts in this directory.

# --- Colors for Output ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# --- Logging Functions ---

# log_info prints an informational message in green.
# Usage: log_info "This is an info message"
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

# log_warn prints a warning message in yellow.
# Usage: log_warn "This is a warning"
log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

# log_error prints an error message in red.
# Usage: log_error "This is an error"
log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}
