#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT

fake_cli="${test_dir}/nvcf-cli"
fake_log="${test_dir}/commands.log"
fake_task_state="${test_dir}/task-state"
fake_task_results_state="${test_dir}/task-results-state"
fake_task_delete_state="${test_dir}/task-delete-state"
fake_deploy_remove_state="${test_dir}/deploy-remove-state"
fake_function_delete_state="${test_dir}/function-delete-state"
jq_argv_log="${test_dir}/jq-argv.log"
config_file="${test_dir}/config.yaml"
touch "$config_file"

real_jq=$(command -v jq)
cat >"${test_dir}/jq" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\0' "$@" >>"$JQ_ARGV_LOG"
exec "$REAL_JQ_BIN" "$@"
EOF
chmod +x "${test_dir}/jq"

cat >"${test_dir}/date" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ ${NGC_API_KEY+x} == x ]]; then
    printf 'NGC_API_KEY reached the timestamp child environment\n' >&2
    exit 9
fi
printf 'test-run\n'
EOF
chmod +x "${test_dir}/date"

cat >"$fake_cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ ${NGC_API_KEY+x} == x ]]; then
    printf 'NGC_API_KEY reached the CLI child environment\n' >&2
    exit 9
fi

printf '%s\n' "$*" >>"$FAKE_LOG"

