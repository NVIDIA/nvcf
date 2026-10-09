#!/usr/bin/env bash
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

usage() {
    cat <<'EOF'
Usage: scripts/e2e-last-cluster.sh [--algorithm NAME]... [--mode on|off]... [--no-build]

Runs the LLM API gateway against a real Stargate, two Pylons, and two
mock-dynamo backends on loopback, and reproduces the overflow cache-thrash
scenario for last_cluster_affinity:

  1. Session S ranks cluster A first and cluster B second.
  2. Two long requests fill A (Pylon max engine concurrency 2).
  3. The session's first gateway request overflows to B.
  4. A drains.
  5. The next 10 gateway requests in the session (same prompt_cache_key)
     must stay on B with the flag on, and return to A with the flag off.

Mode "on" enables STARGATE_LAST_CLUSTER_ENABLED in the gateway and
last_cluster_affinity in Stargate. Mode "off" disables both. Each
algorithm and mode pair runs against a fresh set of processes. The script
exits non-zero when any expectation fails.

Options:
  --algorithm NAME  wait-and-widen or pulsar-wait-and-widen. Repeatable.
                    Default: both.
  --mode MODE       on or off. Repeatable. Default: both.
  --no-build        Reuse existing release binaries and gateway build.

Environment:
  E2E_PORT_BASE     First loopback port, default 27100. Uses 32 ports.
  E2E_WORK_DIR      Logs and configs, default a new mktemp directory.
  E2E_KEEP_WORK_DIR Set to 1 to keep the work directory after success.

Requires cargo, go, curl, and jq.
EOF
}

stargate_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
gateway_root="$(cd "${stargate_root}/../../../invocation-plane-services/llm-api-gateway" && pwd)"
release_dir="${stargate_root}/target/release"

algorithms=()
modes=()
build=1
while [[ $# -gt 0 ]]; do
    case "$1" in
        --algorithm) algorithms+=("$2"); shift 2 ;;
        --mode) modes+=("$2"); shift 2 ;;
        --no-build) build=0; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done
[[ ${#algorithms[@]} -gt 0 ]] || algorithms=(wait-and-widen pulsar-wait-and-widen)
[[ ${#modes[@]} -gt 0 ]] || modes=(on off)
for algorithm in "${algorithms[@]}"; do
    case "${algorithm}" in
        wait-and-widen|pulsar-wait-and-widen) ;;
        *) echo "unsupported algorithm: ${algorithm}" >&2; exit 2 ;;
    esac
done
for mode in "${modes[@]}"; do
    case "${mode}" in
        on|off) ;;
        *) echo "unsupported mode: ${mode}" >&2; exit 2 ;;
    esac
done

for tool in curl jq; do
    command -v "${tool}" >/dev/null || { echo "${tool} is required" >&2; exit 2; }
done

port_base="${E2E_PORT_BASE:-27100}"
work_dir="${E2E_WORK_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/stargate-e2e-last-cluster.XXXXXX")}"
mkdir -p "${work_dir}"
gateway_bin="${work_dir}/llm-api-gateway"

model="e2e-model"
routing_key="e2e"
session_id="e2e-last-cluster-session"
follow_up_count=10
# Each mock-dynamo token takes token_delay_ms. Holders generate
# holder_tokens tokens, so they keep A full for about 3 seconds.
token_delay_ms=50
holder_tokens=60
session_tokens=2
max_engine_concurrency=2

sg_grpc=$((port_base))
sg_http=$((port_base + 1))
sg_metrics=$((port_base + 2))
sg_discovery=$((port_base + 3))
sg_reverse=$((port_base + 4))
worker_auth=$((port_base + 5))
gw_http=$((port_base + 30))
gw_metrics=$((port_base + 31))
mock_port() { echo $((port_base + 10 + 10 * $1)); }
pylon_metrics_port() { echo $((port_base + 11 + 10 * $1)); }
cluster_names=(a b)

