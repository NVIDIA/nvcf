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
Usage: scripts/e2e-last-cluster.sh [--no-build] [--decisions-only]

Runs the LLM API gateway against a real Stargate, two Pylons, and two
mock-dynamo backends on loopback, and reproduces the overflow cache-thrash
scenario for last_cluster_affinity:

  1. Session S ranks cluster A first and cluster B second.
  2. Two long requests fill A (Pylon max engine concurrency 2).
  3. The session's first gateway request overflows to B.
  4. A drains.
  5. The next 10 gateway requests in the session (same prompt_cache_key)
     must stay on B with the flag on, and return to A with the flag off.

The scenario runs for wait-and-widen and pulsar-wait-and-widen, each with the
flag on (STARGATE_LAST_CLUSTER_ENABLED in the gateway and
last_cluster_affinity in Stargate) and off, against a fresh set of processes.
After those cases it runs a decision-route case: POST /v1/decisions and
POST /v1/systemone through the gateway, Stargate, a Pylon, and mock-dynamo. The
gateway authenticates against stargate-dev-auth, which declares both routes in
the model's uris. The case checks that the 200 body, the TypeSafe-Danger-Zone-Images
vendor header, and a 13 MB base64 body reach the backend unchanged, and that a
backend 429 (with Retry-After) and a backend 400 come back unchanged and are not
retried.

The script exits non-zero when any expectation fails.

Options:
  --no-build        Reuse the binaries in target/release. To run without
                    the gateway source or go, copy a prebuilt
                    llm-api-gateway there first.
  --decisions-only  Run only the decision-route case.

Environment:
  E2E_PORT_BASE     First loopback port, default 27100. Uses 32 ports.
  E2E_WORK_DIR      Logs and configs, kept after the run. Default: a new
                    mktemp directory, removed on success.

Requires curl, jq, and sha256sum or shasum. Building also needs cargo and go.
EOF
}

build=1
decisions_only=0
for arg in "$@"; do
    case "${arg}" in
        --no-build) build=0 ;;
        --decisions-only) decisions_only=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: ${arg}" >&2; usage >&2; exit 2 ;;
    esac
done

for tool in curl jq; do
    command -v "${tool}" >/dev/null || { echo "${tool} is required" >&2; exit 2; }
done

stargate_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
gateway_root="${stargate_root}/../../../invocation-plane-services/llm-api-gateway"
release_dir="${stargate_root}/target/release"
port_base="${E2E_PORT_BASE:-27100}"
work_dir="${E2E_WORK_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/stargate-e2e-last-cluster.XXXXXX")}"
mkdir -p "${work_dir}"

model="e2e-model"
routing_key="e2e"
# Both Pylons register with one worker token. The dev auth fixture also
# requires Stargate's service token.
worker_token="e2e-worker-token"
service_token="e2e-service-token"
session_id="e2e-last-cluster-session"
# The decision-route case sends this client token to the gateway. The gateway
# presents the service token to stargate-dev-auth, which answers the
# invocation RPC and declares the routes each model serves.
client_token="e2e-client-token"
nca_id="e2e-nca"
# 1 makes start_stack point the gateway at stargate-dev-auth.
gateway_auth=0
follow_up_count=10
# Each mock-dynamo token takes token_delay_ms. Holders generate
# holder_tokens tokens, so they keep A full for about 3 seconds.
token_delay_ms=50
holder_tokens=60
max_engine_concurrency=2

sg_grpc=$((port_base))
sg_http=$((port_base + 1))
sg_metrics=$((port_base + 2))
sg_discovery=$((port_base + 3))
sg_reverse=$((port_base + 4))
worker_auth=$((port_base + 5))
worker_auth_http=$((port_base + 6))
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
    elif [[ -z "${E2E_WORK_DIR:-}" ]]; then
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