case " $* " in
    *" function create "*)
        if [[ " $* " != *" --json "* ||
              " $* " != *" --health-protocol HTTP "* ||
              " $* " != *" --health-timeout PT30S "* ]]; then
            printf 'Function create configuration is incomplete\n' >&2
            exit 2
        fi
        if [[ ${FAKE_MALFORMED_CREATE:-false} == true ]]; then
            printf '%s\n' '{"function":{"name":"function-task-test-run"}}'
        elif [[ ${FAKE_EMPTY_FUNCTION_IDS:-false} == true ]]; then
            printf '%s\n' '{"function":{"id":"","versionId":"","name":"function-task-test-run"}}'
        else
            printf '%s\n' '{"function":{"id":"function-test","versionId":"version-test","name":"function-task-test-run"}}'
        fi
        ;;
    *" function deploy create "*)
        printf 'Function deployed\n'
        ;;
    *" --json function invoke "*)
        request_body=
        previous=
        for argument in "$@"; do
            if [[ $previous == --request-body ]]; then
                request_body=$argument
                break
            fi
            previous=$argument
        done
        message=$(jq -er '.message' <<<"$request_body")
        if jq -e 'has("inputs")' >/dev/null <<<"$message"; then
            if [[ ${FAKE_REJECT_ADMISSION:-false} == true ]]; then
                message=$(jq -cn \
                    --arg workflowId "test-run" \
                    '{workflowId: $workflowId, decision: "rejected"}')
            else
                message=$(jq -c \
                    --arg workflowId \
                        "${FAKE_ADMITTED_WORKFLOW_ID:-test-run}" \
                    --arg operation \
                        "${FAKE_ADMITTED_OPERATION:-inventory-model-artifacts}" \
                    --arg model \
                        "${FAKE_ADMITTED_MODEL:-model:test:ngc://models/test}" \
                    --arg dataset \
                        "${FAKE_ADMITTED_DATASET:-dataset:test:ngc://datasets/test}" \
                    '
                    .workflowId = $workflowId |
                    .operation = $operation |
                    .inputs.model = $model |
                    .inputs.dataset = $dataset
                    ' \
                    <<<"$message")
                if [[ ${FAKE_EMPTY_ADMITTED_MODEL:-false} == true ]]; then
                    message=$(jq -c '.inputs.model = ""' <<<"$message")
                fi
                if [[ ${FAKE_EMPTY_ADMITTED_DATASET:-false} == true ]]; then
                    message=$(jq -c '.inputs.dataset = ""' <<<"$message")
                fi
            fi
        fi
        jq -cn --arg result "$message" \
            '{status: "fulfilled", response: {result: $result}}'
        ;;
    *" --json task create "*)
        if [[ " $* " != *" --result-strategy UPLOAD "* ||
              " $* " != *" --results-location test-org/test-model "* ||
              " $* " != *" --container-env WORKFLOW_REQUEST_BASE64="* ||
              " $* " != *" --container-env RESULTS_LOCATION=test-org/test-model "* ||
              " $* " != *" --input-file "* ||
              " $* " == *" --secrets "* ]]; then
            printf 'Task artifact handoff configuration is incomplete\n' >&2
            exit 2
        fi
        input_file=
        encoded_request=
        model_artifact=
        dataset_artifact=
        models_dir=
        resources_dir=
        previous=
        for argument in "$@"; do
            if [[ $previous == --input-file ]]; then
                input_file=$argument
            fi
            if [[ $previous == --models ]]; then
                model_artifact=$argument
            fi
            if [[ $previous == --resources ]]; then
                dataset_artifact=$argument
            fi
            if [[ $argument == WORKFLOW_REQUEST_BASE64=* ]]; then
                encoded_request=${argument#WORKFLOW_REQUEST_BASE64=}
            fi
            if [[ $argument == INPUT_MODELS_DIR=* ]]; then
                models_dir=${argument#INPUT_MODELS_DIR=}
            fi
            if [[ $argument == INPUT_RESOURCES_DIR=* ]]; then
                resources_dir=${argument#INPUT_RESOURCES_DIR=}
            fi
            previous=$argument
        done
        if [[ $model_artifact != "${FAKE_EXPECTED_MODEL_ARTIFACT:-model:test:ngc://models/test}" ||
              $dataset_artifact != "${FAKE_EXPECTED_DATASET_ARTIFACT:-dataset:test:ngc://datasets/test}" ]]; then
            printf 'Task did not mount the admitted artifacts\n' >&2
            exit 2
        fi
        if [[ $models_dir != "/config/models/${model_artifact%%:*}" ||
              $resources_dir != "/config/resources/${dataset_artifact%%:*}" ]]; then
            printf 'Task did not isolate artifacts on the shared volume\n' >&2
            exit 2
        fi
        if [[ ! -f $input_file ]] ||
            ! jq -e '
                .secrets == [{
                    "name": "NGC_API_KEY",
                    "value": env.FAKE_EXPECTED_SECRET
                }]
            ' "$input_file" >/dev/null; then
            printf 'Task secret input is missing or invalid\n' >&2
            exit 2
        fi
        input_permissions=$(LC_ALL=C ls -ld "$input_file")
        if [[ ${input_permissions:1:9} != rw------- ]]; then
            printf 'Task secret input must have mode 600\n' >&2
            exit 2
        fi
        if ! jq -en --arg encoded "$encoded_request" '
            ($encoded | @base64d | fromjson) as $request
            | $request.workflowId == "test-run" and
                $request.operation == "inventory-model-artifacts" and
                $request.inputs.model ==
                    env.FAKE_EXPECTED_MODEL_ARTIFACT and
                $request.inputs.dataset ==
                    env.FAKE_EXPECTED_DATASET_ARTIFACT
        ' >/dev/null; then
            printf 'Task did not receive the admitted workflow request\n' >&2
            exit 2
        fi
        if [[ ${FAKE_EMPTY_TASK_ID:-false} == true ]]; then
            printf '%s\n' '{"task":{"id":"","status":"QUEUED"}}'
        else
            printf '%s\n' '{"task":{"id":"task-test","status":"QUEUED"}}'
        fi
        ;;
    *" --json task get "*)
        if [[ " $* " != *" --timeout "* ]]; then
            printf 'Task get must have a timeout\n' >&2
            exit 2
        fi
        sleep "${FAKE_TASK_GET_DELAY_SECONDS:-0}"
        count=0
        if [[ -f $FAKE_TASK_STATE ]]; then
            count=$(<"$FAKE_TASK_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_TASK_STATE"
        if ((count <= ${FAKE_TASK_GET_FAILURES:-0})); then
            exit 4
        fi
        if [[ ${FAKE_ALWAYS_RUNNING:-false} == true ]]; then
            status=RUNNING
        elif ((count == 1)) && [[ -n ${FAKE_FIRST_STATUS:-} ]]; then
            status=$FAKE_FIRST_STATUS
        else
            status=${FAKE_TERMINAL_STATUS:-COMPLETED}
        fi
        if [[ $status == QUEUED || $status == LAUNCHED || $status == RUNNING ]]; then
            progress=50
        else
            progress=100
        fi
        printf '{"task":{"id":"task-test","status":"%s","percentComplete":%s}}\n' \
            "$status" "$progress"
        ;;
    *" --json task events "*)
        if [[ " $* " != *" --timeout "* ]]; then
            printf 'Task events must have a timeout\n' >&2
            exit 2
        fi
        sleep "${FAKE_TASK_EVENTS_DELAY_SECONDS:-0}"
        if [[ ${FAKE_TASK_EVENTS_FAIL:-false} == true ]]; then
            exit 4
        fi
        printf '%s\n' '{"events":[{"taskId":"task-test","message":"COMPLETED"}]}'
        ;;
    *" --json task results "*)
        timeout_seconds=
        previous=
        for argument in "$@"; do
            if [[ $previous == --timeout ]]; then
                timeout_seconds=$argument
            fi
            previous=$argument
        done
        if [[ -z $timeout_seconds ]]; then
            printf 'Task results must have a timeout\n' >&2
            exit 2
        fi
        delay_seconds=${FAKE_TASK_RESULTS_DELAY_SECONDS:-0}
        if ((delay_seconds >= timeout_seconds && delay_seconds > 0)); then
            sleep "$timeout_seconds"
            exit 4
        fi
        sleep "$delay_seconds"
        count=0
        if [[ -f $FAKE_TASK_RESULTS_STATE ]]; then
            count=$(<"$FAKE_TASK_RESULTS_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_TASK_RESULTS_STATE"
        if [[ ${FAKE_NO_RESULTS:-false} == true ||
              $count -le ${FAKE_RESULTS_DELAY_CALLS:-0} ]]; then
            printf '%s\n' '{"results":[]}'
        else
            jq -cn \
                --arg taskId "${FAKE_RESULT_TASK_ID:-task-test}" \
                --arg name \
                    "${FAKE_RESULT_NAME:-artifact-inventory_00000000-0000-4000-8000-000000000000}" \
                --arg status "${FAKE_RESULT_STATUS:-complete}" \
                --arg workflowId "${FAKE_RESULT_WORKFLOW_ID:-test-run}" \
                --arg resultsLocation \
                    "${FAKE_RESULT_LOCATION:-test-org/test-model}" \
                --arg reportPath "${FAKE_RESULT_REPORT_PATH:-report.json}" \
                --argjson modelFileCount \
                    "${FAKE_RESULT_MODEL_FILE_COUNT:-1}" \
                --argjson datasetFileCount \
                    "${FAKE_RESULT_DATASET_FILE_COUNT:-1}" \
                --argjson totalBytes "${FAKE_RESULT_TOTAL_BYTES:-24}" \
                --arg reportSha256 \
                    "${FAKE_RESULT_REPORT_SHA256:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" \
                '{
                    results: [{
                        resultId: "result-test",
                        taskId: $taskId,
                        name: $name,
                        metadata: {
                            status: $status,
                            workflowId: $workflowId,
                            resultsLocation: $resultsLocation,
                            reportPath: $reportPath,
                            modelFileCount: $modelFileCount,
                            datasetFileCount: $datasetFileCount,
                            totalBytes: $totalBytes,
                            reportSha256: $reportSha256
                        }
                    }]
                }'
        fi
        ;;
    *" task cancel "*)
        if [[ ${FAKE_TASK_CANCEL_FAIL:-false} == true ]]; then
            exit 3
        fi
        ;;
    *" task delete "*)
        count=0
        if [[ -f $FAKE_TASK_DELETE_STATE ]]; then
            count=$(<"$FAKE_TASK_DELETE_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_TASK_DELETE_STATE"
        if ((count <= ${FAKE_TASK_DELETE_FAILURES:-0})); then
            exit 3
        fi
        ;;
    *" function deploy remove "*)
        count=0
        if [[ -f $FAKE_DEPLOY_REMOVE_STATE ]]; then
            count=$(<"$FAKE_DEPLOY_REMOVE_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_DEPLOY_REMOVE_STATE"
        if ((count <= ${FAKE_DEPLOY_REMOVE_FAILURES:-0})); then
            exit 3
        fi
        ;;
    *" function delete "*)
        count=0
        if [[ -f $FAKE_FUNCTION_DELETE_STATE ]]; then
            count=$(<"$FAKE_FUNCTION_DELETE_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_FUNCTION_DELETE_STATE"
        if ((count <= ${FAKE_FUNCTION_DELETE_FAILURES:-0})); then
            exit 3
        fi
        ;;
    *)
        printf 'Unexpected command: %s\n' "$*" >&2
        exit 2
        ;;
