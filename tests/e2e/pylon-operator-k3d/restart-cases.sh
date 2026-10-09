#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Restart and failure cases of the Pylon Operator k3d end-to-end test. run.sh
# sources this file before main and calls run_restart_cases; it is not run on
# its own. It uses run.sh's settings, globals and helpers, such as kc, gw,
# expect, hold, conditions_are, start_port_forward and step_done. run.sh calls
# undo_faults from on_exit, delete_second_endpoint from reset_endpoint and
# cleanup, and registry_is and chat_status_is in its backend scaled to zero
# section.
# shellcheck shell=bash

readonly OPERATOR_DEPLOYMENT="${OPERATOR_RELEASE}"
readonly SAMPLE_SELECTOR="app.kubernetes.io/name=openai-compatible-sample"
# A second InferenceEndpoint applied while the operator is down (C3b).
readonly ENDPOINT_B="${ENDPOINT}-b"

STREAM_PID=""
# Faults that on_exit undoes when a run stops in the middle of a case.
OPERATOR_SCALED_DOWN=0

# ---------------------------------------------------------------------------
# Restart and failure checks. Assertions name the view they read: "stack" is
# the router's view through the gateway, "operator" is the CR status.

# registry_is HEALTH REGISTERED HEALTHY: the /v1/registry entry of MODEL.
registry_is() {
  local out="${E2E_WORK_DIR}/registry.json" code body
  code="$(gw GET /v1/registry "${out}")"
  body="$(compact "${out}" 2>/dev/null || true)"
  LAST_OBSERVED="HTTP ${code} ${body:0:300}"
  [ "${code}" = "200" ] || return 1
  printf '%s' "${body}" | grep -q "\"model\":\"${MODEL}\",\"health\":\"$1\"" || return 1
  printf '%s' "${body}" | grep -Eq "\"registeredServers\":$2,\"healthyServers\":$3[,}]"
}

registry_status_is() {
  local out="${E2E_WORK_DIR}/registry.json" code
  code="$(gw GET /v1/registry "${out}")"
  LAST_OBSERVED="HTTP ${code} $(head -c 200 "${out}" 2>/dev/null | tr '\n' ' ')"
  [ "${code}" = "$1" ]
}

chat_status_is() {
  local out="${E2E_WORK_DIR}/chat-status.out" code
  code="$(gw POST /v1/chat/completions "${out}" \
    -H @"${AUTH_HEADER_FILE}" \
    -H 'Content-Type: application/json' \
    --data "$(chat_body "${MODEL}")")"
  LAST_OBSERVED="HTTP ${code} $(head -c 200 "${out}" 2>/dev/null | tr '\n' ' ')"
  [ "${code}" = "$1" ]
}

# steady_state: both views agree that the endpoint is registered and healthy.
steady_state() {
  local cr
  if ! conditions_are Ready=True/HealthProbeSucceeded TransportReady=True/PylonConnected Registered=True/RegisteredWithRouter; then
    LAST_OBSERVED="CR: ${LAST_OBSERVED}"
    return 1
  fi
  cr="${LAST_OBSERVED}"
  if ! registry_is Healthy 1 1; then
    LAST_OBSERVED="CR: ${cr}; registry: ${LAST_OBSERVED}"
    return 1
  fi
  return 0
}

# endpoint_field NAME JSONPATH
endpoint_field() {
  kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "$1" -o "jsonpath=$2" 2>/dev/null || true
}

server_ids() {
  endpoint_field "${ENDPOINT}" '{.status.servers[*].inferenceServerId}'
}

transition_time() {
  endpoint_field "${ENDPOINT}" "{.status.conditions[?(@.type==\"$1\")].lastTransitionTime}"
}

# server_id_replaced OLD: one server is listed and its id is not OLD.
server_id_replaced() {
  local ids
  ids="$(server_ids)"
  LAST_OBSERVED="servers=${ids:-<none>} before=$1"
  [ -n "${ids}" ] && [ "${ids}" != "$1" ] && [ "${ids}" = "${ids%% *}" ]
}