wait_for() {
    local description="$1" deadline=$((SECONDS + 30))
    shift
    until "$@" >/dev/null 2>&1; do
        [[ ${SECONDS} -lt ${deadline} ]] || fail "timed out waiting for ${description}"
        sleep 0.2
    done
}

start() {
    local log="$1"
    shift
    "$@" >"${log}" 2>&1 &
    pids+=("$!")
}

mock_count() {
    curl -sf "http://127.0.0.1:$(mock_port "$1")/test-control" |
        jq --arg model "${model}" '[.counters[]
            | select(.endpoint == "chat_completions" and .model == $model and .request_class == "api_gateway")
            | .count] | add // 0'
}

# Prints the cluster whose mock-dynamo served the request run by "$@".
served_by() {
    local before_a before_b delta
    before_a=$(mock_count 0)
    before_b=$(mock_count 1)
    "$@"
    delta="$(($(mock_count 0) - before_a)):$(($(mock_count 1) - before_b))"
    case "${delta}" in
        1:0) echo "cluster-a" ;;
        0:1) echo "cluster-b" ;;
        *) fail "could not attribute request (a:b delta ${delta})" ;;
    esac
}

affinity_key() {
    local hasher=(sha256sum)
    command -v sha256sum >/dev/null || hasher=(shasum -a 256)
    printf 'mt:v1:session:%s' "$(printf '%s' "${session_id}" | "${hasher[@]}" | cut -d' ' -f1)"
}

# Sends a direct Stargate request with the given affinity key and no
# last-cluster hint.
stargate_request() {
    local request_id="$1" affinity="$2" tokens="$3"
    curl -sf -N -o /dev/null "http://127.0.0.1:${sg_http}/v1/chat/completions" \
        -H 'content-type: application/json' \
        -H "x-request-id: ${request_id}" \
        -H "x-routing-key: ${routing_key}" \
        -H "x-model: ${model}" \
        -H 'x-input-tokens: 8' \
        -H "x-cache-affinity-key: ${affinity}" \
        -d "{\"model\":\"${model}\",\"stream\":true,\"max_tokens\":${tokens},\"messages\":[{\"role\":\"user\",\"content\":\"hold\"}]}"
}

# Sends a gateway request in session S. The gateway derives the affinity key
# from prompt_cache_key and adds the last-cluster hint when enabled.
gateway_request() {
    local status
    status=$(curl -s -N -o "${run_dir}/last-gateway-response.txt" -w '%{http_code}' \
        "http://127.0.0.1:${gw_http}/v1/chat/completions" \
        -H 'content-type: application/json' \
        -d "{\"model\":\"${routing_key}/${model}\",\"stream\":true,\"max_tokens\":2,\"prompt_cache_key\":\"${session_id}\",\"messages\":[{\"role\":\"user\",\"content\":\"turn\"}]}")
    [[ "${status}" == "200" ]] || fail "gateway request returned ${status}: $(cat "${run_dir}/last-gateway-response.txt")"
}

both_tunnels_connected() {
    grep -q "reverse tunnel connected" "${run_dir}/pylon-a.log" &&
        grep -q "reverse tunnel connected" "${run_dir}/pylon-b.log"
}

