#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
umask 077

# Keep the key available to this shell for the input-file pipeline without
# forwarding it to any child process, including the default timestamp command.
if [[ ${NGC_API_KEY+x} == x ]]; then
    export -n NGC_API_KEY
fi

NVCF_CLI_BIN=${NVCF_CLI_BIN:-nvcf-cli}
WORKFLOW_ID=${WORKFLOW_ID:-$(date -u +%Y%m%d%H%M%S)}
GPU=${GPU:-H100}
INSTANCE_TYPE=${INSTANCE_TYPE:-NCP.GPU.H100_8x}
BACKEND=${BACKEND:-ncp-local}
REGIONS=${REGIONS:-us-west-1}
FUNCTION_DEPLOY_TIMEOUT_SECONDS=${FUNCTION_DEPLOY_TIMEOUT_SECONDS:-900}
POLL_INTERVAL_SECONDS=${POLL_INTERVAL_SECONDS:-10}
TASK_TIMEOUT_SECONDS=${TASK_TIMEOUT_SECONDS:-900}
RESULTS_TIMEOUT_SECONDS=${RESULTS_TIMEOUT_SECONDS:-120}
CLEANUP_DELETE_ATTEMPTS=${CLEANUP_DELETE_ATTEMPTS:-6}
CLEANUP_RETRY_INTERVAL_SECONDS=${CLEANUP_RETRY_INTERVAL_SECONDS:-5}
KEEP_RESOURCES=${KEEP_RESOURCES:-false}

function_id=
version_id=
task_id=
task_secret_file=
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

retry_cleanup() {
    local description=$1
    local attempt
    shift

    for ((attempt = 1; attempt <= CLEANUP_DELETE_ATTEMPTS; attempt++)); do
        log "${description} (attempt ${attempt}/${CLEANUP_DELETE_ATTEMPTS})"
        if "$@" >&2; then
            return 0
        fi
        if ((attempt < CLEANUP_DELETE_ATTEMPTS)); then
            sleep "$CLEANUP_RETRY_INTERVAL_SECONDS"
        fi
    done
    return 1
}

cleanup() {
    local result=$?
    local cleanup_failed=false
    trap - EXIT

    if [[ -n $task_secret_file ]]; then
        rm -f -- "$task_secret_file"
    fi

    if [[ $KEEP_RESOURCES == true ]]; then
        log "Keeping workflow resources"
        exit "$result"
    fi

    set +e
    if [[ -n $task_id ]]; then
        if [[ $task_terminal != true ]]; then
            log "Canceling task ${task_id}"
            if ! cli task cancel "$task_id" >&2; then
                log "ERROR: Failed to cancel task ${task_id}"
                cleanup_failed=true
            fi
        fi
        if ! retry_cleanup \
            "Deleting task ${task_id}" \
            cli task delete "$task_id"; then
            log "ERROR: Failed to delete task ${task_id}"
            cleanup_failed=true
        fi
    fi

    if [[ $function_deployed == true ]]; then
        if ! retry_cleanup \
            "Removing function deployment ${function_id}/${version_id}" \
            cli function deploy remove \
            --function-id "$function_id" \
            --version-id "$version_id"; then
            log "ERROR: Failed to remove function deployment ${function_id}/${version_id}"
            cleanup_failed=true
        fi
    fi

    if [[ $function_created == true ]]; then
        if ! retry_cleanup \
            "Deleting function ${function_id}/${version_id}" \
            cli function delete "$function_id" "$version_id"; then
            log "ERROR: Failed to delete function ${function_id}/${version_id}"
            cleanup_failed=true
        fi
    fi

    if [[ $cleanup_failed == true ]]; then
        log "ERROR: Cleanup incomplete; verify task ${task_id:-not-created} and function ${function_id:-not-created}/${version_id:-not-created}"
        if ((result == 0)); then
            result=1
        fi
    fi
    exit "$result"
}

trap cleanup EXIT
trap 'exit 130' INT TERM