endpoint_observed() {
  local generation observed
  generation="$(endpoint_field "$1" '{.metadata.generation}')"
  observed="$(endpoint_field "$1" '{.status.observedGeneration}')"
  LAST_OBSERVED="$1 generation=${generation:-?} observedGeneration=${observed:-<unset>}"
  [ -n "${generation}" ] && [ "${generation}" = "${observed}" ]
}

endpoint_status_empty() {
  local status
  status="$(endpoint_field "$1" '{.status}')"
  LAST_OBSERVED="$1 status=${status:-<empty>}"
  [ -z "${status}" ] || [ "${status}" = "{}" ]
}

# value_is LABEL EXPECTED COMMAND [ARGS...]: COMMAND prints EXPECTED.
value_is() {
  local label="$1" want="$2" got
  shift 2
  got="$("$@")"
  LAST_OBSERVED="${label}=${got:-<unset>}, expected ${want:-<unset>}"
  [ "${got}" = "${want}" ]
}

# count_above LABEL BEFORE COMMAND [ARGS...]: COMMAND prints a number above BEFORE.
count_above() {
  local label="$1" before="$2" got
  shift 2
  got="$("$@")"
  LAST_OBSERVED="${label}=${got:-<unset>}, before ${before}"
  [ -n "${got}" ] && [ "${got}" -gt "${before}" ]
}

# event_count NAME [FIELD=VALUE]: Events on InferenceEndpoint NAME, counting
# repeats of a deduplicated Event.
event_count() {
  kc -n "${E2E_MODELS_NAMESPACE}" get events \
    --field-selector "involvedObject.kind=InferenceEndpoint,involvedObject.name=$1${2:+,$2}" \
    -o jsonpath='{range .items[*]}{.count}{"\n"}{end}' 2>/dev/null \
    | awk '{ n += ($1 == "" ? 1 : $1) } END { print n + 0 }' || true
}

event_reasons() {
  kc -n "${E2E_MODELS_NAMESPACE}" get events \
    --field-selector "involvedObject.kind=InferenceEndpoint,involvedObject.name=$1" \
    --sort-by=.lastTimestamp -o jsonpath='{range .items[*]}{.type}/{.reason}({.count}) {end}' 2>/dev/null || true
}

# deployment_selector NAMESPACE DEPLOYMENT: the Deployment's pod selector as k=v,k=v.
deployment_selector() {
  kc -n "$1" get deployment "$2" \
    -o go-template='{{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null | sed 's/,$//' || true
}

first_pod() {
  kc -n "$1" get pods -l "$2" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
}

pod_uids() {
  kc -n "$1" get pods -l "$2" -o jsonpath='{range .items[*]}{.metadata.name}={.metadata.uid} {end}' 2>/dev/null || true
}

container_restarts() {
  kc -n "$1" get pods -l "$2" -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}' 2>/dev/null || true
}

# new_pod_ready NAMESPACE SELECTOR OLD_POD: a pod other than OLD_POD is Ready.
new_pod_ready() {
  local lines
  lines="$(kc -n "$1" get pods -l "$2" \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' 2>/dev/null || true)"
  LAST_OBSERVED="$(printf '%s' "${lines}" | tr '\n' ';')"
  printf '%s\n' "${lines}" | grep -v "^$3 " | grep -q ' True$'
}

pods_gone() {
  local pods
  pods="$(kc -n "$1" get pods -l "$2" -o name 2>/dev/null || true)"
  LAST_OBSERVED="pods: ${pods:-<none>}"
  [ -z "${pods}" ]
}

deployment_absent() {
  if kc -n "$1" get deployment "$2" >/dev/null 2>&1; then
    LAST_OBSERVED="deployment $2 exists"
    return 1
  fi
  LAST_OBSERVED="deployment $2 not found"
}

# router_log_count TEXT SERVER_ID: router log lines with both strings.
router_log_count() {
  kc -n "${E2E_STACK_NAMESPACE}" logs "deployment/${ROUTER_DEPLOYMENT}" 2>/dev/null | decolor \
    | grep -F "$1" | grep -cF "$2" || true
}