esac
EOF
chmod +x "$fake_cli"

run_workflow() {
    local workflow_id=test-run
    if [[ ${FAKE_USE_DEFAULT_WORKFLOW_ID:-false} == true ]]; then
        workflow_id=
    fi

    PATH="${test_dir}:$PATH" \
    REAL_JQ_BIN=$real_jq \
    JQ_ARGV_LOG=$jq_argv_log \
    FAKE_LOG=$fake_log \
    FAKE_TASK_STATE=$fake_task_state \
    FAKE_TASK_RESULTS_STATE=$fake_task_results_state \
    FAKE_TASK_DELETE_STATE=$fake_task_delete_state \
    FAKE_DEPLOY_REMOVE_STATE=$fake_deploy_remove_state \
    FAKE_FUNCTION_DELETE_STATE=$fake_function_delete_state \
    FAKE_MALFORMED_CREATE=${FAKE_MALFORMED_CREATE:-false} \
    FAKE_EMPTY_FUNCTION_IDS=${FAKE_EMPTY_FUNCTION_IDS:-false} \
    FAKE_EMPTY_TASK_ID=${FAKE_EMPTY_TASK_ID:-false} \
    FAKE_FIRST_STATUS=${FAKE_FIRST_STATUS:-} \
    FAKE_ALWAYS_RUNNING=${FAKE_ALWAYS_RUNNING:-false} \
    FAKE_TASK_GET_FAILURES=${FAKE_TASK_GET_FAILURES:-0} \
    FAKE_TASK_GET_DELAY_SECONDS=${FAKE_TASK_GET_DELAY_SECONDS:-0} \
    FAKE_TASK_EVENTS_FAIL=${FAKE_TASK_EVENTS_FAIL:-false} \
    FAKE_TASK_EVENTS_DELAY_SECONDS=${FAKE_TASK_EVENTS_DELAY_SECONDS:-0} \
    FAKE_TERMINAL_STATUS=${FAKE_TERMINAL_STATUS:-COMPLETED} \
    FAKE_REJECT_ADMISSION=${FAKE_REJECT_ADMISSION:-false} \
    FAKE_ADMITTED_WORKFLOW_ID=${FAKE_ADMITTED_WORKFLOW_ID:-test-run} \
    FAKE_ADMITTED_OPERATION=${FAKE_ADMITTED_OPERATION:-inventory-model-artifacts} \
    FAKE_ADMITTED_MODEL=${FAKE_ADMITTED_MODEL:-model:test:ngc://models/test} \
    FAKE_ADMITTED_DATASET=${FAKE_ADMITTED_DATASET:-dataset:test:ngc://datasets/test} \
    FAKE_EMPTY_ADMITTED_MODEL=${FAKE_EMPTY_ADMITTED_MODEL:-false} \
    FAKE_EMPTY_ADMITTED_DATASET=${FAKE_EMPTY_ADMITTED_DATASET:-false} \
    FAKE_EXPECTED_MODEL_ARTIFACT=${FAKE_EXPECTED_MODEL_ARTIFACT:-model:test:ngc://models/test} \
    FAKE_EXPECTED_DATASET_ARTIFACT=${FAKE_EXPECTED_DATASET_ARTIFACT:-dataset:test:ngc://datasets/test} \
    FAKE_EXPECTED_SECRET=nvapi-test \
    FAKE_NO_RESULTS=${FAKE_NO_RESULTS:-false} \
    FAKE_RESULTS_DELAY_CALLS=${FAKE_RESULTS_DELAY_CALLS:-0} \
    FAKE_TASK_RESULTS_DELAY_SECONDS=${FAKE_TASK_RESULTS_DELAY_SECONDS:-0} \
    FAKE_RESULT_TASK_ID=${FAKE_RESULT_TASK_ID:-task-test} \
    FAKE_RESULT_NAME=${FAKE_RESULT_NAME:-artifact-inventory_00000000-0000-4000-8000-000000000000} \
    FAKE_RESULT_STATUS=${FAKE_RESULT_STATUS:-complete} \
    FAKE_RESULT_WORKFLOW_ID=${FAKE_RESULT_WORKFLOW_ID:-test-run} \
    FAKE_RESULT_LOCATION=${FAKE_RESULT_LOCATION:-test-org/test-model} \
    FAKE_RESULT_REPORT_PATH=${FAKE_RESULT_REPORT_PATH:-report.json} \
    FAKE_RESULT_MODEL_FILE_COUNT=${FAKE_RESULT_MODEL_FILE_COUNT:-1} \
    FAKE_RESULT_DATASET_FILE_COUNT=${FAKE_RESULT_DATASET_FILE_COUNT:-1} \
    FAKE_RESULT_TOTAL_BYTES=${FAKE_RESULT_TOTAL_BYTES:-24} \
    FAKE_RESULT_REPORT_SHA256=${FAKE_RESULT_REPORT_SHA256:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa} \
    FAKE_TASK_CANCEL_FAIL=${FAKE_TASK_CANCEL_FAIL:-false} \
    FAKE_TASK_DELETE_FAILURES=${FAKE_TASK_DELETE_FAILURES:-0} \
    FAKE_DEPLOY_REMOVE_FAILURES=${FAKE_DEPLOY_REMOVE_FAILURES:-0} \
    FAKE_FUNCTION_DELETE_FAILURES=${FAKE_FUNCTION_DELETE_FAILURES:-0} \
    NVCF_CLI_BIN=$fake_cli \
    NVCF_CLI_CONFIG=$config_file \
    FUNCTION_IMAGE=registry.example/function:test \
    TASK_IMAGE=registry.example/task:test \
    MODEL_ARTIFACT=model:test:ngc://models/test \
    DATASET_ARTIFACT=dataset:test:ngc://datasets/test \
    RESULTS_LOCATION=test-org/test-model \
    NGC_API_KEY=nvapi-test \
    WORKFLOW_ID=$workflow_id \
    POLL_INTERVAL_SECONDS=${POLL_INTERVAL_SECONDS:-1} \
    TASK_TIMEOUT_SECONDS=${TASK_TIMEOUT_SECONDS:-5} \
    TASK_STATUS_READ_ATTEMPTS=${TASK_STATUS_READ_ATTEMPTS:-3} \
    TASK_EVENTS_TIMEOUT_SECONDS=${TASK_EVENTS_TIMEOUT_SECONDS:-1} \
    RESULTS_TIMEOUT_SECONDS=${RESULTS_TIMEOUT_SECONDS:-5} \
    KEEP_RESOURCES=${KEEP_RESOURCES:-false} \
    CLEANUP_DELETE_ATTEMPTS=3 \
    CLEANUP_RETRY_INTERVAL_SECONDS=0 \
    "$script_dir/run.sh"
}

reset_fake() {
    : >"$fake_log"
    : >"$jq_argv_log"
    rm -f \
        "$fake_task_state" \
        "$fake_task_results_state" \
        "$fake_task_delete_state" \
        "$fake_deploy_remove_state" \
        "$fake_function_delete_state"
}

assert_command_order() {
    local previous=0
    local pattern
    local line
    for pattern in "$@"; do
        line=$(grep -n -m1 -- "$pattern" "$fake_log" | cut -d: -f1)
        if [[ -z $line || $line -le $previous ]]; then
            printf 'Command is missing or out of order: %s\n' "$pattern" >&2
            return 1
        fi
        previous=$line
    done
}

reset_fake
success_output=$(FAKE_FIRST_STATUS=RUNNING run_workflow)
if ! jq -e '
    .workflowId == "test-run" and
    .function.id == "function-test" and
    (.function.admission.response.result | fromjson |
        .operation == "inventory-model-artifacts") and
    (.function.completion.response.result | fromjson |
        .inventoryResult.metadata.status == "complete" and
        .inventoryResult.metadata.resultsLocation ==
            "test-org/test-model" and
        .inventoryResult.metadata.reportPath ==
            "report.json") and
    .task.id == "task-test" and
    .task.status == "COMPLETED" and
    .task.result.metadata.status == "complete" and
    .task.result.metadata.workflowId == "test-run"
' >/dev/null <<<"$success_output"; then
    printf 'Success output did not contain the expected workflow summary\n' >&2
    exit 1
fi
if grep -q -- "nvapi-test" "$fake_log"; then
    printf 'Task secret must not appear in the command log\n' >&2
    exit 1
fi
if grep -a -q -- "nvapi-test" "$jq_argv_log"; then
    printf 'Task secret must not appear in jq process arguments\n' >&2
    exit 1
fi
task_input_file=$(sed -n 's/.*--input-file \([^ ]*\).*/\1/p' "$fake_log" | head -1)
if [[ -z $task_input_file ]]; then
    printf 'Task create did not use a temporary input file\n' >&2
    exit 1
fi
if [[ -e $task_input_file ]]; then
    printf 'Temporary task secret input was not removed: %s\n' \
        "$task_input_file" >&2
    exit 1
fi

assert_command_order \
    "function create" \
    "function deploy create" \
    "function invoke" \
    "task create" \
    "task get" \
    "task events" \
    "task results"

first_invoke_line=$(grep -n -- "function invoke" "$fake_log" | sed -n '1s/:.*//p')
task_create_line=$(grep -n -m1 -- "task create" "$fake_log" | cut -d: -f1)
task_events_line=$(grep -n -m1 -- "task events" "$fake_log" | cut -d: -f1)
task_results_line=$(grep -n -m1 -- "task results" "$fake_log" | cut -d: -f1)
second_invoke_line=$(grep -n -- "function invoke" "$fake_log" | sed -n '2s/:.*//p')
deploy_remove_line=$(grep -n -m1 -- "function deploy remove" "$fake_log" | cut -d: -f1)
function_delete_line=$(grep -n -m1 -- "function delete" "$fake_log" | cut -d: -f1)
if [[ -z $first_invoke_line || -z $second_invoke_line ||
      $first_invoke_line -ge $task_create_line ||
      $task_create_line -ge $task_events_line ||
      $task_events_line -ge $task_results_line ||
      $task_results_line -ge $second_invoke_line ||
      $second_invoke_line -ge $deploy_remove_line ||
      $deploy_remove_line -ge $function_delete_line ]]; then
    printf 'Workflow stages or cleanup commands are out of order\n' >&2
    exit 1
fi

reset_fake
FAKE_RESULTS_DELAY_CALLS=1 run_workflow >/dev/null
task_results_attempts=$(grep -c -- "task results --timeout" "$fake_log")
if ((task_results_attempts != 2)); then
    printf 'Expected delayed task results to be retried\n' >&2
    exit 1
fi

reset_fake
events_failure_output=$(FAKE_TASK_EVENTS_FAIL=true \
    FAKE_TASK_EVENTS_DELAY_SECONDS=1 \
    TASK_EVENTS_TIMEOUT_SECONDS=1 \
    RESULTS_TIMEOUT_SECONDS=1 \
    run_workflow)
if ! jq -e '.task.events.events == []' <<<"$events_failure_output" >/dev/null; then
    printf 'An event request failure must produce an empty event list\n' >&2
    exit 1
fi
grep -q -- "task events --timeout 1" "$fake_log"
grep -q -- "task results --timeout" "$fake_log"

reset_fake
if FAKE_NO_RESULTS=true RESULTS_TIMEOUT_SECONDS=1 \
    run_workflow >/dev/null 2>&1; then
    printf 'Expected missing task results to fail the workflow\n' >&2
    exit 1
fi
grep -q -- "task results --timeout" "$fake_log"

reset_fake
if FAKE_TASK_RESULTS_DELAY_SECONDS=2 RESULTS_TIMEOUT_SECONDS=1 \
    run_workflow >/dev/null 2>&1; then
    printf 'Expected a hanging task results request to respect the deadline\n' >&2
    exit 1
fi
grep -q -- "task results --timeout 1 task-test" "$fake_log"
if (( $(grep -c -- "function invoke" "$fake_log") != 1 )); then
    printf 'A timed-out result request must not invoke completion\n' >&2
    exit 1
fi

for mismatch in \
    task \
    name \
    workflow \
    status \
    location \
    path \
    model-count \
    model-count-fraction \
    dataset-count \
    dataset-count-fraction \
    total-bytes \
    total-bytes-fraction \
    digest; do
    reset_fake
    case "$mismatch" in
        task)
            mismatch_env=(FAKE_RESULT_TASK_ID=task-other)
            ;;
        name)
            mismatch_env=(FAKE_RESULT_NAME=artifact-other)
            ;;
        workflow)
            mismatch_env=(FAKE_RESULT_WORKFLOW_ID=workflow-other)
            ;;
        status)
            mismatch_env=(FAKE_RESULT_STATUS=incomplete)
            ;;
        location)
            mismatch_env=(FAKE_RESULT_LOCATION=test-org/other-model)
            ;;
        path)
            mismatch_env=(FAKE_RESULT_REPORT_PATH=other.json)
            ;;
        model-count)
            mismatch_env=(FAKE_RESULT_MODEL_FILE_COUNT=0)
            ;;
        model-count-fraction)
            mismatch_env=(FAKE_RESULT_MODEL_FILE_COUNT=1.5)
            ;;
        dataset-count)
            mismatch_env=(FAKE_RESULT_DATASET_FILE_COUNT=0)
            ;;
        dataset-count-fraction)
            mismatch_env=(FAKE_RESULT_DATASET_FILE_COUNT=1.5)
            ;;
        total-bytes)
            mismatch_env=(FAKE_RESULT_TOTAL_BYTES=-1)
            ;;
        total-bytes-fraction)
            mismatch_env=(FAKE_RESULT_TOTAL_BYTES=1.5)
            ;;
        digest)
            mismatch_env=(FAKE_RESULT_REPORT_SHA256=not-a-sha256)
            ;;
    esac
    export "${mismatch_env[@]}"
    if RESULTS_TIMEOUT_SECONDS=1 run_workflow >/dev/null 2>&1; then
        printf 'Expected mismatched %s result metadata to fail\n' "$mismatch" >&2
        exit 1
    fi
    for assignment in "${mismatch_env[@]}"; do
        unset "${assignment%%=*}"
    done