pids=()
cleanup() {
    local pid
    for pid in "${pids[@]+"${pids[@]}"}"; do
        kill "${pid}" 2>/dev/null || true
    done
    for pid in "${pids[@]+"${pids[@]}"}"; do
        wait "${pid}" 2>/dev/null || true
    done
    pids=()
}
on_exit() {
    local status=$?
    cleanup
    if [[ ${status} -ne 0 ]]; then
        echo "FAIL: logs kept in ${work_dir}" >&2
    elif [[ "${E2E_KEEP_WORK_DIR:-0}" == "1" || -n "${E2E_WORK_DIR:-}" ]]; then
        echo "logs kept in ${work_dir}"
    else
        rm -rf "${work_dir}"
    fi
    exit "${status}"
}
trap on_exit EXIT
trap 'exit 130' INT TERM

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

port_open() {
    (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

check_ports_free() {
    local port
    for port in "$@"; do
        if port_open "${port}"; then
            fail "port ${port} is already in use; set E2E_PORT_BASE"
        fi
    done
}

wait_for() {
    local description="$1" deadline=$((SECONDS + 30))
    shift
    until "$@" >/dev/null 2>&1; do
        [[ ${SECONDS} -lt ${deadline} ]] || fail "timed out waiting for ${description}"
        sleep 0.2
    done
}

build_binaries() {
    echo "building stargate, pylon, mock-dynamo, and the worker-auth fixture (release)"
    (cd "${stargate_root}" && cargo build --locked --release -q -p stargate -p pylon -p mock-dynamo)
    echo "building llm-api-gateway"
    (cd "${gateway_root}" && go build -o "${gateway_bin}" ./cmd/llm-api-gateway)
}

write_lb_config() {
    local algorithm="$1" flag="$2" path="$3" virtual_nodes=""
    if [[ "${algorithm}" == "wait-and-widen" ]]; then
        virtual_nodes='"cache_affinity_virtual_nodes": 8,'
    fi
    cat >"${path}" <<EOF
{
  "default": "power-of-n",
  "models": {
    "${model}": {
      "algorithm": "${algorithm}",
      "seed": "e2e-seed",
      "require_cache_affinity_key": true,
      ${virtual_nodes}
      "cache_affinity_backend_selection_count": 1,
      "cache_affinity_wait_ms": 300,
      "cache_affinity_input_tokens_scale": 0.1,
      "last_cluster_affinity": ${flag}
    }
  }
}
EOF
}

start() {
    local name="$1" log="$2"
    shift 2
    "$@" >"${log}" 2>&1 &
    pids+=("$!")
}

stargate_metric_sum() {
    local name="$1" filter="$2"
    curl -sf "http://127.0.0.1:${sg_metrics}/metrics" |
        awk -v name="${name}" -v filter="${filter}" '
            index($0, name "{") == 1 && index($0, filter) > 0 { sum += $NF }
            END { printf "%d\n", sum }'
}

mock_count() {
    curl -sf "http://127.0.0.1:$(mock_port "$1")/test-control" |
        jq --arg model "${model}" '[.counters[]
            | select(.endpoint == "chat_completions" and .model == $model and .request_class == "api_gateway")
            | .count] | add // 0'
}

# Prints the cluster whose mock-dynamo served the request run by "$@".
served_by() {
    local before_a before_b after_a after_b
    before_a=$(mock_count 0)
    before_b=$(mock_count 1)
    "$@"
    after_a=$(mock_count 0)
    after_b=$(mock_count 1)
    case "$((after_a - before_a)):$((after_b - before_b))" in
        1:0) echo "cluster-a" ;;
        0:1) echo "cluster-b" ;;
        *) fail "could not attribute request: a +$((after_a - before_a)), b +$((after_b - before_b))" ;;
    esac
}

affinity_key() {
    printf 'mt:v1:session:%s' "$(printf '%s' "${session_id}" | shasum -a 256 | cut -d' ' -f1)"
}

