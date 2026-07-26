#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

NVCF_CLI_BIN=${NVCF_CLI_BIN:-nvcf-cli}
WORKFLOW_ID=${WORKFLOW_ID:-$(date -u +%Y%m%d%H%M%S)}
WORKFLOW_MESSAGE=${WORKFLOW_MESSAGE:-function-task-pipeline}
GPU=${GPU:-H100}
INSTANCE_TYPE=${INSTANCE_TYPE:-NCP.GPU.H100_8x}
BACKEND=${BACKEND:-ncp-local}
REGIONS=${REGIONS:-us-west-1}
FUNCTION_DEPLOY_TIMEOUT_SECONDS=${FUNCTION_DEPLOY_TIMEOUT_SECONDS:-900}
POLL_INTERVAL_SECONDS=${POLL_INTERVAL_SECONDS:-10}
TASK_TIMEOUT_SECONDS=${TASK_TIMEOUT_SECONDS:-900}
KEEP_RESOURCES=${KEEP_RESOURCES:-false}

function_id=
version_id=
task_id=
function_created=false
function_deployed=false
task_terminal=false

log() {
    printf '[function-task-pipeline] %s\n' "$*" >&2
}

fail() {
    log "ERROR: $*"
    return 1
}

require_env() {
    local name=$1
    if [[ -z ${!name:-} ]]; then
        fail "${name} is required"
    fi
}

cli() {
    "$NVCF_CLI_BIN" --config "$NVCF_CLI_CONFIG" "$@"
}

cleanup() {
    local result=$?
    trap - EXIT

    if [[ $KEEP_RESOURCES == true ]]; then
        log "Keeping workflow resources"
        exit "$result"
    fi

    set +e
    if [[ -n $task_id ]]; then
        if [[ $task_terminal != true ]]; then
            log "Canceling task ${task_id}"
            cli task cancel "$task_id" >&2
        fi
        log "Deleting task ${task_id}"
        cli task delete "$task_id" >&2
    fi

    if [[ $function_deployed == true ]]; then
        log "Removing function deployment ${function_id}/${version_id}"
        cli function deploy remove \
            --function-id "$function_id" \
            --version-id "$version_id" >&2
    fi

    if [[ $function_created == true ]]; then
        log "Deleting function ${function_id}/${version_id}"
        cli function delete "$function_id" "$version_id" >&2
    fi
    exit "$result"
}

trap cleanup EXIT
trap 'exit 130' INT TERM

require_env NVCF_CLI_CONFIG
require_env FUNCTION_IMAGE
require_env TASK_IMAGE

if [[ ! -f $NVCF_CLI_CONFIG ]]; then
    fail "NVCF_CLI_CONFIG does not exist: ${NVCF_CLI_CONFIG}"
fi
if ! command -v "$NVCF_CLI_BIN" >/dev/null 2>&1; then
    fail "NVCF CLI is not available: ${NVCF_CLI_BIN}"
fi
if ! command -v jq >/dev/null 2>&1; then
    fail "jq is required"
fi
if [[ ! $WORKFLOW_ID =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]]; then
    fail "WORKFLOW_ID must match ^[A-Za-z0-9][A-Za-z0-9_-]*$"
fi
if [[ ! $POLL_INTERVAL_SECONDS =~ ^[0-9]+$ ]]; then
    fail "POLL_INTERVAL_SECONDS must be a non-negative integer"
fi
if [[ ! $FUNCTION_DEPLOY_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "FUNCTION_DEPLOY_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ ! $TASK_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "TASK_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ $KEEP_RESOURCES != true && $KEEP_RESOURCES != false ]]; then
    fail "KEEP_RESOURCES must be true or false"
fi

function_name="function-task-${WORKFLOW_ID}"
task_name="task-${WORKFLOW_ID}"

log "Creating function ${function_name}"
cli function create \
    --name "$function_name" \
    --image "$FUNCTION_IMAGE" \
    --inference-url /echo \
    --inference-port 8000 \
    --health-uri /health \
    --health-port 8000 >&2