done

reset_fake
FAKE_RESULT_TOTAL_BYTES=0 run_workflow >/dev/null

reset_fake
FAKE_ADMITTED_MODEL=model:accepted:ngc://models/accepted \
FAKE_EXPECTED_MODEL_ARTIFACT=model:accepted:ngc://models/accepted \
FAKE_ADMITTED_DATASET=evalset:accepted:ngc://datasets/accepted \
FAKE_EXPECTED_DATASET_ARTIFACT=evalset:accepted:ngc://datasets/accepted \
    run_workflow >/dev/null
grep -q -- "--models model:accepted:ngc://models/accepted" "$fake_log"
grep -q -- "--resources evalset:accepted:ngc://datasets/accepted" "$fake_log"
grep -q -- "INPUT_MODELS_DIR=/config/models/model" "$fake_log"
grep -q -- "INPUT_RESOURCES_DIR=/config/resources/evalset" "$fake_log"

reset_fake
if FAKE_ADMITTED_DATASET=model:accepted:ngc://datasets/accepted \
    run_workflow >/dev/null 2>&1; then
    printf 'Expected identical model and dataset names to be rejected\n' >&2
    exit 1
fi
if grep -q -- "task create" "$fake_log"; then
    printf 'Aliased artifact names must be rejected before Task creation\n' >&2
    exit 1