# Sends a direct Stargate request with the session's affinity key and no
# last-cluster hint. Holders use this to occupy the session's rank-1 cluster.
stargate_request() {
    local request_id="$1" tokens="$2"
    curl -sf -N -o /dev/null "http://127.0.0.1:${sg_http}/v1/chat/completions" \
        -H 'content-type: application/json' \
        -H "x-request-id: ${request_id}" \
        -H "x-routing-key: ${routing_key}" \
        -H "x-model: ${model}" \
        -H 'x-input-tokens: 8' \
        -H "x-cache-affinity-key: $(affinity_key)" \
        -d "{\"model\":\"${model}\",\"stream\":true,\"max_tokens\":${tokens},\"messages\":[{\"role\":\"user\",\"content\":\"hold\"}]}"
}

# Sends a gateway request in session S. The gateway derives the affinity key
# from prompt_cache_key and adds the last-cluster hint when enabled.
gateway_request() {
    local status
    status=$(curl -s -N -o "${run_dir}/last-gateway-response.txt" -w '%{http_code}' \
        "http://127.0.0.1:${gw_http}/v1/chat/completions" \
        -H 'content-type: application/json' \
        -d "{\"model\":\"${routing_key}/${model}\",\"stream\":true,\"max_tokens\":${session_tokens},\"prompt_cache_key\":\"${session_id}\",\"messages\":[{\"role\":\"user\",\"content\":\"turn\"}]}")
    [[ "${status}" == "200" ]] || fail "gateway request returned ${status}: $(cat "${run_dir}/last-gateway-response.txt")"
}

start_stack() {
    local algorithm="$1" mode="$2" flag=false gateway_flag=false index name
    [[ "${mode}" == "on" ]] && flag=true && gateway_flag=true
    check_ports_free "${sg_grpc}" "${sg_http}" "${sg_metrics}" "${sg_discovery}" \
        "${worker_auth}" "${gw_http}" "${gw_metrics}" \
        "$(mock_port 0)" "$(mock_port 1)" "$(pylon_metrics_port 0)" "$(pylon_metrics_port 1)"
    write_lb_config "${algorithm}" "${flag}" "${run_dir}/lb-config.json"

    start worker-auth "${run_dir}/worker-auth.log" \
        "${release_dir}/stargate-worker-auth-gateway" \
        --listen-addr "127.0.0.1:${worker_auth}" \
        --worker "token-a=${routing_key}" --worker "token-b=${routing_key}"
    wait_for "worker-auth fixture" port_open "${worker_auth}"
    start stargate "${run_dir}/stargate.log" env RUST_LOG=info \
        "${release_dir}/stargate" \
        --stargate-id e2e-stargate \
        --listen-addr "127.0.0.1:${sg_grpc}" \
        --http-listen-addr "127.0.0.1:${sg_http}" \
        --model-discovery-listen-addr "127.0.0.1:${sg_discovery}" \
        --advertise-addr "127.0.0.1:${sg_grpc}" \
        --stargate-discovery-dns-name localhost \
        --disable-dns-discovery \
        --metrics-port "${sg_metrics}" \
        --lb-config-path "${run_dir}/lb-config.json" \
        --backend-connectivity=reverse \
        --reverse-tunnel-listen-addr "127.0.0.1:${sg_reverse}" \
        --advertised-hostname-template localhost \
        --worker-auth-endpoint "http://127.0.0.1:${worker_auth}" \
        --quic-insecure

    for index in 0 1; do
        name="${cluster_names[${index}]}"
        start "mock-${name}" "${run_dir}/mock-${name}.log" \
            "${release_dir}/mock-dynamo" \
            --http-listen-addr "127.0.0.1:$(mock_port "${index}")" \
            --model-name "${model}" \
            --num-tokens "${holder_tokens}" \
            --token-delay-ms "${token_delay_ms}"
    done
    wait_for "stargate metrics" curl -sf "http://127.0.0.1:${sg_metrics}/metrics"
    for index in 0 1; do
        name="${cluster_names[${index}]}"
        wait_for "mock-dynamo ${name}" curl -sf "http://127.0.0.1:$(mock_port "${index}")/health"
        start "pylon-${name}" "${run_dir}/pylon-${name}.log" env RUST_LOG=info \
            "${release_dir}/pylon" \
            --upstream-http-base-url "http://127.0.0.1:$(mock_port "${index}")" \
            --model-name "${model}" \
            --stargate-address "127.0.0.1:${sg_grpc}" \
            --inference-server-id "pylon-${name}" \
            --cluster-id "cluster-${name}" \
            --auth-token "token-${name}" \
            --backend-connectivity reverse \
            --quic-insecure \
            --kv-cache-stats-path /kv-cache/stats \
            --min-update-interval-ms 100 \
            --disable-bringup \
            --active-canary-interval-ms=0 \
            --initial-input-tps 1000 \
            --max-engine-concurrency "${max_engine_concurrency}" \
            --metrics-host 127.0.0.1 \
            --metrics-port "$(pylon_metrics_port "${index}")"
    done

    start gateway "${run_dir}/gateway.log" env \
        NVCF_GATEWAY_ADDR="127.0.0.1:${gw_http}" \
        METRICS_PORT="${gw_metrics}" \
        NVCF_REGION=local \
        LOCAL_FUNCTION_ID="${routing_key}" \
        NVCF_DEFAULT_MODEL="${model}" \
        OLRIC_ENABLED=false \
        RATE_LIMIT_ENABLED=false \
        STARGATE_URL="http://127.0.0.1:${sg_http}" \
        STARGATE_LAST_CLUSTER_ENABLED="${gateway_flag}" \
        "${gateway_bin}"
    wait_for "gateway readiness" curl -sf "http://127.0.0.1:${gw_http}/readyz"
    wait_for "both pylon reverse tunnels" both_tunnels_connected
    # Both clusters must be routable. A warm-up through Stargate with a
    # throwaway affinity key confirms the routing target exists.
    wait_for "stargate routing" curl -sf -N -o /dev/null \
        "http://127.0.0.1:${sg_http}/v1/chat/completions" \
        -H 'content-type: application/json' -H 'x-request-id: e2e-warmup' \
        -H "x-routing-key: ${routing_key}" -H "x-model: ${model}" \
        -H 'x-input-tokens: 1' -H 'x-cache-affinity-key: e2e-warmup' \
        -d "{\"model\":\"${model}\",\"stream\":true,\"max_tokens\":1,\"messages\":[{\"role\":\"user\",\"content\":\"warm\"}]}"
}

