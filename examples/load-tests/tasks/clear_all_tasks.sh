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
set -e

# take in a command argument for the org id
ORG_ID=$1

# check to see if the org id is provided
if [ -z "$ORG_ID" ]; then
    echo "org id is required"
    exit 1
fi

# take in a command argument for the number of threads to use
THREADS=$2

# default to 16 threads if not provided
if [ -z "$THREADS" ]; then
    THREADS=16
fi

# check to see if the ngc cli is installed
if ! command -v ngc &> /dev/null
then
    echo "ngc cli could not be found - please install it from https://org.ngc.nvidia.com/setup/installers/cli"
    exit 1
fi

# prompt the user to confirm the action
read -p "Are you sure you want to clear all tasks for org $ORG_ID? (y/n): " CONFIRM
if [ "$CONFIRM" != "y" ]; then
    echo "Aborting..."
    exit 1
fi

# get the number of tasks in the org
TASKS=$(ngc cf task ls --org $ORG_ID --format_type json)
TASK_IDS=$(echo "$TASKS" | jq -r '.[] | .id')
TOTAL_TASKS=$(echo "$TASKS" | jq '. | length')

echo "Found $TOTAL_TASKS tasks to delete using $THREADS parallel threads"

# delete tasks in parallel using xargs
echo "$TASK_IDS" | xargs -I {} -P $THREADS -n 1 bash -c '
    echo "Deleting task $1"
    ngc cf task rm --org '$ORG_ID' $1
' _ {}

DELETED_COUNT=$TOTAL_TASKS

# confirm there are no tasks left
NEW_TASKS=$(ngc cf task ls --org $ORG_ID --format_type json)
if [ "$(echo "$NEW_TASKS" | jq '. | length')" -gt 0 ]; then
    echo "There are still $(echo "$NEW_TASKS" | jq '. | length') tasks left in org $ORG_ID"
    exit 1
fi

# print the number of tasks deleted
echo "Deleted $DELETED_COUNT tasks from org $ORG_ID"

# print a success message
echo "All tasks cleared for org $ORG_ID"