status_json=$(cli --json status)
function_id=$(jq -er '.currentFunction.functionId' <<<"$status_json")
version_id=$(jq -er '.currentFunction.versionId' <<<"$status_json")
function_created=true

log "Deploying function ${function_id}/${version_id}"
function_deployed=true
cli function deploy create \
    --function-id "$function_id" \
    --version-id "$version_id" \
    --gpu "$GPU" \
    --instance-type "$INSTANCE_TYPE" \
    --backend "$BACKEND" \
    --regions "$REGIONS" \
    --min-instances 1 \
    --max-instances 1 \
    --timeout "$FUNCTION_DEPLOY_TIMEOUT_SECONDS" >&2

admission_body=$(jq -cn \
    --arg message "$WORKFLOW_MESSAGE" \
    '{message: $message, repeats: 1}')

log "Invoking the interactive admission stage"
admission_output=$(cli --json function invoke \
    --function-id "$function_id" \
    --version-id "$version_id" \
    --request-body "$admission_body" \
    --timeout 120 \
    --poll-duration 5)

log "Submitting batch task ${task_name}"
task_create_json=$(cli --json task create \
    --name "$task_name" \
    --gpu "$GPU" \
    --instance-type "$INSTANCE_TYPE" \
    --backend "$BACKEND" \
    --image "$TASK_IMAGE" \
    --container-env "NUM_OF_RESULTS=1" \
    --container-env "DELAY_BETWEEN_RESULTS_IN_MINUTES=0" \
    --container-env "FILE_SIZE_BYTES=8192" \
    --container-env "INCLUDE_METADATA=true" \
    --description "Batch stage for workflow ${WORKFLOW_ID}" \
    --max-runtime PT15M \
    --max-queued PT15M \
    --termination-grace PT1M \
    --result-strategy NONE)
task_id=$(jq -er '.task.id' <<<"$task_create_json")

start_seconds=$SECONDS
task_status=
while true; do
    task_json=$(cli --json task get "$task_id")
    task_status=$(jq -er '.task.status' <<<"$task_json")
    task_progress=$(jq -r '.task.percentComplete // 0' <<<"$task_json")
    log "Task ${task_id}: ${task_status} (${task_progress}%)"

    case "$task_status" in
        COMPLETED)
            task_terminal=true
            break
            ;;
        ERRORED|CANCELED|EXCEEDED_MAX_RUNTIME_DURATION|EXCEEDED_MAX_QUEUED_DURATION)
            task_terminal=true
            fail "Task ${task_id} ended with status ${task_status}"
            ;;
    esac

    if ((SECONDS - start_seconds >= TASK_TIMEOUT_SECONDS)); then
        fail "Task ${task_id} did not finish within ${TASK_TIMEOUT_SECONDS} seconds"
    fi
    sleep "$POLL_INTERVAL_SECONDS"
done

events_output=$(cli --json task events "$task_id")
completion_message="Task ${task_id} completed for workflow ${WORKFLOW_ID}"
completion_body=$(jq -cn \
    --arg message "$completion_message" \
    '{message: $message, repeats: 1}')

log "Invoking the interactive completion stage"
completion_output=$(cli --json function invoke \
    --function-id "$function_id" \
    --version-id "$version_id" \
    --request-body "$completion_body" \
    --timeout 120 \
    --poll-duration 5)

jq -n \
    --arg workflowId "$WORKFLOW_ID" \
    --arg functionId "$function_id" \
    --arg versionId "$version_id" \
    --arg taskId "$task_id" \
    --arg taskStatus "$task_status" \
    --argjson admission "$admission_output" \
    --argjson taskEvents "$events_output" \
    --argjson completion "$completion_output" \
    '{
        workflowId: $workflowId,
        function: {
            id: $functionId,
            versionId: $versionId,
            admission: $admission,
            completion: $completion
        },
        task: {
            id: $taskId,
            status: $taskStatus,
            events: $taskEvents
        }
    }'