require_env NVCF_CLI_CONFIG
require_env FUNCTION_IMAGE
require_env TASK_IMAGE
require_env MODEL_ARTIFACT
require_env DATASET_ARTIFACT
require_env RESULTS_LOCATION
require_env NGC_API_KEY

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
if [[ ! $POLL_INTERVAL_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "POLL_INTERVAL_SECONDS must be a positive integer"
fi
if [[ ! $FUNCTION_DEPLOY_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "FUNCTION_DEPLOY_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ ! $TASK_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "TASK_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ ! $RESULTS_TIMEOUT_SECONDS =~ ^[1-9][0-9]*$ ]]; then
    fail "RESULTS_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ ! $CLEANUP_DELETE_ATTEMPTS =~ ^[1-9][0-9]*$ ]]; then
    fail "CLEANUP_DELETE_ATTEMPTS must be a positive integer"
fi
if [[ ! $CLEANUP_RETRY_INTERVAL_SECONDS =~ ^[0-9]+$ ]]; then
    fail "CLEANUP_RETRY_INTERVAL_SECONDS must be a non-negative integer"
fi
if [[ $KEEP_RESOURCES != true && $KEEP_RESOURCES != false ]]; then
    fail "KEEP_RESOURCES must be true or false"
fi

function_name="function-task-${WORKFLOW_ID}"
task_name="task-${WORKFLOW_ID}"

log "Creating function ${function_name}"
function_create_json=$(cli --json function create \
    --name "$function_name" \
    --image "$FUNCTION_IMAGE" \
    --inference-url /echo \
    --inference-port 8000 \
    --health-uri /health \
    --health-protocol HTTP \
    --health-port 8000 \
    --health-timeout PT30S)
function_id=$(jq -er \
    '.function.id | select(type == "string" and length > 0)' \
    <<<"$function_create_json")
version_id=$(jq -er \
    '.function.versionId | select(type == "string" and length > 0)' \
    <<<"$function_create_json")
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

workflow_request=$(jq -cn \
    --arg workflowId "$WORKFLOW_ID" \
    --arg modelArtifact "$MODEL_ARTIFACT" \
    --arg datasetArtifact "$DATASET_ARTIFACT" \
    '{
        workflowId: $workflowId,
        operation: "inventory-model-artifacts",
        inputs: {
            model: $modelArtifact,
            dataset: $datasetArtifact
        }
    }')
admission_body=$(jq -cn \
    --arg message "$workflow_request" \
    '{message: $message, repeats: 1}')

log "Invoking the interactive admission stage"
admission_output=$(cli --json function invoke \
    --function-id "$function_id" \
    --version-id "$version_id" \
    --request-body "$admission_body" \
    --timeout 120 \
    --poll-duration 5)
admission_payload=$(jq -cer \
    --arg workflowId "$WORKFLOW_ID" \
    '
    (.response.result // .responseBody.result)
    | select(type == "string")
    | fromjson
    | select(type == "object")
    | select(.workflowId == $workflowId)
    | select(.operation == "inventory-model-artifacts")
    | select(.inputs.model | type == "string" and length > 0)
    | select(.inputs.dataset | type == "string" and length > 0)
' <<<"$admission_output")
accepted_model_artifact=$(jq -er '.inputs.model' <<<"$admission_payload")
accepted_dataset_artifact=$(jq -er '.inputs.dataset' <<<"$admission_payload")
workflow_request_base64=$(jq -r @base64 <<<"$admission_payload")

task_secret_file=$(mktemp)
printf '%s' "$NGC_API_KEY" |
    jq -Rs '{secrets: [{name: "NGC_API_KEY", value: .}]}' \
    >"$task_secret_file"

log "Submitting batch task ${task_name}"
task_create_json=$(cli --json task create \
    --input-file "$task_secret_file" \
    --name "$task_name" \
    --gpu "$GPU" \
    --instance-type "$INSTANCE_TYPE" \
    --backend "$BACKEND" \
    --image "$TASK_IMAGE" \
    --container-env "WORKFLOW_REQUEST_BASE64=${workflow_request_base64}" \
    --container-env "RESULTS_LOCATION=${RESULTS_LOCATION}" \
    --models "$accepted_model_artifact" \
    --resources "$accepted_dataset_artifact" \
    --description "Inventory model and dataset artifacts for workflow ${WORKFLOW_ID}" \
    --max-runtime PT15M \
    --max-queued PT15M \
    --termination-grace PT1M \
    --result-strategy UPLOAD \
    --results-location "$RESULTS_LOCATION")
rm -f -- "$task_secret_file"
task_secret_file=
task_id=$(jq -er \
    '.task.id | select(type == "string" and length > 0)' \
    <<<"$task_create_json")

task_deadline=$((SECONDS + TASK_TIMEOUT_SECONDS))
task_status=
while true; do
    remaining_seconds=$((task_deadline - SECONDS))
    if ((remaining_seconds <= 0)); then
        fail "Task ${task_id} did not finish within ${TASK_TIMEOUT_SECONDS} seconds"
    fi
    if ! task_json=$(cli --json task get \
        --timeout "$remaining_seconds" \
        "$task_id"); then
        if ((SECONDS >= task_deadline)); then
            fail "Task ${task_id} did not finish within ${TASK_TIMEOUT_SECONDS} seconds"
        fi
        fail "Failed to read status for task ${task_id}"
    fi
    if ((SECONDS >= task_deadline)); then
        fail "Task ${task_id} did not finish within ${TASK_TIMEOUT_SECONDS} seconds"
    fi

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
        QUEUED|LAUNCHED|RUNNING)
            ;;
        *)
            fail "Task ${task_id} returned unexpected status ${task_status}"
            ;;
    esac

    remaining_seconds=$((task_deadline - SECONDS))
    sleep_seconds=$POLL_INTERVAL_SECONDS
    if ((sleep_seconds > remaining_seconds)); then
        sleep_seconds=$remaining_seconds
    fi
    sleep "$sleep_seconds"
done

events_output=$(cli --json task events "$task_id")
results_deadline=$((SECONDS + RESULTS_TIMEOUT_SECONDS))
result_summary=
while true; do
    remaining_seconds=$((results_deadline - SECONDS))
    if ((remaining_seconds <= 0)); then
        fail "Task ${task_id} results were not available within ${RESULTS_TIMEOUT_SECONDS} seconds"
    fi

    if results_output=$(cli --json task results \
        --timeout "$remaining_seconds" \
        "$task_id") &&
        result_summary=$(jq -cer \
            --arg taskId "$task_id" \
            --arg workflowId "$WORKFLOW_ID" \
            --arg resultsLocation "$RESULTS_LOCATION" \
            'first(
                .results[]?
                | select(
                    .taskId == $taskId and
                    (.name | test(
                        "^artifact-inventory_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
                    )) and
                    .metadata.status == "complete" and
                    .metadata.workflowId == $workflowId and
                    .metadata.resultsLocation == $resultsLocation and
                    .metadata.reportPath == "report.json" and
                    (
                        .metadata.modelFileCount |
                        type == "number" and . == floor and . > 0
                    ) and
                    (
                        .metadata.datasetFileCount |
                        type == "number" and . == floor and . > 0
                    ) and
                    (
                        .metadata.totalBytes |
                        type == "number" and . == floor and . >= 0
                    ) and
                    (
                        .metadata.reportSha256 |
                        type == "string" and
                        test("^[0-9a-f]{64}$")
                    )
                )
            )' \
            <<<"$results_output"); then
        break
    fi

    if ((SECONDS >= results_deadline)); then
        fail "Task ${task_id} results were not available within ${RESULTS_TIMEOUT_SECONDS} seconds"
    fi
    remaining_seconds=$((results_deadline - SECONDS))
    log "Waiting for artifact inventory result from task ${task_id}"
    sleep_seconds=$POLL_INTERVAL_SECONDS
    if ((sleep_seconds > remaining_seconds)); then
        sleep_seconds=$remaining_seconds
    fi
    sleep "$sleep_seconds"
done

completion_message=$(jq -cn \
    --arg workflowId "$WORKFLOW_ID" \
    --arg taskId "$task_id" \
    --argjson result "$result_summary" \
    '{
        workflowId: $workflowId,
        taskId: $taskId,
        inventoryResult: $result
    }')
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
    --argjson taskResult "$result_summary" \
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
            events: $taskEvents,
            result: $taskResult
        }
    }'
