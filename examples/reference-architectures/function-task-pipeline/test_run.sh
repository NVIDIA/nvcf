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
fake_task_delete_state="${test_dir}/task-delete-state"
fake_deploy_remove_state="${test_dir}/deploy-remove-state"
fake_function_delete_state="${test_dir}/function-delete-state"
config_file="${test_dir}/config.yaml"
touch "$config_file"

cat >"$fake_cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

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
        printf '%s\n' '{"status":"fulfilled","responseBody":"ok"}'
        ;;
    *" --json task create "*)
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
        printf '%s\n' '{"events":[{"taskId":"task-test","message":"COMPLETED"}]}'
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
    FAKE_LOG=$fake_log \
    FAKE_TASK_STATE=$fake_task_state \
    FAKE_TASK_DELETE_STATE=$fake_task_delete_state \
    FAKE_DEPLOY_REMOVE_STATE=$fake_deploy_remove_state \
    FAKE_FUNCTION_DELETE_STATE=$fake_function_delete_state \
    FAKE_MALFORMED_CREATE=${FAKE_MALFORMED_CREATE:-false} \
    FAKE_EMPTY_FUNCTION_IDS=${FAKE_EMPTY_FUNCTION_IDS:-false} \
    FAKE_EMPTY_TASK_ID=${FAKE_EMPTY_TASK_ID:-false} \
    FAKE_FIRST_STATUS=${FAKE_FIRST_STATUS:-} \
    FAKE_ALWAYS_RUNNING=${FAKE_ALWAYS_RUNNING:-false} \
    FAKE_TASK_GET_DELAY_SECONDS=${FAKE_TASK_GET_DELAY_SECONDS:-0} \
    FAKE_TERMINAL_STATUS=${FAKE_TERMINAL_STATUS:-COMPLETED} \
    FAKE_TASK_CANCEL_FAIL=${FAKE_TASK_CANCEL_FAIL:-false} \
    FAKE_TASK_DELETE_FAILURES=${FAKE_TASK_DELETE_FAILURES:-0} \
    FAKE_DEPLOY_REMOVE_FAILURES=${FAKE_DEPLOY_REMOVE_FAILURES:-0} \
    FAKE_FUNCTION_DELETE_FAILURES=${FAKE_FUNCTION_DELETE_FAILURES:-0} \
    NVCF_CLI_BIN=$fake_cli \
    NVCF_CLI_CONFIG=$config_file \
    FUNCTION_IMAGE=registry.example/function:test \
    TASK_IMAGE=registry.example/task:test \
    WORKFLOW_ID=test-run \
    POLL_INTERVAL_SECONDS=${POLL_INTERVAL_SECONDS:-1} \
    TASK_TIMEOUT_SECONDS=${TASK_TIMEOUT_SECONDS:-5} \
    CLEANUP_DELETE_ATTEMPTS=3 \
    CLEANUP_RETRY_INTERVAL_SECONDS=0 \
    "$script_dir/run.sh"
}

reset_fake() {
    : >"$fake_log"
    rm -f \
        "$fake_task_state" \
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
    .task.id == "task-test" and
    .task.status == "COMPLETED"
' >/dev/null <<<"$success_output"; then
    printf 'Success output did not contain the expected workflow summary\n' >&2
    exit 1
fi

assert_command_order \
    "function create" \
    "function deploy create" \
    "function invoke" \
    "task create" \
    "task get" \
    "task events"

first_invoke_line=$(grep -n -- "function invoke" "$fake_log" | sed -n '1s/:.*//p')
task_create_line=$(grep -n -m1 -- "task create" "$fake_log" | cut -d: -f1)
task_events_line=$(grep -n -m1 -- "task events" "$fake_log" | cut -d: -f1)
second_invoke_line=$(grep -n -- "function invoke" "$fake_log" | sed -n '2s/:.*//p')
deploy_remove_line=$(grep -n -m1 -- "function deploy remove" "$fake_log" | cut -d: -f1)
function_delete_line=$(grep -n -m1 -- "function delete" "$fake_log" | cut -d: -f1)
if [[ -z $first_invoke_line || -z $second_invoke_line ||
      $first_invoke_line -ge $task_create_line ||
      $task_create_line -ge $task_events_line ||
      $task_events_line -ge $second_invoke_line ||
      $second_invoke_line -ge $deploy_remove_line ||
      $deploy_remove_line -ge $function_delete_line ]]; then
    printf 'Workflow stages or cleanup commands are out of order\n' >&2
    exit 1
fi

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
if FAKE_MALFORMED_CREATE=true run_workflow >/dev/null 2>&1; then
    printf 'Expected missing Function IDs to fail the workflow\n' >&2
    exit 1
fi
if grep -q -- "function deploy create" "$fake_log"; then
    printf 'A malformed Function create response must not be deployed\n' >&2
    exit 1
fi

reset_fake
if FAKE_EMPTY_FUNCTION_IDS=true run_workflow >/dev/null 2>&1; then
    printf 'Expected empty Function IDs to fail the workflow\n' >&2
    exit 1
fi
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

printf 'function-task-pipeline tests passed\n'