fi

for unsafe_model in \
    '../resources:accepted:ngc://models/accepted' \
    'model/name:accepted:ngc://models/accepted'; do
    reset_fake
    if FAKE_ADMITTED_MODEL="$unsafe_model" \
        FAKE_EXPECTED_MODEL_ARTIFACT="$unsafe_model" \
        run_workflow >/dev/null 2>&1; then
        printf 'Expected unsafe model artifact name to be rejected: %s\n' \
            "$unsafe_model" >&2
        exit 1
    fi
    if grep -q -- "task create" "$fake_log"; then
        printf 'Unsafe artifact names must be rejected before Task creation\n' >&2
        exit 1
    fi
done

for admission_mismatch in workflow operation model dataset; do
    reset_fake
    case "$admission_mismatch" in
        workflow)
            mismatch_env=(FAKE_ADMITTED_WORKFLOW_ID=workflow-other)
            ;;
        operation)
            mismatch_env=(FAKE_ADMITTED_OPERATION=other-operation)
            ;;
        model)
            mismatch_env=(FAKE_EMPTY_ADMITTED_MODEL=true)
            ;;
        dataset)
            mismatch_env=(FAKE_EMPTY_ADMITTED_DATASET=true)
            ;;
    esac
    export "${mismatch_env[@]}"
    if run_workflow >/dev/null 2>&1; then
        printf 'Expected mismatched admission %s to fail\n' \
            "$admission_mismatch" >&2
        exit 1
    fi
    for assignment in "${mismatch_env[@]}"; do
        unset "${assignment%%=*}"
    done
    if grep -q -- "task create" "$fake_log"; then
        printf 'An invalid admission must not create a Task\n' >&2
        exit 1
    fi