start_stack() {
    local algorithm="$1" flag="$2" index name port
    for port in "${sg_grpc}" "${sg_http}" "${sg_metrics}" "${sg_discovery}" "${worker_auth}" \
        "${worker_auth_http}" \
        "${gw_http}" "${gw_metrics}" "$(mock_port 0)" "$(mock_port 1)" \
        "$(pylon_metrics_port 0)" "$(pylon_metrics_port 1)"; do
        if port_open "${port}"; then
            fail "port ${port} is already in use; set E2E_PORT_BASE"
        fi
    done
    cat >"${run_dir}/lb-config.json" <<EOF
{
  "default": "power-of-n",
  "models": {
    "${model}": {
      "algorithm": "${algorithm}",
      "seed": "e2e-seed",
      "cache_affinity_backend_selection_count": 1,
      "cache_affinity_wait_ms": 300,
      "cache_affinity_input_tokens_scale": 0.1,
      "last_cluster_affinity": ${flag}
    }
  }
}
EOF

    cat >"${run_dir}/worker-auth.json" <<EOF
{
  "serviceToken": "${service_token}",
  "workerToken": "${worker_token}",
  "routingKey": "${routing_key}",
  "invocation": {
    "clientToken": "${client_token}",
    "ncaId": "${nca_id}",
    "modelSpecs": {
      "${model}": {"uris": ["/v1/chat/completions", "/v1/decisions", "/v1/systemone"]}
    }
  }
}
EOF
    cat >"${run_dir}/stargate-secrets.json" <<EOF
{"authToken": "${service_token}"}
EOF
    cat >"${run_dir}/gateway-secrets.json" <<EOF
{"nvcfApiToken": "${service_token}"}
EOF
    local gateway_auth_env=()
    if [[ ${gateway_auth} -eq 1 ]]; then
        gateway_auth_env=(
            NVCF_GRPC_ADDR="127.0.0.1:${worker_auth}"
            NVCF_GRPC_INSECURE=true
            SECRETS_PATH="${run_dir}/gateway-secrets.json"
        )
    fi
    start "${run_dir}/worker-auth.log" \
        "${release_dir}/stargate-dev-auth" \
        --config-path "${run_dir}/worker-auth.json" \
        --grpc-listen-addr "127.0.0.1:${worker_auth}" \
        --http-listen-addr "127.0.0.1:${worker_auth_http}"
    wait_for "worker-auth fixture" port_open "${worker_auth}"
    start "${run_dir}/stargate.log" env RUST_LOG=info \
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
        --secrets-path "${run_dir}/stargate-secrets.json" \
        --quic-insecure

    for index in 0 1; do
        name="${cluster_names[${index}]}"
        start "${run_dir}/mock-${name}.log" \
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
        start "${run_dir}/pylon-${name}.log" env RUST_LOG=info \
            "${release_dir}/pylon" \
            --upstream-http-base-url "http://127.0.0.1:$(mock_port "${index}")" \
            --model-name "${model}" \
            --stargate-address "127.0.0.1:${sg_grpc}" \
            --inference-server-id "pylon-${name}" \
            --cluster-id "cluster-${name}" \
            --auth-token "${worker_token}" \
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

    start "${run_dir}/gateway.log" env \
        NVCF_GATEWAY_ADDR="127.0.0.1:${gw_http}" \
        METRICS_PORT="${gw_metrics}" \
        NVCF_REGION=local \
        LOCAL_FUNCTION_ID="${routing_key}" \
        NVCF_DEFAULT_MODEL="${model}" \
        OLRIC_ENABLED=false \
        RATE_LIMIT_ENABLED=false \
        STARGATE_URL="http://127.0.0.1:${sg_http}" \
        STARGATE_LAST_CLUSTER_ENABLED="${flag}" \
        "${gateway_auth_env[@]+"${gateway_auth_env[@]}"}" \
        "${release_dir}/llm-api-gateway"
    wait_for "gateway readiness" curl -sf "http://127.0.0.1:${gw_http}/readyz"
    wait_for "both pylon reverse tunnels" both_tunnels_connected
    wait_for "stargate routing" stargate_request e2e-warmup e2e-warmup 1
    # Let both Pylons publish a stats update after the warm-up.
    sleep 1
}