router_log_last() {
  kc -n "${E2E_STACK_NAMESPACE}" logs "deployment/${ROUTER_DEPLOYMENT}" --timestamps 2>/dev/null | decolor \
    | grep -F "$1" | grep -F "$2" | tail -n 1 | cut -c1-240 || true
}

# start_stream OUTFILE CHUNKS ITL_MS: a streaming chat in the background that
# lasts about CHUNKS x ITL_MS. Its HTTP status goes to OUTFILE.code. The
# controls go in the body: the gateway does not forward X-Load-Tester headers.
start_stream() {
  start_port_forward >/dev/null 2>&1 || true
  curl -sS -N --max-time 120 --cacert "${CA_FILE}" -o "$1" -w '%{http_code}' \
    -H @"${AUTH_HEADER_FILE}" \
    -H 'Content-Type: application/json' \
    --data "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true,\"x_load_tester_output_chunks\":$2,\"x_load_tester_itl_ms\":$3}" \
    "https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}/v1/chat/completions" >"$1.code" 2>>"${E2E_WORK_DIR}/curl.err" &
  STREAM_PID=$!
}

stream_open() {
  LAST_OBSERVED="stream pid ${STREAM_PID:-<none>}"
  [ -n "${STREAM_PID}" ] && kill -0 "${STREAM_PID}" 2>/dev/null
}

# stream_completed OUTFILE: the background stream ended with 200 and [DONE].
stream_completed() {
  local code done_line data
  if [ -n "${STREAM_PID}" ]; then
    wait "${STREAM_PID}" 2>/dev/null || true
    STREAM_PID=""
  fi
  code="$(cat "$1.code" 2>/dev/null || true)"
  data="$(grep -c '^data: ' "$1" 2>/dev/null || true)"
  done_line="$(grep -c '^data: \[DONE\]' "$1" 2>/dev/null || true)"
  LAST_OBSERVED="HTTP ${code:-000}, ${data:-0} data lines, [DONE]=${done_line:-0}"
  [ "${code}" = "200" ] && [ "${done_line:-0}" -ge 1 ]
}

# ---------------------------------------------------------------------------
# Faults

scale_operator() {
  ensure_context
  kc -n "${E2E_OPERATOR_NAMESPACE}" scale "deployment/${OPERATOR_DEPLOYMENT}" --replicas="$1" >/dev/null
  if [ "$1" = "0" ]; then OPERATOR_SCALED_DOWN=1; else OPERATOR_SCALED_DOWN=0; fi
}

# kill_backend_process: the container restarts in place; the pod is kept.
kill_backend_process() {
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" exec "deployment/${SAMPLE_DEPLOYMENT}" -- kill 1 >/dev/null 2>&1 || true
}

apply_second_endpoint() {
  ensure_context
  sed "s/^  name: ${ENDPOINT}\$/  name: ${ENDPOINT_B}/" "${MANIFESTS}/inference-endpoint.yaml" \
    | kc -n "${E2E_MODELS_NAMESPACE}" apply -f - >/dev/null
}

delete_second_endpoint() {
  kc -n "${E2E_MODELS_NAMESPACE}" delete inferenceendpoint "${ENDPOINT_B}" --ignore-not-found --wait=true --timeout=60s >/dev/null
}

# undo_faults: restores what a case changed when a run stops in the middle.
undo_faults() {
  if [ "${STREAM_PID}" != "" ]; then
    kill "${STREAM_PID}" 2>/dev/null || true
    STREAM_PID=""
  fi
  if [ "${OPERATOR_SCALED_DOWN}" = "1" ]; then
    log "scaling ${E2E_OPERATOR_NAMESPACE}/${OPERATOR_DEPLOYMENT} back to 1"
    scale_operator 1 || true
  fi
}

# ---------------------------------------------------------------------------
# Cases