done

reset_fake
if FAKE_REJECT_ADMISSION=true run_workflow >/dev/null 2>&1; then
    printf 'Expected a rejected Function admission response to fail\n' >&2
    exit 1
fi
if grep -q -- "task create" "$fake_log"; then
    printf 'A rejected admission response must not create a Task\n' >&2
    exit 1
fi

reset_fake
FAKE_USE_DEFAULT_WORKFLOW_ID=true run_workflow >/dev/null

reset_fake
if FAKE_TERMINAL_STATUS=ERRORED run_workflow >/dev/null 2>&1; then
    printf 'Expected an errored task to fail the workflow\n' >&2
    exit 1
fi

grep -q -- "task delete task-test" "$fake_log"
grep -q -- "function deploy remove" "$fake_log"
grep -q -- "function delete function-test version-test" "$fake_log"

reset_fake
if FAKE_TERMINAL_STATUS=UNKNOWN run_workflow >/dev/null 2>&1; then
    printf 'Expected an unknown task status to fail the workflow\n' >&2
    exit 1
fi
grep -q -- "task cancel task-test" "$fake_log"

reset_fake
if FAKE_ALWAYS_RUNNING=true TASK_TIMEOUT_SECONDS=1 run_workflow >/dev/null 2>&1; then
    printf 'Expected a task timeout to fail the workflow\n' >&2
    exit 1