both_tunnels_connected() {
    grep -q "reverse tunnel connected" "${run_dir}/pylon-a.log" &&
        grep -q "reverse tunnel connected" "${run_dir}/pylon-b.log"
}

run_case() {
    local algorithm="$1" mode="$2"
    run_dir="${work_dir}/${algorithm}-${mode}"
    mkdir -p "${run_dir}"
    echo
    echo "=== ${algorithm}, last-cluster ${mode} ==="
    start_stack "${algorithm}" "${mode}"
    # Let both Pylons publish a stats update after the warm-up.
    sleep 1

    local holder_pids=() rank_one rank_two first overflow follow_ups=()
    local session_before returning_before new_fallback_before
    session_before=$(stargate_metric_sum stargate_routing_session_selections_total "model=\"${model}\"")

    # Fill rank 1. Each holder is a direct Stargate request in session S with
    # no hint, so it lands on the session's rank-1 cluster while it has room.
    local before_a before_b
    before_a=$(mock_count 0)
    before_b=$(mock_count 1)
    for index in $(seq 1 "${max_engine_concurrency}"); do
        stargate_request "e2e-holder-${index}" "${holder_tokens}" &
        holder_pids+=("$!")
        sleep 0.3
    done
    sleep 0.3
    local holders_a=$(($(mock_count 0) - before_a)) holders_b=$(($(mock_count 1) - before_b))
    if [[ ${holders_a} -eq ${max_engine_concurrency} && ${holders_b} -eq 0 ]]; then
        rank_one="cluster-a"
        rank_two="cluster-b"
    elif [[ ${holders_b} -eq ${max_engine_concurrency} && ${holders_a} -eq 0 ]]; then
        rank_one="cluster-b"
        rank_two="cluster-a"
    else
        fail "holders did not land on one cluster: a=${holders_a} b=${holders_b}"
    fi
    echo "session rank 1 (A): ${rank_one}; rank 2 (B): ${rank_two}; A is full"

    returning_before=$(stargate_metric_sum stargate_routing_session_selections_total 'session_state="returning"')
    new_fallback_before=$(stargate_metric_sum stargate_routing_session_selections_total 'selection="fallback",session_state="new"')

    first=$(served_by gateway_request)
    echo "first gateway request (no hint): ${first}"
    [[ "${first}" == "${rank_two}" ]] || fail "first request should overflow to ${rank_two}, got ${first}"
    overflow="${first}"

    for pid in "${holder_pids[@]}"; do
        wait "${pid}" || fail "holder request failed"
    done
    # A drained. Give Pylon time to publish the idle stats.
    sleep 1
    echo "A drained"

    local index
    for index in $(seq 1 "${follow_up_count}"); do
        follow_ups+=("$(served_by gateway_request)")
        # The gateway stores the last cluster in the background.
        sleep 0.1
    done
    echo "follow-up clusters: ${follow_ups[*]}"

    local expected on_b=0 on_a=0 cluster
    for cluster in "${follow_ups[@]}"; do
        if [[ "${cluster}" == "${overflow}" ]]; then on_b=$((on_b + 1)); else on_a=$((on_a + 1)); fi
    done
    echo "follow-ups on B: ${on_b}/${follow_up_count}; on A: ${on_a}/${follow_up_count}"

    local returning new_fallback session_total
    returning=$(($(stargate_metric_sum stargate_routing_session_selections_total 'session_state="returning"') - returning_before))
    new_fallback=$(($(stargate_metric_sum stargate_routing_session_selections_total 'selection="fallback",session_state="new"') - new_fallback_before))
    session_total=$(($(stargate_metric_sum stargate_routing_session_selections_total "model=\"${model}\"") - session_before))
    echo "stargate session selections: returning=${returning} new-fallback=${new_fallback} total=${session_total}"
    curl -sf "http://127.0.0.1:${gw_metrics}/metrics" | grep -E '^llm_api_gateway_last_cluster_(lookups|writes)_total' | grep -v ' 0$' || true

    if [[ "${mode}" == "on" ]]; then
        expected="${overflow}"
        [[ ${on_b} -eq ${follow_up_count} ]] || fail "flag on: expected ${follow_up_count}/${follow_up_count} follow-ups on ${expected}"
        [[ ${returning} -eq ${follow_up_count} ]] || fail "flag on: expected ${follow_up_count} returning selections, got ${returning}"
        [[ ${new_fallback} -eq 1 ]] || fail "flag on: expected 1 new fallback selection, got ${new_fallback}"
    else
        expected="${rank_one}"
        [[ ${on_a} -eq ${follow_up_count} ]] || fail "flag off: expected ${follow_up_count}/${follow_up_count} follow-ups on ${expected}"
        [[ ${session_total} -eq 0 ]] || fail "flag off: expected no session selections, got ${session_total}"
    fi
    echo "PASS: ${algorithm}, last-cluster ${mode}"
    cleanup
}

if [[ ${build} -eq 1 ]]; then
    build_binaries
else
    [[ -x "${gateway_bin}" ]] || (cd "${gateway_root}" && go build -o "${gateway_bin}" ./cmd/llm-api-gateway)
fi
for binary in stargate pylon mock-dynamo stargate-worker-auth-gateway; do
    [[ -x "${release_dir}/${binary}" ]] || fail "missing ${release_dir}/${binary}; run without --no-build"
done

for algorithm in "${algorithms[@]}"; do
    for mode in "${modes[@]}"; do
        run_case "${algorithm}" "${mode}"
    done
done
echo
echo "all cases passed"