run_case() {
    local algorithm="$1" flag="$2"
    run_dir="${work_dir}/${algorithm}-${flag}"
    mkdir -p "${run_dir}"
    echo "=== ${algorithm}, last_cluster_affinity=${flag} ==="
    start_stack "${algorithm}" "${flag}"

    # Fill rank 1. Holders are direct Stargate requests in session S with no
    # hint, so they land on the session's rank-1 cluster while it has room.
    local before_a before_b holders index pid holder_pids=()
    before_a=$(mock_count 0)
    before_b=$(mock_count 1)
    for index in $(seq 1 "${max_engine_concurrency}"); do
        stargate_request "e2e-holder-${index}" "$(affinity_key)" "${holder_tokens}" &
        holder_pids+=("$!")
        sleep 0.3
    done
    sleep 0.3
    local rank_one rank_two
    holders="$(($(mock_count 0) - before_a)):$(($(mock_count 1) - before_b))"
    case "${holders}" in
        "${max_engine_concurrency}:0") rank_one=cluster-a; rank_two=cluster-b ;;
        "0:${max_engine_concurrency}") rank_one=cluster-b; rank_two=cluster-a ;;
        *) fail "holders did not land on one cluster (a:b ${holders})" ;;
    esac

    local served expected
    served=$(served_by gateway_request)
    [[ "${served}" == "${rank_two}" ]] || fail "first request should overflow to ${rank_two}, got ${served}"

    for pid in "${holder_pids[@]}"; do
        wait "${pid}" || fail "holder request failed"
    done
    # A drained. Give Pylon time to publish the idle stats.
    sleep 1

    if [[ "${flag}" == true ]]; then
        expected="${rank_two}"
    else
        expected="${rank_one}"
    fi
    for index in $(seq 1 "${follow_up_count}"); do
        served=$(served_by gateway_request)
        [[ "${served}" == "${expected}" ]] || fail "follow-up ${index} went to ${served}, expected ${expected}"
        # The gateway stores the last cluster in the background.
        sleep 0.1
    done
    echo "PASS: ${algorithm}, last_cluster_affinity=${flag}"
    cleanup
}

# Total requests the mock-dynamo backends counted for an endpoint.
mock_endpoint_total() {
    local endpoint="$1" index total=0 count
    for index in 0 1; do
        count=$(curl -sf "http://127.0.0.1:$(mock_port "${index}")/test-control" |
            jq --arg endpoint "${endpoint}" --arg model "${model}" '[.counters[]
                | select(.endpoint == $endpoint and .model == $model and .request_class == "api_gateway")
                | .count] | add // 0')
        total=$((total + count))
    done
    echo "${total}"
}

# Writes a decision request body with a base64 image of image_bytes bytes.
write_decision_body() {
    local route="$1" image_bytes="$2" file="$3" input_field questions
    if [[ "${route}" == decisions ]]; then
        input_field=input
        questions='[{"type":"predicate","name":"cat","text":"is there a cat?"}]'
    else
        input_field=state
        questions='{"cat":{"type":"noul","text":"is there a cat?"}}'
    fi
    {
        printf '{"model":"%s/%s","%s":[{"type":"input_image","image_url":"data:image/png;base64,' \
            "${routing_key}" "${model}" "${input_field}"
        head -c "${image_bytes}" /dev/zero | tr '\0' 'A'
        printf '"}],"questions":%s}' "${questions}"
    } >"${file}"
}

# POSTs a decision request through the gateway and prints the status code.
# Response headers go to ${run_dir}/decision-headers.txt and the body to
# ${run_dir}/decision-body.txt.
decision_request() {
    local route="$1" query="$2" body_file="$3"
    curl -s -o "${run_dir}/decision-body.txt" -D "${run_dir}/decision-headers.txt" \
        -w '%{http_code}' --max-time 60 \
        "http://127.0.0.1:${gw_http}/v1/${route}${query}" \
        -H 'content-type: application/json' \
        -H "authorization: Bearer ${client_token}" \
        -H 'TypeSafe-Danger-Zone-Images: true' \
        -H 'X-Vendor-Test: v' \
        --data-binary "@${body_file}"
}

expect_decision() {
    local description="$1" expected_status="$2" status="$3"
    [[ "${status}" == "${expected_status}" ]] ||
        fail "${description}: expected ${expected_status}, got ${status}: $(head -c 300 "${run_dir}/decision-body.txt")"
}