fi
grep -q -- "task cancel task-test" "$fake_log"

reset_fake
FAKE_TASK_GET_FAILURES=2 run_workflow >/dev/null
task_get_attempts=$(grep -c -- "task get --timeout" "$fake_log")
if ((task_get_attempts != 3)); then
    printf 'Expected transient task status failures to be retried\n' >&2
    exit 1
fi

reset_fake
if FAKE_TASK_GET_FAILURES=3 TASK_STATUS_READ_ATTEMPTS=3 \
    run_workflow >/dev/null 2>&1; then
    printf 'Expected exhausted task status reads to fail the workflow\n' >&2
    exit 1
fi
task_get_attempts=$(grep -c -- "task get --timeout" "$fake_log")
if ((task_get_attempts != 3)); then
    printf 'Expected task status reads to stop at the retry limit\n' >&2
    exit 1
fi

reset_fake
cancel_failure_log="${test_dir}/cancel-failure.log"
if FAKE_TERMINAL_STATUS=UNKNOWN FAKE_TASK_CANCEL_FAIL=true \
    run_workflow >/dev/null 2>"$cancel_failure_log"; then
    printf 'Expected a task cancellation failure to fail cleanup\n' >&2
    exit 1
fi
grep -q -- "task cancel task-test" "$fake_log"
grep -q -- "ERROR: Failed to cancel task task-test" "$cancel_failure_log"
grep -q -- "ERROR: Cleanup incomplete" "$cancel_failure_log"
grep -q -- "task delete task-test" "$fake_log"
grep -q -- "function deploy remove" "$fake_log"
grep -q -- "function delete function-test version-test" "$fake_log"

