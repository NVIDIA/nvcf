#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral test for the OpenBao bootstrap functions in deploy.sh.
#
# The post-install hook Job retries its own pod (backoffLimit defaults to 6, so
# up to 7 attempts per helm install), which means initialize_cluster and
# unseal_cluster both have to survive being re-run against a cluster an earlier
# attempt already changed. A test that only walked the clean-install path would
# not notice a regression here, so each case drives the real functions against a
# stub cluster and asserts the retry-specific behavior directly: no second
# `bao operator init`, no peer join before the primary reports ha_mode=active,
# a transient raft challenge 500 retried instead of fatal, and already-joined
# peers left alone.
#
# Both deploy.sh copies carry the same bootstrap logic and are expected to stay
# in sync, so the whole suite runs against each.
#
# Requires bash, jq, and coreutils. No cluster, no network.
# Run: deploy/helm/openbao/tests/bootstrap/test-deploy-bootstrap.sh
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
openbao_dir="$(cd "${script_dir}/../.." && pwd)"

namespace="vault-system"
statefulset="openbao-server"
primary="${statefulset}-0"

fail=0
workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

# Sourcing deploy.sh outright would run the installer, so lift out the block
# that is nothing but function definitions: everything from the first helper
# down to the line where the main flow begins.
extract_functions() {
    local src="$1" log_sh="$2" out="$3"
    # The bootstrap functions log through the helpers each copy sources at
    # startup, so bring those along. log.sh has no trailing newline, hence the
    # explicit separator.
    cat "${log_sh}" >"${out}"
    printf '\n' >>"${out}"
    sed -n '/^# Helper function to get root token$/,/^log_section "Deploying OpenBao cluster/p' "${src}" |
        sed '$d' >>"${out}"
    if ! grep -q '^unseal_cluster() {' "${out}" || ! grep -q '^log_warn()' "${out}"; then
        printf 'FAIL: could not extract functions from %s\n' "${src}"
        exit 1
    fi
}

# A stub kubectl backed by files under $STATE. It records every invocation so a
# test can assert both what the bootstrap did and the order it did it in.
stub_bin="${workdir}/bin"
mkdir -p "${stub_bin}"
cat >"${stub_bin}/kubectl" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "$*" >>"${STATE}/calls"

state() { cat "${STATE}/$1" 2>/dev/null || true; }

verb="${1:-}"
shift || true

case "${verb}" in
get)
    case "${1:-}" in
    pod) printf 'true' ;;
    secret)
        # Real kubectl emits the base64 from the secret; callers pipe it
        # through `base64 -d`. An absent or empty value must decode to "".
        case "${2:-}" in
        *-unseal) printf '%s' "$(state unseal_key)" | base64 | tr -d '\n' ;;
        *-root-token) printf '%s' "$(state root_token)" | base64 | tr -d '\n' ;;
        esac
        ;;
    esac
    ;;
patch | create) ;; # recorded above; nothing else to model
exec)
    pod="${1:-}"
    while [ "$#" -gt 0 ] && [ "${1}" != "--" ]; do shift; done
    shift || true
    [ "${1:-}" = "bao" ] || exit 0
    shift
    case "${1:-}:${2:-}" in
    status:*)
        sealed="$(state "sealed.${pod}")"
        initialized="$(state initialized)"
        ha_mode="standby"
        if [ "${pod}" = "${STATE_PRIMARY}" ]; then
            # Report standby until the configured number of polls has elapsed,
            # so a caller that joins peers on a fixed sleep is caught.
            polls="$(state "polls.${pod}")"
            polls=$((${polls:-0} + 1))
            printf '%s' "${polls}" >"${STATE}/polls.${pod}"
            [ "${polls}" -gt "$(state active_after)" ] && ha_mode="active"
        fi
        printf '{"initialized":%s,"sealed":%s,"ha_mode":"%s"}\n' \
            "${initialized:-false}" "${sealed:-true}" "${ha_mode}"
        ;;
    operator:init)
        if [ "$(state initialized)" = "true" ]; then
            echo "Error initializing: Error making API request." >&2
            echo "Code: 400. Errors:" >&2
            echo "* Vault is already initialized" >&2
            exit 2
        fi
        printf 'true' >"${STATE}/initialized"
        printf 'test-unseal-key' >"${STATE}/unseal_key"
        printf 'test-root-token' >"${STATE}/root_token"
        printf '{"unseal_keys_b64":["test-unseal-key"],"root_token":"test-root-token"}\n'
        ;;
    operator:unseal)
        if [ -z "${3:-}" ]; then
            echo "Error unsealing: no key supplied" >&2
            exit 2
        fi
        printf 'false' >"${STATE}/sealed.${pod}"
        ;;
    operator:raft)
        remaining="$(state join_fail_remaining)"
        if [ "${remaining:-0}" -gt 0 ]; then
            printf '%s' "$((remaining - 1))" >"${STATE}/join_fail_remaining"
            echo "Error joining the node to the Raft cluster: Error making API request." >&2
            echo "Code: 500. Errors:" >&2
            echo "* failed to join raft cluster: failed to get raft challenge" >&2
            exit 2
        fi
        printf 'true' >"${STATE}/joined.${pod}"
        ;;
    esac
    ;;
esac
exit 0
STUB
chmod +x "${stub_bin}/kubectl"

# A cluster that is installed but not yet initialized, with every pod sealed.
new_state() {
    local dir
    dir="$(mktemp -d -p "${workdir}")"
    printf 'false' >"${dir}/initialized"
    printf '0' >"${dir}/active_after"
    printf '0' >"${dir}/join_fail_remaining"
    : >"${dir}/calls"
    printf '%s' "${dir}"
}