# run_restart_cases: every case starts and ends in the steady state of both
# views.
run_restart_cases() {
  local T="${E2E_TIMEOUT}"
  local ids before restarts warnings registered_at events old_pod start ready_at selector

  section "C7a: backend process killed"
  ensure_context
  expect "C7a steady state before the fault" "${T}" steady_state || true
  ids="$(server_ids)"
  before="$(router_log_count 'health check failed' "${ids}")"
  restarts="$(container_restarts "${E2E_MODELS_NAMESPACE}" "${SAMPLE_SELECTOR}")"
  warnings="$(event_count "${ENDPOINT}" type=Warning)"
  registered_at="$(transition_time Registered)"
  log "killing the backend process at $(date -u +%H:%M:%S) UTC"
  kill_backend_process
  expect "C7a stack: router logs a failed health probe for ${ids}" 60 \
    count_above "router 'health check failed' lines" "${before}" router_log_count 'health check failed' "${ids}" || true
  log "router: $(router_log_last 'health check failed' "${ids}")"
  log "router: $(router_log_last 'skipping routing update' "${ids}")"
  expect "C7a operator: Warning Event for Ready=False" 60 \
    count_above "Warning Events" "${warnings}" event_count "${ENDPOINT}" type=Warning || true
  expect "C7a backend container restarted in place" 60 \
    count_above restartCount "${restarts:-0}" container_restarts "${E2E_MODELS_NAMESPACE}" "${SAMPLE_SELECTOR}" || true
  expect "C7a recovered: steady state in both views" "${T}" steady_state || true
  expect "C7a operator: Registered stayed True (lastTransitionTime unchanged)" 0 \
    value_is lastTransitionTime "${registered_at}" transition_time Registered || true
  log "Events: $(event_reasons "${ENDPOINT}")"
  step_done

  section "C1: router pod deleted"
  ensure_context
  ids="$(server_ids)"
  selector="$(deployment_selector "${E2E_STACK_NAMESPACE}" "${ROUTER_DEPLOYMENT}")"
  old_pod="$(first_pod "${E2E_STACK_NAMESPACE}" "${selector}")"
  start="${SECONDS}"
  kc -n "${E2E_STACK_NAMESPACE}" delete pod "${old_pod}" --wait=false >/dev/null
  expect "C1 operator: Registered=False/RouterUnreachable, TransportReady=False/TunnelNotConnected" 90 \
    conditions_are Registered=False/RouterUnreachable TransportReady=False/TunnelNotConnected || true
  expect "C1 stack: GET /v1/registry is 502 while no router is ready" 60 registry_status_is 502 || true
  if expect "C1 new router pod Ready" "${T}" new_pod_ready "${E2E_STACK_NAMESPACE}" "${selector}" "${old_pod}"; then
    ready_at="${SECONDS}"
    log "router pod deleted to new pod Ready: $((ready_at - start))s (readiness warm-up)"
    if expect "C1 stack: registry Healthy, 1 registered, 1 healthy" 60 registry_is Healthy 1 1; then
      if [ $((SECONDS - ready_at)) -le 15 ]; then
        pass "C1 stack: re-registered within 15s of the new router being Ready" "$((SECONDS - ready_at))s"
      else
        fail "C1 stack: re-registered within 15s of the new router being Ready" "$((SECONDS - ready_at))s"
      fi
    fi
  fi
  expect "C1 recovered: steady state in both views" "${T}" steady_state || true
  expect "C1 operator: same server id after the router restart" 0 value_is servers "${ids}" server_ids || true
  log "Events: $(event_reasons "${ENDPOINT}")"
  step_done

  section "C2: gateway pod deleted"
  ensure_context
  registered_at="$(transition_time Registered)"
  events="$(event_count "${ENDPOINT}")"
  selector="$(deployment_selector "${E2E_STACK_NAMESPACE}" "${GATEWAY_DEPLOYMENT}")"
  old_pod="$(first_pod "${E2E_STACK_NAMESPACE}" "${selector}")"
  kc -n "${E2E_STACK_NAMESPACE}" delete pod "${old_pod}" --wait=true --timeout=60s >/dev/null || true
  stop_port_forward
  expect "C2 new gateway pod Ready" "${T}" new_pod_ready "${E2E_STACK_NAMESPACE}" "${selector}" "${old_pod}" || true
  # Restart the port-forward here: started from gw inside $(...), it would
  # hold the command substitution open.
  if ! start_port_forward; then
    fail "C2 port-forward to the new gateway pod" "$(tail -n 5 "${PF_LOG}" | tr '\n' ' ')"
  fi
  expect "C2 stack: new gateway serves the registry, Healthy, 1 registered, 1 healthy" 60 registry_is Healthy 1 1 || true
  expect "C2 operator: no transition (Registered lastTransitionTime unchanged)" 0 \
    value_is lastTransitionTime "${registered_at}" transition_time Registered || true
  expect "C2 operator: no new Events" 0 value_is Events "${events}" event_count "${ENDPOINT}" || true
  step_done

  section "C3: operator down during a stream (C3b: new endpoint while down)"
  ensure_context
  ids="$(server_ids)"
  registered_at="$(transition_time Registered)"
  before="$(pod_uids "${E2E_MODELS_NAMESPACE}" "${TRANSPORT_SELECTOR}")"
  start_stream "${E2E_WORK_DIR}/c3-stream.sse" 60 500
  sleep 2
  scale_operator 0
  expect "C3 operator pods gone" 60 pods_gone "${E2E_OPERATOR_NAMESPACE}" "${OPERATOR_SELECTOR}" || true
  expect "C3 stack: stream started before the outage is still open with the operator gone" 0 stream_open || true
  apply_second_endpoint
  hold "C3 stack: registry stays Healthy, 1 registered, 1 healthy, while the operator is down" 10 registry_is Healthy 1 1 || true
  expect "C3 stack: new chat while the operator is down streams SSE (200)" 30 chat_stream_ok || true
  expect "C3b operator: ${ENDPOINT_B} has no status while the operator is down" 0 endpoint_status_empty "${ENDPOINT_B}" || true
  expect "C3b operator: no transport Deployment pylon-${ENDPOINT_B} while the operator is down" 0 \
    deployment_absent "${E2E_MODELS_NAMESPACE}" "pylon-${ENDPOINT_B}" || true
  expect "C3 stack: stream started before the outage ends with [DONE]" 0 stream_completed "${E2E_WORK_DIR}/c3-stream.sse" || true
  scale_operator 1
  expect "C3b operator: ${ENDPOINT_B} reconciled after the operator returns" "${T}" endpoint_observed "${ENDPOINT_B}" || true
  expect "C3 operator: conditions True after the operator returns" "${T}" \
    conditions_are Ready=True/HealthProbeSucceeded TransportReady=True/PylonConnected Registered=True/RegisteredWithRouter || true
  expect "C3 operator: no transition (Registered lastTransitionTime unchanged)" 0 \
    value_is lastTransitionTime "${registered_at}" transition_time Registered || true
  expect "C3 operator: Pylon pod unchanged (same name and UID)" 0 \
    value_is pods "${before}" pod_uids "${E2E_MODELS_NAMESPACE}" "${TRANSPORT_SELECTOR}" || true
  expect "C3 operator: same server id" 0 value_is servers "${ids}" server_ids || true
  delete_second_endpoint || true
  expect "C3b cleanup: pylon-${ENDPOINT_B} garbage-collected" "${T}" deployment_absent "${E2E_MODELS_NAMESPACE}" "pylon-${ENDPOINT_B}" || true
  expect "C3 recovered: steady state in both views" "${T}" steady_state || true
  step_done

  section "C5: transport (Pylon) pod deleted"
  ensure_context
  ids="$(server_ids)"
  kc -n "${E2E_MODELS_NAMESPACE}" delete pod -l "${TRANSPORT_SELECTOR}" --wait=false >/dev/null
  expect "C5 operator: one server with a new server id" "${T}" server_id_replaced "${ids}" || true
  expect "C5 operator: conditions True" "${T}" \
    conditions_are Ready=True/HealthProbeSucceeded TransportReady=True/PylonConnected Registered=True/RegisteredWithRouter || true
  expect "C5 stack: registry Healthy, 1 registered, 1 healthy" 60 registry_is Healthy 1 1 || true
  # The hold outlasts the 3 s registry cache, so a stale second server would show.
  hold "C5 stack: registry Healthy, 1 registered (old server removed), 1 healthy" 9 registry_is Healthy 1 1 || true
  log "servers: $(server_ids)"
  log "Events: $(event_reasons "${ENDPOINT}")"
  step_done
}
