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


set -euo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/utils/utils.sh" ]; then
    echo "Error: log.sh not found in ${SCRIPT_DIR}/utils/utils.sh"
    return 1
else
  source "${SCRIPT_DIR}/utils/utils.sh"
fi
if [ -f "${SCRIPT_DIR}/utils/functions.sh" ]; then
    source "${SCRIPT_DIR}/utils/functions.sh"
else
    echo "Error: functions.sh not found in ${SCRIPT_DIR}/utils/functions.sh"
    return 1
fi

mount_path="jwt"

#-------------------------------------------
# Enable the JWT auth mount
#-------------------------------------------
enable_auth_mount "$mount_path" "jwt"

#-------------------------------------------
# Configure the JWT auth engine
#-------------------------------------------
configure_auth_jwt
