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
fake_state="${test_dir}/state"
config_file="${test_dir}/config.yaml"
touch "$config_file"

cat >"$fake_cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$FAKE_LOG"

case " $* " in
    *" function create "*)
        printf 'Function created\n'
        ;;
    *" --json status "*)
        printf '%s\n' '{"currentFunction":{"functionId":"function-test","versionId":"version-test"}}'
        ;;
    *" function deploy create "*)
        printf 'Function deployed\n'
        ;;
    *" --json function invoke "*)
        printf '%s\n' '{"status":"fulfilled","responseBody":"ok"}'
        ;;
    *" --json task create "*)
        printf '%s\n' '{"task":{"id":"task-test","status":"QUEUED"}}'
        ;;
    *" --json task get "*)
        count=0
        if [[ -f $FAKE_STATE ]]; then
            count=$(<"$FAKE_STATE")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" >"$FAKE_STATE"
        if ((count == 1)); then
            printf '%s\n' '{"task":{"id":"task-test","status":"RUNNING","percentComplete":50}}'
        else
            printf '{"task":{"id":"task-test","status":"%s","percentComplete":100}}\n' \
                "${FAKE_TERMINAL_STATUS:-COMPLETED}"
        fi
        ;;
    *" --json task events "*)
        printf '%s\n' '{"events":[{"taskId":"task-test","message":"COMPLETED"}]}'
        ;;
    *" task cancel "*|*" task delete "*|*" function deploy remove "*|*" function delete "*)
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
    FAKE_STATE=$fake_state \
    FAKE_TERMINAL_STATUS=${FAKE_TERMINAL_STATUS:-COMPLETED} \
    NVCF_CLI_BIN=$fake_cli \
    NVCF_CLI_CONFIG=$config_file \
    FUNCTION_IMAGE=registry.example/function:test \
    TASK_IMAGE=registry.example/task:test \
    WORKFLOW_ID=test-run \
    POLL_INTERVAL_SECONDS=0 \
    TASK_TIMEOUT_SECONDS=5 \
    "$script_dir/run.sh"
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

success_output=$(run_workflow)
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
    "task events" \
    "function deploy remove" \
    "function delete"

: >"$fake_log"
rm -f "$fake_state"
if FAKE_TERMINAL_STATUS=ERRORED run_workflow >/dev/null 2>&1; then
    printf 'Expected an errored task to fail the workflow\n' >&2
    exit 1
fi

grep -q -- "task delete task-test" "$fake_log"
grep -q -- "function deploy remove" "$fake_log"
grep -q -- "function delete function-test version-test" "$fake_log"

printf 'function-task-pipeline tests passed\n'
