#! /bin/bash
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

# take in one command argument for the org id
ORG_ID=${1:-}

# check to see if the org id is provided
if [ -z "$ORG_ID" ]; then
    echo "org id is required"
    exit 1
fi

# check to see if the ngc cli is installed
if ! command -v ngc &> /dev/null
then
    echo "ngc cli could not be found - please install it from https://org.ngc.nvidia.com/setup/installers/cli"
    exit 1
fi

# get the number of tasks in the org in json format
echo "Getting tasks for org $ORG_ID"
TASKS=$(ngc cf task ls --org $ORG_ID --format_type json)

# print the number of tasks
echo "Total number of tasks in org $ORG_ID: $(echo "$TASKS" | jq '. | length')"

# print the number of tasks in each status
echo "Number of tasks in each status:"
echo "$TASKS" | jq -r '. | group_by(.status) | map({status: .[0].status, count: length})[] | "> \(.status): \(.count)"'