# A cluster a previous Job attempt already initialized and stored keys for.
bootstrapped_state() {
    local dir
    dir="$(new_state)"
    printf 'true' >"${dir}/initialized"
    printf 'test-unseal-key' >"${dir}/unseal_key"
    printf 'test-root-token' >"${dir}/root_token"
    printf '%s' "${dir}"
}

out=""
rc=0
run_fn() { # run_fn <funcs-file> <state-dir> <function> [args...]
    local funcs="$1" state="$2"
    shift 2
    set +e
    out="$(
        export STATE="${state}" STATE_PRIMARY="${primary}" PATH="${stub_bin}:${PATH}"
        # shellcheck disable=SC1090
        source "${funcs}"
        # Advance the clock instead of actually waiting. This keeps the polling
        # loops instant while still letting their $SECONDS timeouts fire, so a
        # regression that never satisfies a wait condition fails the test
        # rather than hanging it.
        sleep() { SECONDS=$((SECONDS + ${1:-0})); }
        "$@" 2>&1
    )"
    rc=$?
    set -e
}

expect_rc() { # expect_rc <desc> <want> <got>
    if [ "$3" != "$2" ]; then
        printf 'FAIL: %s (want rc %s, got %s)\n%s\n' "$1" "$2" "$3" "${out}"
        fail=1
    fi
}

expect_calls() { # expect_calls <desc> <state> <pattern> <want-count>
    local got
    got="$(grep -c -- "$3" "$2/calls" || true)"
    if [ "${got}" != "$4" ]; then
        printf 'FAIL: %s (want %s calls matching "%s", got %s)\n' "$1" "$4" "$3" "${got}"
        fail=1
    fi
}

expect_output() { # expect_output <desc> <substring>
    case "${out}" in
    *"$2"*) ;;
    *)
        printf 'FAIL: %s (output did not contain "%s")\n%s\n' "$1" "$2" "${out}"
        fail=1
        ;;
    esac
}

run_suite() {
    local label="$1" src="$2" log_sh="$3" funcs state join_line polls
    funcs="${workdir}/funcs-${label}.sh"
    extract_functions "${src}" "${log_sh}" "${funcs}"
    printf -- '--- %s\n' "${src#"${openbao_dir}/"}"

    # A clean install initializes exactly once.
    state="$(new_state)"
    run_fn "${funcs}" "${state}" initialize_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: clean install initializes" 0 "${rc}"
    expect_calls "${label}: clean install runs bao operator init" "${state}" "bao operator init" 1

    # Re-running against that same cluster is the Job's own retry. It must skip
    # init and reuse the stored keys rather than fail on "already initialized".
    run_fn "${funcs}" "${state}" initialize_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: retry after a completed init succeeds" 0 "${rc}"
    expect_calls "${label}: retry does not re-run bao operator init" "${state}" "bao operator init" 1
    expect_output "${label}: retry reports the skip" "already initialized"

    # Initialized, but the generated keys were never persisted. Unrecoverable,
    # and it has to say so instead of unsealing with an empty key.
    state="$(new_state)"
    printf 'true' >"${state}/initialized"
    run_fn "${funcs}" "${state}" initialize_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: initialized without stored keys fails" 1 "${rc}"
    expect_output "${label}: unrecoverable keys are called out" "unrecoverable"
    expect_calls "${label}: no unseal is attempted with an empty key" "${state}" "bao operator unseal" 0

    # No peer may join before the primary reports ha_mode=active. Hold the
    # primary in standby for three polls; a fixed sleep would join immediately.
    state="$(bootstrapped_state)"
    printf '3' >"${state}/active_after"
    run_fn "${funcs}" "${state}" unseal_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: unseal succeeds once the leader goes active" 0 "${rc}"
    join_line="$(grep -n 'bao operator raft join' "${state}/calls" | head -1 | cut -d: -f1 || true)"
    if [ -z "${join_line}" ]; then
        printf 'FAIL: %s: expected a raft join call\n' "${label}"
        fail=1
    else
        polls="$(head -n "$((join_line - 1))" "${state}/calls" | grep -c "${primary} .* bao status" || true)"
        if [ "${polls}" -lt 4 ]; then
            printf 'FAIL: %s: peer joined after only %s leader polls, expected to wait for ha_mode=active\n' \
                "${label}" "${polls}"
            fail=1
        fi
    fi

    # The transient challenge 500 right after leader election is retried.
    state="$(bootstrapped_state)"
    printf '2' >"${state}/join_fail_remaining"
    run_fn "${funcs}" "${state}" unseal_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: a transient raft challenge 500 is retried" 0 "${rc}"
    # Two failures then success for the first peer, one call for the second.
    expect_calls "${label}: join retried before succeeding" "${state}" "bao operator raft join" 4

    # Peers a previous attempt already unsealed are left alone.
    state="$(bootstrapped_state)"
    printf 'false' >"${state}/sealed.${statefulset}-1"
    printf 'false' >"${state}/sealed.${statefulset}-2"
    run_fn "${funcs}" "${state}" unseal_cluster "${namespace}" "${statefulset}"
    expect_rc "${label}: rerun with peers already joined succeeds" 0 "${rc}"
    expect_calls "${label}: already-joined peers are not rejoined" "${state}" "bao operator raft join" 0
}

run_suite "helm" "${openbao_dir}/helm/scripts/deploy.sh" "${openbao_dir}/helm/scripts/log.sh"
run_suite "standalone" "${openbao_dir}/deploy.sh" "${openbao_dir}/utils/log.sh"

if [ "${fail}" -ne 0 ]; then
    printf '\nFAILED\n'
    exit 1
fi
printf '\nPASS\n'
