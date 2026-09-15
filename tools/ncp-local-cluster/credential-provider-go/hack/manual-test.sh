#!/bin/bash
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

# manual-test.sh: A script for manually testing the generic-credential-provider

set -euo pipefail # Fail fast

PROVIDER_BINARY="../../bin/generic-credential-provider" # Relative to script location (hack/)
TEMP_CONFIG_FILE="./test_config.json" # Created in the current directory (hack/)

# Function to base64 encode username:password (cross-platform)
# Usage: b64_auth "user" "pass"
function b64_auth() {
    local user_pass="${1:-}:${2:-}"
    if [[ "$(uname)" == "Darwin" ]]; then
        echo -n "${user_pass}" | base64
    else
        echo -n "${user_pass}" | base64 -w 0
    fi
}

function run_test_case() {
    local test_name="$1"
    local description="$2"
    local config_content="$3"
    local request_json="$4"

    echo "================================================================================"
    echo "Test Case: ${test_name}"
    echo "Description: ${description}"
    echo "--------------------------------------------------------------------------------"
    echo "Input Request JSON:"
    echo "${request_json}"
    echo "--------------------------------------------------------------------------------"

    # Create temp config file
    if [[ "${config_content}" == "DO_NOT_CREATE" ]]; then
        echo "Config File: (Intentionally not created for this test)"
        rm -f "${TEMP_CONFIG_FILE}" # Ensure it doesn't exist
    else
        echo "Config File Content (${TEMP_CONFIG_FILE}):"
        printf '%s' "${config_content}" > "${TEMP_CONFIG_FILE}"
        cat "${TEMP_CONFIG_FILE}"
        echo
        echo "--------------------------------------------------------------------------------"
    fi

    echo "Executing command:"
    echo "echo '${request_json}' | ${PROVIDER_BINARY} get-credentials --config-file ${TEMP_CONFIG_FILE}"
    echo "--------------------------------------------------------------------------------"

    ERROR_OUTPUT_FILE=$(mktemp)
    CMD_OUTPUT=""
    EXIT_CODE=0

    set +e
    CMD_OUTPUT=$(echo "${request_json}" | "${PROVIDER_BINARY}" get-credentials --config-file "${TEMP_CONFIG_FILE}" 2>"${ERROR_OUTPUT_FILE}")
    EXIT_CODE=$?
    set -e

    echo "STDOUT:"
    if [[ -n "${CMD_OUTPUT}" ]]; then
        echo "${CMD_OUTPUT}"
    else
        echo "(empty)"
    fi
    echo "--------------------------------------------------------------------------------"
    echo "STDERR:"
    if [ -s "${ERROR_OUTPUT_FILE}" ]; then
        cat "${ERROR_OUTPUT_FILE}"
    else
        echo "(empty)"
    fi
    rm -f "${ERROR_OUTPUT_FILE}"
    echo "--------------------------------------------------------------------------------"
    echo "EXIT CODE: ${EXIT_CODE}"
    echo "================================================================================"
    echo ""

    if [[ "${config_content}" != "DO_NOT_CREATE" ]]; then
        rm -f "${TEMP_CONFIG_FILE}"
    fi
}

# --- Test Scenario Definitions ---

function test_scenario_1_nvcr_path_specific() {
    local name="NVCR Path Specific (repository)"
    local description="Tests successful lookup for nvcr.io/ngc-org/ngc-team/repository"
    local config_content
    config_content=$(cat <<-END_CONFIG
{
    "auths": {
        "nvcr.io/ngc-org/ngc-team/repository": { "auth": "$(b64_auth repouser repopass)" },
        "nvcr.io": { "auth": "$(b64_auth nvcrdefault nvcrdefaultpass)" }
    }
}
END_CONFIG
)
    local request_json='{"image": "nvcr.io/ngc-org/ngc-team/repository:latest"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_2_nvcr_host_fallback() {
    local name="NVCR Host Fallback"
    local description="Tests fallback to nvcr.io host when path nvcr.io/unknown/path is not found"
    local config_content
    config_content=$(cat <<-END_CONFIG
{
    "auths": {
        "nvcr.io/ngc-org/ngc-team/repository": { "auth": "$(b64_auth repouser repopass)" },
        "nvcr.io": { "auth": "$(b64_auth nvcrdefault nvcrdefaultpass)" }
    }
}
END_CONFIG
)
    local request_json='{"image": "nvcr.io/unknown/path:tag"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_3_docker_hub_library() {
    local name="Docker Hub Library Image (ubuntu)"
    local description="Tests lookup for official 'ubuntu' image (index.docker.io/library/ubuntu)"
    local config_content
    config_content=$(cat <<-END_CONFIG
{
    "auths": {
        "index.docker.io/library/ubuntu": { "auth": "$(b64_auth ubuntuuser ubuntupass)" },
        "docker.io": { "auth": "$(b64_auth dockerdefault dockerdefaultpass)" }
    }
}
END_CONFIG
)
    local request_json='{"image": "ubuntu:latest"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_4_docker_hub_user_image() {
    local name="Docker Hub User Image (myuser/myimage)"
    local description="Tests lookup for user image 'myuser/myimage' (config has docker.io/myuser/myimage)"
    local config_content
    config_content=$(cat <<-END_CONFIG
{
    "auths": {
        "docker.io/myuser/myimage": { "auth": "$(b64_auth myuser myuserpass)" },
        "docker.io": { "auth": "$(b64_auth dockerdefault dockerdefaultpass)" }
    }
}
END_CONFIG
)
    local request_json='{"image": "myuser/myimage:v1"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_5_no_creds_unknown_registry() {
    local name="No Credentials Found (Unknown Registry)"
    local description="Tests behavior when no matching credentials exist"
    local config_content
    config_content=$(cat <<-END_CONFIG
{
    "auths": {
        "nvcr.io": { "auth": "$(b64_auth nvcrdefault nvcrdefaultpass)" }
    }
}
END_CONFIG
)
    local request_json='{"image": "unknown.registry.com/foo/bar:tag"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_6_malformed_request_json() {
    local name="Malformed Request JSON"
    local description="Tests provider handling of malformed input JSON from stdin"
    local config_content='{"auths":{}}'
    local request_json='{"image": "nvcr.io/ngc-org/ngc-team/repository:latest"' # Missing closing brace
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_7_config_file_not_found() {
    local name="Config File Not Found"
    local description="Tests provider handling when --config-file points to non-existent file"
    local config_content="DO_NOT_CREATE"
    local request_json='{"image": "nvcr.io/ngc-org/ngc-team/repository:latest"}'
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

function test_scenario_8_empty_stdin_request() {
    local name="Empty Stdin Request"
    local description="Tests provider handling of empty stdin (should error in main.go)"
    local config_content='{"auths":{}}'
    local request_json=''
    run_test_case "${name}" "${description}" "${config_content}" "${request_json}"
}

# --- Script Main Logic ---

if [ ! -f "${PROVIDER_BINARY}" ]; then
    echo "Error: Provider binary not found at ${PROVIDER_BINARY}"
    echo "Please build the provider first (e.g., from the project root: make build)"
    exit 1
fi

# Execute all test case functions
test_scenario_1_nvcr_path_specific
test_scenario_2_nvcr_host_fallback
test_scenario_3_docker_hub_library
test_scenario_4_docker_hub_user_image
test_scenario_5_no_creds_unknown_registry
test_scenario_6_malformed_request_json
test_scenario_7_config_file_not_found
test_scenario_8_empty_stdin_request

echo "Manual test script finished." 