reset_fake
if FAKE_TASK_DELETE_FAILURES=3 run_workflow >/dev/null 2>&1; then
    printf 'Expected incomplete cleanup to fail a successful workflow\n' >&2
    exit 1
fi
task_delete_attempts=$(grep -c -- "task delete task-test" "$fake_log")
if ((task_delete_attempts != 3)); then
    printf 'Expected task deletion to use all cleanup attempts\n' >&2
    exit 1
fi
grep -q -- "function deploy remove" "$fake_log"
grep -q -- "function delete function-test version-test" "$fake_log"

reset_fake
FAKE_DEPLOY_REMOVE_FAILURES=2 run_workflow >/dev/null 2>&1
deploy_remove_attempts=$(grep -c -- "function deploy remove" "$fake_log")
if ((deploy_remove_attempts != 3)); then
    printf 'Expected deployment removal to succeed on the third attempt\n' >&2
    exit 1
fi

reset_fake
if FAKE_DEPLOY_REMOVE_FAILURES=3 run_workflow >/dev/null 2>&1; then
    printf 'Expected exhausted deployment removal to fail cleanup\n' >&2
    exit 1
fi
deploy_remove_attempts=$(grep -c -- "function deploy remove" "$fake_log")
if ((deploy_remove_attempts != 3)); then
    printf 'Expected deployment removal to use all cleanup attempts\n' >&2
    exit 1
fi

reset_fake
FAKE_FUNCTION_DELETE_FAILURES=2 run_workflow >/dev/null
delete_attempts=$(grep -c -- "function delete function-test version-test" "$fake_log")
if ((delete_attempts != 3)); then
    printf 'Expected function deletion to succeed on the third attempt\n' >&2
    exit 1
fi

reset_fake
if FAKE_TASK_GET_DELAY_SECONDS=2 TASK_TIMEOUT_SECONDS=1 \
    run_workflow >/dev/null 2>&1; then
    printf 'Expected a late completed status to miss the task deadline\n' >&2
    exit 1
fi
grep -q -- "task cancel task-test" "$fake_log"
if grep -q -- "task events" "$fake_log"; then
    printf 'A task that completed after the deadline must not advance\n' >&2
    exit 1
fi

reset_fake
malformed_create_log="${test_dir}/malformed-create.log"
if FAKE_MALFORMED_CREATE=true \
    run_workflow >/dev/null 2>"$malformed_create_log"; then
    printf 'Expected missing Function IDs to fail the workflow\n' >&2
    exit 1
fi
grep -q -- "inspect function-task-test-run manually" "$malformed_create_log"
if grep -q -- "function deploy create" "$fake_log"; then
    printf 'A malformed Function create response must not be deployed\n' >&2
    exit 1
fi

reset_fake
empty_function_ids_log="${test_dir}/empty-function-ids.log"
if FAKE_EMPTY_FUNCTION_IDS=true \
    run_workflow >/dev/null 2>"$empty_function_ids_log"; then
    printf 'Expected empty Function IDs to fail the workflow\n' >&2
    exit 1
fi
grep -q -- "inspect function-task-test-run manually" "$empty_function_ids_log"
if grep -q -- "function deploy create" "$fake_log"; then
    printf 'Empty Function IDs must not be deployed\n' >&2
    exit 1
fi

reset_fake
if FAKE_EMPTY_TASK_ID=true run_workflow >/dev/null 2>&1; then
    printf 'Expected an empty Task ID to fail the workflow\n' >&2
    exit 1
fi
if grep -q -- "task get" "$fake_log"; then
    printf 'An empty Task ID must not be polled\n' >&2
    exit 1
fi

reset_fake
if POLL_INTERVAL_SECONDS=0 run_workflow >/dev/null 2>&1; then
    printf 'Expected a zero poll interval to be rejected\n' >&2
    exit 1
fi
if [[ -s $fake_log ]]; then
    printf 'Poll interval validation should run before creating resources\n' >&2
    exit 1
fi

reset_fake
if TASK_EVENTS_TIMEOUT_SECONDS=0 run_workflow >/dev/null 2>&1; then
    printf 'Expected a zero Task events timeout to be rejected\n' >&2
    exit 1
fi
if [[ -s $fake_log ]]; then
    printf 'Task events timeout validation should run before creating resources\n' >&2
    exit 1
fi

reset_fake
KEEP_RESOURCES=true run_workflow >/dev/null
if grep -q -E -- "task delete|function deploy remove|function delete" "$fake_log"; then
    printf 'KEEP_RESOURCES=true must not delete workflow resources\n' >&2
    exit 1
fi

printf 'function-task-pipeline tests passed\n'