run_decision_case() {
    run_dir="${work_dir}/decisions"
    mkdir -p "${run_dir}"
    echo "=== decision routes through gateway, Stargate, Pylon, mock-dynamo ==="
    gateway_auth=1
    start_stack wait-and-widen false

    local route small large status before after
    for route in decisions systemone; do
        small="${run_dir}/${route}-small.json"
        large="${run_dir}/${route}-large.json"
        write_decision_body "${route}" 64 "${small}"
        write_decision_body "${route}" $((13 * 1024 * 1024)) "${large}"

        before=$(mock_endpoint_total "${route}")
        status=$(decision_request "${route}" "" "${small}")
        expect_decision "${route} 200" 200 "${status}"
        jq -e --arg model "${model}" '.model == $model
            and .echo["typesafe-danger-zone-images"] == "true"
            and .echo["x-vendor-test"] == "v"
            and (.echo["x-input-tokens"] | tonumber) > 0
            and (.answers | length) > 0' "${run_dir}/decision-body.txt" >/dev/null ||
            fail "${route} 200 body or echoed headers wrong: $(head -c 400 "${run_dir}/decision-body.txt")"
        grep -qi '^x-mock-echo-typesafe-danger-zone-images: true' "${run_dir}/decision-headers.txt" ||
            fail "${route}: TypeSafe-Danger-Zone-Images did not reach the backend"

        status=$(decision_request "${route}" "" "${large}")
        expect_decision "${route} 13 MB body" 200 "${status}"
        jq -e '.echo.body_len > 13000000 and .echo["typesafe-danger-zone-images"] == "true"' \
            "${run_dir}/decision-body.txt" >/dev/null ||
            fail "${route}: 13 MB body did not arrive intact: $(head -c 300 "${run_dir}/decision-body.txt")"

        status=$(decision_request "${route}" "?mock_error=429" "${small}")
        expect_decision "${route} backend 429" 429 "${status}"
        grep -qi '^retry-after: 7' "${run_dir}/decision-headers.txt" ||
            fail "${route}: 429 lost its Retry-After header"

        status=$(decision_request "${route}" "?mock_error=400" "${small}")
        expect_decision "${route} backend 400" 400 "${status}"
        if [[ "${route}" == decisions ]]; then
            jq -e '.error.type == "invalid_request_error"' "${run_dir}/decision-body.txt" >/dev/null ||
                fail "decisions 400 body was changed: $(head -c 300 "${run_dir}/decision-body.txt")"
        else
            jq -e '.detail | type == "array"' "${run_dir}/decision-body.txt" >/dev/null ||
                fail "systemone 400 body was changed: $(head -c 300 "${run_dir}/decision-body.txt")"
        fi

        # Four requests, one backend attempt each: the 429 was not retried.
        after=$(mock_endpoint_total "${route}")
        [[ $((after - before)) -eq 4 ]] ||
            fail "${route}: expected 4 backend requests, saw $((after - before))"
        echo "PASS: ${route}"
    done
    gateway_auth=0
    cleanup
}

if [[ ${build} -eq 1 ]]; then
    mkdir -p "${release_dir}"
    echo "building llm-api-gateway, stargate, pylon, and mock-dynamo"
    (cd "${gateway_root}" && go build -o "${release_dir}/llm-api-gateway" ./cmd/llm-api-gateway)
    (cd "${stargate_root}" && cargo build --locked --release -q -p stargate -p pylon -p mock-dynamo)
fi
for binary in llm-api-gateway stargate stargate-dev-auth pylon mock-dynamo; do
    [[ -x "${release_dir}/${binary}" ]] || fail "missing ${release_dir}/${binary}; run without --no-build"
done

if [[ ${decisions_only} -eq 0 ]]; then
    for algorithm in wait-and-widen pulsar-wait-and-widen; do
        for flag in true false; do
            run_case "${algorithm}" "${flag}"
        done
    done
fi
run_decision_case
echo "all cases passed"
