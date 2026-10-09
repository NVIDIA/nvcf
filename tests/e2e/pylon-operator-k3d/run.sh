#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end test of Pylon Operator with the LLM gateway stack on a local k3d
# cluster. See README.md for prerequisites and settings.
#
# The script is compatible with bash 3.2 (the macOS default). It touches only
# the kube context in E2E_KUBE_CONTEXT and refuses to run when the current
# context differs.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
MANIFESTS="${SCRIPT_DIR}/manifests"

E2E_KUBE_CONTEXT="${E2E_KUBE_CONTEXT:-k3d-pylon-op-e2e}"
E2E_CLEANUP="${E2E_CLEANUP:-0}"
E2E_DEPLOY_ONLY="${E2E_DEPLOY_ONLY:-0}"
E2E_TIMEOUT="${E2E_TIMEOUT:-180}"
E2E_HELM_TIMEOUT="${E2E_HELM_TIMEOUT:-5m}"
E2E_STACK_NAMESPACE="${E2E_STACK_NAMESPACE:-llm-stack}"
E2E_OPERATOR_NAMESPACE="${E2E_OPERATOR_NAMESPACE:-pylon-operator}"
E2E_MODELS_NAMESPACE="${E2E_MODELS_NAMESPACE:-models}"
E2E_CLUSTER_ID="${E2E_CLUSTER_ID:-spark-e2e}"
E2E_IMAGE_REGISTRY="${E2E_IMAGE_REGISTRY:-docker.io}"
E2E_IMAGE_TAG="${E2E_IMAGE_TAG:-latest}"
E2E_ROUTER_REPOSITORY="${E2E_ROUTER_REPOSITORY:-src/libraries/rust/stargate/crates/stargate}"
E2E_GATEWAY_REPOSITORY="${E2E_GATEWAY_REPOSITORY:-src/invocation-plane-services/llm-api-gateway}"
E2E_OPERATOR_REPOSITORY="${E2E_OPERATOR_REPOSITORY:-src/compute-plane-services/pylon-operator}"
E2E_PYLON_REPOSITORY="${E2E_PYLON_REPOSITORY:-src/libraries/rust/stargate/crates/pylon}"
E2E_ROUTER_GRPC_ADDRESS="${E2E_ROUTER_GRPC_ADDRESS:-http://llm-request-router.${E2E_STACK_NAMESPACE}.svc.cluster.local:50071}"
E2E_DEV_INSECURE_TRANSPORT="${E2E_DEV_INSECURE_TRANSPORT:-0}"
E2E_ROTATE_CREDENTIALS="${E2E_ROTATE_CREDENTIALS:-0}"
E2E_GATEWAY_LOCAL_PORT="${E2E_GATEWAY_LOCAL_PORT:-18443}"
E2E_WORK_DIR="${E2E_WORK_DIR:-${TMPDIR:-/tmp}/pylon-operator-k3d-e2e}"

readonly STACK_RELEASE="llm-gateway-stack"
readonly OPERATOR_RELEASE="pylon-operator"
readonly STACK_CHART="${REPO_ROOT}/deploy/helm/llm-gateway-stack/llm-gateway-stack"
readonly OPERATOR_CHART="${REPO_ROOT}/deploy/helm/pylon-operator/pylon-operator"
readonly CREDENTIAL_SECRET="e2e-cluster-credential"
readonly CA_CONFIGMAP="llm-gateway-stack-ca"
readonly ROUTER_DEPLOYMENT="llm-request-router"
readonly GATEWAY_DEPLOYMENT="llm-api-gateway"
readonly GATEWAY_SERVICE="llm-api-gateway"
readonly GATEWAY_SERVICE_PORT="8080"
readonly SAMPLE_DEPLOYMENT="openai-compatible-sample"
readonly ENDPOINT="test-model"
readonly MODEL="test-model"
readonly WRONG_MODEL="wrong-model"
readonly TRANSPORT_DEPLOYMENT="pylon-${ENDPOINT}"
readonly TRANSPORT_SELECTOR="app.kubernetes.io/name=pylon,pylon.nvidia.com/endpoint=${ENDPOINT}"
readonly OPERATOR_SELECTOR="app.kubernetes.io/instance=${OPERATOR_RELEASE}"

CA_FILE="${E2E_WORK_DIR}/ca.crt"
AUTH_HEADER_FILE="${E2E_WORK_DIR}/auth-header"
API_KEY_FILE="${E2E_WORK_DIR}/api-key"
PF_LOG="${E2E_WORK_DIR}/port-forward.log"
PF_PID=""

RESULTS=()
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
LAST_OBSERVED=""
ABORTED=0

# ---------------------------------------------------------------------------
# Output helpers

log() {
  printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

section() {
  printf '\n===== %s =====\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  RESULTS+=("PASS  $1${2:+ ($2)}")
  log "PASS  $1${2:+ ($2)}"
}

fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  RESULTS+=("FAIL  $1${2:+ ($2)}")
  log "FAIL  $1${2:+ ($2)}"
}

skip() {
  SKIP_COUNT=$((SKIP_COUNT + 1))
  RESULTS+=("SKIP  $1")
}

die() {
  printf 'run.sh: %s\n' "$*" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# Cluster access. Every kubectl and helm call names the context explicitly,
# and every phase re-checks that the current context has not changed.

kc() {
  kubectl --context "${E2E_KUBE_CONTEXT}" "$@"
}

hm() {
  helm --kube-context "${E2E_KUBE_CONTEXT}" "$@"
}

ensure_context() {
  local current
  current="$(kubectl config current-context 2>/dev/null || true)"
  if [ "${current}" != "${E2E_KUBE_CONTEXT}" ]; then
    die "current kube context is '${current:-<none>}', expected '${E2E_KUBE_CONTEXT}'. Refusing to run."
  fi
}

# ---------------------------------------------------------------------------
# Secrets

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$1"
  else
    od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

sha256_hex() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1
  else
    printf '%s' "$1" | openssl dgst -sha256 -r | cut -d' ' -f1
  fi
}

# ---------------------------------------------------------------------------
# Polling. A check function returns 0 when satisfied and may set
# LAST_OBSERVED to describe what it saw.

poll() {
  local timeout="$1"
  shift
  local deadline=$((SECONDS + timeout))
  while :; do
    if "$@"; then
      return 0
    fi
    if [ "${SECONDS}" -ge "${deadline}" ]; then
      return 1
    fi
    sleep 3
  done
}

# expect NAME TIMEOUT CHECK [ARGS...]: poll CHECK and record the outcome.
expect() {
  local name="$1" timeout="$2"
  shift 2
  local start="${SECONDS}"
  LAST_OBSERVED=""
  if poll "${timeout}" "$@"; then
    pass "${name}" "$((SECONDS - start))s"
    return 0
  fi
  fail "${name}" "timed out after ${timeout}s; last observed: ${LAST_OBSERVED:-nothing}"
  return 1
}

# hold NAME SECONDS CHECK [ARGS...]: CHECK must hold on every poll for SECONDS.
hold() {
  local name="$1" window="$2"
  shift 2
  local end=$((SECONDS + window))
  LAST_OBSERVED=""
  while [ "${SECONDS}" -lt "${end}" ]; do
    if ! "$@"; then
      fail "${name}" "broke within ${window}s; observed: ${LAST_OBSERVED:-nothing}"
      return 1
    fi
    sleep 3
  done
  if "$@"; then
    pass "${name}" "held ${window}s"
    return 0
  fi
  fail "${name}" "broke at the end of ${window}s; observed: ${LAST_OBSERVED:-nothing}"
  return 1
}

# ---------------------------------------------------------------------------
# InferenceEndpoint checks

condition() {
  kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" \
    -o "jsonpath={.status.conditions[?(@.type==\"$1\")].status}/{.status.conditions[?(@.type==\"$1\")].reason}" 2>/dev/null || true
}

# conditions_are Type=Status/Reason ...
conditions_are() {
  local rc=0 observed="" pair type want got
  for pair in "$@"; do
    type="${pair%%=*}"
    want="${pair#*=}"
    got="$(condition "${type}")"
    observed="${observed}${type}=${got:-<unset>} "
    [ "${got}" = "${want}" ] || rc=1
  done
  LAST_OBSERVED="${observed% }"
  return "${rc}"
}

spec_observed() {
  local generation observed
  generation="$(kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" -o jsonpath='{.metadata.generation}' 2>/dev/null || true)"
  observed="$(kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)"
  LAST_OBSERVED="generation=${generation:-?} observedGeneration=${observed:-?}"
  [ -n "${generation}" ] && [ "${generation}" = "${observed}" ]
}

routers_connected_at_least() {
  local n
  n="$(kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" -o jsonpath='{.status.registration.routersConnected}' 2>/dev/null || true)"
  LAST_OBSERVED="routersConnected=${n:-<unset>}"
  [ -n "${n}" ] && [ "${n}" -ge "$1" ]
}

servers_count_is() {
  local pods n
  pods="$(kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" \
    -o jsonpath='{range .status.servers[*]}{.pod}{" streams="}{.registrationStreams}{" tunnels="}{.reverseTunnels}{"\n"}{end}' 2>/dev/null || true)"
  n="$(printf '%s' "${pods}" | grep -c . || true)"
  LAST_OBSERVED="servers=${n}: $(printf '%s' "${pods}" | tr '\n' ';')"
  [ "${n}" -eq "$1" ]
}

printer_columns_ok() {
  local out header row
  out="$(kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoints 2>/dev/null || true)"
  header="$(printf '%s\n' "${out}" | head -n 1 | tr -s ' ')"
  row="$(printf '%s\n' "${out}" | grep "^${ENDPOINT} " | tr -s ' ' || true)"
  LAST_OBSERVED="header='${header}' row='${row}'"
  [ "${header}" = "NAME MODEL GPU READY REGISTERED SERVERS AGE" ] || return 1
  case "${row}" in
    "${ENDPOINT} ${MODEL} "*"True True 1 "*) return 0 ;;
  esac
  return 1
}

transport_replicas_are() {
  local spec status
  spec="$(kc -n "${E2E_MODELS_NAMESPACE}" get deployment "${TRANSPORT_DEPLOYMENT}" -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
  status="$(kc -n "${E2E_MODELS_NAMESPACE}" get deployment "${TRANSPORT_DEPLOYMENT}" -o jsonpath='{.status.replicas}' 2>/dev/null || true)"
  LAST_OBSERVED="spec.replicas=${spec:-<unset>} status.replicas=${status:-0}"
  [ "${spec}" = "$1" ] && [ "${status:-0}" = "$1" ]
}

transport_deployment_gone() {
  if kc -n "${E2E_MODELS_NAMESPACE}" get deployment "${TRANSPORT_DEPLOYMENT}" >/dev/null 2>&1; then
    LAST_OBSERVED="deployment ${TRANSPORT_DEPLOYMENT} still exists"
    return 1
  fi
  LAST_OBSERVED="deployment ${TRANSPORT_DEPLOYMENT} not found"
  return 0
}

endpoint_gone() {
  ! kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Gateway checks, through kubectl port-forward and TLS verified against the
# stack CA. The gateway certificate covers 127.0.0.1.

start_port_forward() {
  if [ -n "${PF_PID}" ] && kill -0 "${PF_PID}" 2>/dev/null; then
    return 0
  fi
  kc -n "${E2E_STACK_NAMESPACE}" port-forward --address 127.0.0.1 \
    "svc/${GATEWAY_SERVICE}" "${E2E_GATEWAY_LOCAL_PORT}:${GATEWAY_SERVICE_PORT}" >>"${PF_LOG}" 2>&1 &
  PF_PID=$!
  local i=0
  while [ "${i}" -lt 30 ]; do
    if curl -s -o /dev/null --max-time 2 --cacert "${CA_FILE}" "https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}/v1/models"; then
      return 0
    fi
    kill -0 "${PF_PID}" 2>/dev/null || break
    sleep 1
    i=$((i + 1))
  done
  return 1
}

stop_port_forward() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
    PF_PID=""
  fi
}

# gw METHOD PATH OUTFILE [CURL ARGS...]: prints the HTTP status, 000 on
# connection failure. Response headers go to OUTFILE.headers.
gw() {
  local method="$1" path="$2" out="$3"
  shift 3
  start_port_forward >/dev/null 2>&1 || true
  local code
  code="$(curl -sS -N --max-time 30 --cacert "${CA_FILE}" -X "${method}" \
    -D "${out}.headers" -o "${out}" -w '%{http_code}' "$@" \
    "https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}${path}" 2>>"${E2E_WORK_DIR}/curl.err" || true)"
  printf '%s' "${code:-000}"
}

compact() {
  tr -d ' \t\r\n' <"$1"
}

models_lists_test_model() {
  local out="${E2E_WORK_DIR}/models.json" code body
  code="$(gw GET /v1/models "${out}")"
  body="$(compact "${out}" 2>/dev/null || true)"
  LAST_OBSERVED="HTTP ${code} ${body:0:300}"
  [ "${code}" = "200" ] && printf '%s' "${body}" | grep -q "\"id\":\"${MODEL}\""
}

models_omit_test_model() {
  local out="${E2E_WORK_DIR}/models.json" code body
  code="$(gw GET /v1/models "${out}")"
  body="$(compact "${out}" 2>/dev/null || true)"
  LAST_OBSERVED="HTTP ${code} ${body:0:300}"
  [ "${code}" = "200" ] && ! printf '%s' "${body}" | grep -q "\"id\":\"${MODEL}\""
}

registry_lists_test_model() {
  local out="${E2E_WORK_DIR}/registry.json" code body
  code="$(gw GET /v1/registry "${out}")"
  body="$(compact "${out}" 2>/dev/null || true)"
  LAST_OBSERVED="HTTP ${code} ${body:0:300}"
  [ "${code}" = "200" ] || return 1
  printf '%s' "${body}" | grep -q "\"model\":\"${MODEL}\",\"health\":\"Healthy\"" || return 1
  printf '%s' "${body}" | grep -q "\"clusterId\":\"${E2E_CLUSTER_ID}\"" || return 1
  printf '%s' "${body}" | grep -Eq '"registeredServers":1,"healthyServers":1[,}]'
}

chat_body() {
  printf '{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":true}' "$1"
}

chat_stream_ok() {
  local out="${E2E_WORK_DIR}/chat.sse" code data done_line ctype
  code="$(gw POST /v1/chat/completions "${out}" \
    -H @"${AUTH_HEADER_FILE}" \
    -H 'Content-Type: application/json' \
    -H 'X-Load-Tester-Output-Chunks: 3' \
    --data "$(chat_body "${MODEL}")")"
  data="$(grep -c '^data: ' "${out}" 2>/dev/null || true)"
  done_line="$(grep -c '^data: \[DONE\]' "${out}" 2>/dev/null || true)"
  ctype="$(grep -i '^content-type:' "${out}.headers" 2>/dev/null | tr -d '\r' | head -n 1 || true)"
  LAST_OBSERVED="HTTP ${code}, ${data:-0} data lines, [DONE]=${done_line:-0}, ${ctype:-no content-type}; body: $(head -c 200 "${out}" 2>/dev/null | tr '\n' ' ')"
  [ "${code}" = "200" ] && [ "${data:-0}" -ge 2 ] && [ "${done_line:-0}" -ge 1 ] \
    && printf '%s' "${ctype}" | grep -qi 'text/event-stream'
}

chat_without_key_is_401() {
  local out="${E2E_WORK_DIR}/chat-nokey.json" code
  code="$(gw POST /v1/chat/completions "${out}" \
    -H 'Content-Type: application/json' \
    --data "$(chat_body "${MODEL}")")"
  LAST_OBSERVED="HTTP ${code} $(head -c 200 "${out}" 2>/dev/null | tr '\n' ' ')"
  [ "${code}" = "401" ]
}

# ---------------------------------------------------------------------------
# Diagnostics and summary

# Strips ANSI color codes from the router and Pylon logs.
decolor() {
  sed $'s/\033\\[[0-9;]*m//g'
}

dump_diagnostics() {
  ensure_context
  section "InferenceEndpoint ${E2E_MODELS_NAMESPACE}/${ENDPOINT}"
  kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" -o yaml 2>&1 || true
  section "Pods"
  kc get pods -n "${E2E_STACK_NAMESPACE}" -o wide 2>&1 || true
  kc get pods -n "${E2E_OPERATOR_NAMESPACE}" -o wide 2>&1 || true
  kc get pods -n "${E2E_MODELS_NAMESPACE}" -o wide 2>&1 || true
  section "Events in ${E2E_MODELS_NAMESPACE} (last 30)"
  kc -n "${E2E_MODELS_NAMESPACE}" get events --sort-by=.lastTimestamp 2>&1 | tail -n 30 || true
  section "Operator logs (tail 100)"
  kc -n "${E2E_OPERATOR_NAMESPACE}" logs -l "${OPERATOR_SELECTOR}" --tail=100 --all-containers 2>&1 | decolor || true
  section "Pylon logs (tail 100)"
  kc -n "${E2E_MODELS_NAMESPACE}" logs -l "${TRANSPORT_SELECTOR}" --tail=100 --all-containers --prefix 2>&1 | decolor || true
  section "Router logs (tail 100)"
  kc -n "${E2E_STACK_NAMESPACE}" logs "deployment/${ROUTER_DEPLOYMENT}" --tail=100 --all-containers 2>&1 | decolor || true
  section "Gateway logs (tail 100)"
  kc -n "${E2E_STACK_NAMESPACE}" logs "deployment/${GATEWAY_DEPLOYMENT}" --tail=100 --all-containers 2>&1 | decolor || true
}

print_summary() {
  local r
  section "SUMMARY"
  for r in ${RESULTS[@]+"${RESULTS[@]}"}; do
    printf '%s\n' "${r}"
  done
  printf -- '---------------------------------------------\n'
  if [ "${FAIL_COUNT}" -eq 0 ] && [ "${ABORTED}" -eq 0 ]; then
    printf 'RESULT: PASS (%d passed, %d failed, %d skipped)\n' "${PASS_COUNT}" "${FAIL_COUNT}" "${SKIP_COUNT}"
  else
    printf 'RESULT: FAIL (%d passed, %d failed, %d skipped)\n' "${PASS_COUNT}" "${FAIL_COUNT}" "${SKIP_COUNT}"
  fi
}

# Remaining step names, recorded as skipped when a required step fails.
REMAINING_STEPS=""

abort() {
  ABORTED=1
  local s
  local IFS='|'
  for s in ${REMAINING_STEPS}; do
    [ -n "${s}" ] && skip "${s}"
  done
  dump_diagnostics
  print_summary
  exit 1
}

# step NAME: marks NAME as done in REMAINING_STEPS.
step_done() {
  REMAINING_STEPS="${REMAINING_STEPS#*|}"
}

on_exit() {
  undo_faults
  stop_port_forward
}
trap on_exit EXIT

# ---------------------------------------------------------------------------
# Phases

preflight() {
  local tool
  for tool in kubectl helm curl; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
  if ! command -v openssl >/dev/null 2>&1 && [ ! -r /dev/urandom ]; then
    die "openssl or /dev/urandom is required"
  fi
  ensure_context
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  : >"${PF_LOG}"
  : >"${E2E_WORK_DIR}/curl.err"
  log "context ${E2E_KUBE_CONTEXT}, work dir ${E2E_WORK_DIR}"
  kc get nodes >/dev/null || die "cannot reach the cluster of context ${E2E_KUBE_CONTEXT}"
  pass "kube context is ${E2E_KUBE_CONTEXT}"
}

# A re-run reuses the cluster token of the credential Secret and the API key
# of the work directory: a new token or key reaches the router and gateway
# only after the kubelet refreshes their Secret volumes, which can take a
# minute or more. E2E_ROTATE_CREDENTIALS=1 generates new ones anyway.
generate_credentials() {
  ensure_context
  CLUSTER_TOKEN=""
  API_KEY=""
  if [ "${E2E_ROTATE_CREDENTIALS}" != "1" ]; then
    CLUSTER_TOKEN="$(kc -n "${E2E_OPERATOR_NAMESPACE}" get secret "${CREDENTIAL_SECRET}" \
      -o 'jsonpath={.data.cluster-token}' 2>/dev/null | base64 --decode 2>/dev/null || true)"
    if [ -s "${API_KEY_FILE}" ]; then
      API_KEY="$(cat "${API_KEY_FILE}")"
    fi
  fi
  if [ -n "${CLUSTER_TOKEN}" ]; then
    log "reusing the cluster token in Secret ${E2E_OPERATOR_NAMESPACE}/${CREDENTIAL_SECRET}"
  else
    CLUSTER_TOKEN="$(random_hex 24)"
    log "generated a cluster token"
  fi
  if [ -n "${API_KEY}" ]; then
    log "reusing the API key in ${API_KEY_FILE}"
  else
    API_KEY="$(random_hex 32)"
    log "generated an API key; it is in ${API_KEY_FILE}"
  fi
  CLUSTER_TOKEN_SHA256="$(sha256_hex "${CLUSTER_TOKEN}")"
  API_KEY_SHA256="$(sha256_hex "${API_KEY}")"
  (
    umask 077
    printf '%s' "${API_KEY}" >"${API_KEY_FILE}"
    printf 'Authorization: Bearer %s\n' "${API_KEY}" >"${AUTH_HEADER_FILE}"
  )
}

create_namespaces() {
  ensure_context
  local ns
  for ns in "${E2E_STACK_NAMESPACE}" "${E2E_OPERATOR_NAMESPACE}" "${E2E_MODELS_NAMESPACE}"; do
    kc create namespace "${ns}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  done
  pass "namespaces ${E2E_STACK_NAMESPACE}, ${E2E_OPERATOR_NAMESPACE}, ${E2E_MODELS_NAMESPACE}"
}

reset_endpoint() {
  ensure_context
  if kc get crd inferenceendpoints.pylon.nvidia.com >/dev/null 2>&1 \
    && kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoint "${ENDPOINT}" >/dev/null 2>&1; then
    log "deleting the InferenceEndpoint left by a previous run"
    kc -n "${E2E_MODELS_NAMESPACE}" delete inferenceendpoint "${ENDPOINT}" --wait=true --timeout=60s >/dev/null
    poll 60 transport_deployment_gone || true
  fi
  if kc get crd inferenceendpoints.pylon.nvidia.com >/dev/null 2>&1; then
    delete_second_endpoint || true
  fi
}

create_credential_secret() {
  ensure_context
  kc -n "${E2E_OPERATOR_NAMESPACE}" create secret generic "${CREDENTIAL_SECRET}" \
    --from-literal=cluster-token="${CLUSTER_TOKEN}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  pass "cluster credential Secret ${E2E_OPERATOR_NAMESPACE}/${CREDENTIAL_SECRET}"
}

install_stack() {
  ensure_context
  local values="${E2E_WORK_DIR}/llm-gateway-stack-values.yaml"
  cat >"${values}" <<EOF
clusterId: ${E2E_CLUSTER_ID}
clusterCredential:
  sha256Hashes:
    - sha256:${CLUSTER_TOKEN_SHA256}
apiKeys:
  - id: e2e
    sha256: ${API_KEY_SHA256}
llm-request-router:
  llmRequestRouter:
    replicaCount: 1
    image:
      registry: ${E2E_IMAGE_REGISTRY}
      repository: ${E2E_ROUTER_REPOSITORY}
      tag: ${E2E_IMAGE_TAG}
      pullPolicy: IfNotPresent
llm-api-gateway:
  llmApiGateway:
    replicaCount: 1
    image:
      registry: ${E2E_IMAGE_REGISTRY}
      repository: ${E2E_GATEWAY_REPOSITORY}
      tag: ${E2E_IMAGE_TAG}
      pullPolicy: IfNotPresent
    olric:
      env: local
EOF
  log "helm dependency build ${STACK_CHART}"
  helm dependency build --skip-refresh "${STACK_CHART}" >/dev/null
  log "helm upgrade --install ${STACK_RELEASE} (namespace ${E2E_STACK_NAMESPACE})"
  if ! hm upgrade --install "${STACK_RELEASE}" "${STACK_CHART}" \
    --namespace "${E2E_STACK_NAMESPACE}" --values "${values}" \
    --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-stack.log" 2>&1; then
    cat "${E2E_WORK_DIR}/helm-stack.log"
    fail "helm install ${STACK_RELEASE}"
    return 1
  fi
  pass "helm install ${STACK_RELEASE}"
}

copy_trust_bundle() {
  ensure_context
  kc -n "${E2E_STACK_NAMESPACE}" get configmap "${CA_CONFIGMAP}" -o 'jsonpath={.data.ca\.crt}' >"${CA_FILE}"
  if ! grep -q 'BEGIN CERTIFICATE' "${CA_FILE}"; then
    fail "CA ConfigMap ${E2E_STACK_NAMESPACE}/${CA_CONFIGMAP} has a certificate in ca.crt"
    return 1
  fi
  kc -n "${E2E_OPERATOR_NAMESPACE}" create configmap "${CA_CONFIGMAP}" --from-file=ca.crt="${CA_FILE}" \
    --dry-run=client -o yaml | kc apply -f - >/dev/null
  pass "CA ConfigMap copied to ${E2E_OPERATOR_NAMESPACE}/${CA_CONFIGMAP}"
}

install_operator() {
  ensure_context
  local values="${E2E_WORK_DIR}/pylon-operator-values.yaml"
  local insecure=false
  [ "${E2E_DEV_INSECURE_TRANSPORT}" = "1" ] && insecure=true
  cat >"${values}" <<EOF
image:
  repository: ${E2E_IMAGE_REGISTRY}/${E2E_OPERATOR_REPOSITORY}
  tag: ${E2E_IMAGE_TAG}
  pullPolicy: IfNotPresent
clusterId: ${E2E_CLUSTER_ID}
router:
  grpcAddress: ${E2E_ROUTER_GRPC_ADDRESS}
pylon:
  image:
    repository: ${E2E_IMAGE_REGISTRY}/${E2E_PYLON_REPOSITORY}
    tag: ${E2E_IMAGE_TAG}
    pullPolicy: IfNotPresent
watchNamespaces:
  - ${E2E_MODELS_NAMESPACE}
credential:
  existingSecret: ${CREDENTIAL_SECRET}
trustBundle:
  configMap: ${CA_CONFIGMAP}
devInsecureTransport: ${insecure}
transport:
  replicas: 1
EOF
  log "helm upgrade --install ${OPERATOR_RELEASE} (namespace ${E2E_OPERATOR_NAMESPACE}), router ${E2E_ROUTER_GRPC_ADDRESS}, devInsecureTransport=${insecure}"
  if ! hm upgrade --install "${OPERATOR_RELEASE}" "${OPERATOR_CHART}" \
    --namespace "${E2E_OPERATOR_NAMESPACE}" --values "${values}" \
    --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-operator.log" 2>&1; then
    cat "${E2E_WORK_DIR}/helm-operator.log"
    fail "helm install ${OPERATOR_RELEASE}"
    return 1
  fi
  pass "helm install ${OPERATOR_RELEASE}"
}

deploy_backend() {
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" apply -f "${MANIFESTS}/sample-backend.yaml" >/dev/null
  if ! kc -n "${E2E_MODELS_NAMESPACE}" rollout status "deployment/${SAMPLE_DEPLOYMENT}" --timeout="${E2E_TIMEOUT}s" >/dev/null; then
    fail "sample backend ready"
    return 1
  fi
  pass "sample backend ready"
}

apply_endpoint() {
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" apply -f "${MANIFESTS}/inference-endpoint.yaml" >/dev/null
  pass "InferenceEndpoint ${E2E_MODELS_NAMESPACE}/${ENDPOINT} applied"
}

scale_backend() {
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" scale "deployment/${SAMPLE_DEPLOYMENT}" --replicas="$1" >/dev/null
}

patch_model_name() {
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" patch inferenceendpoint "${ENDPOINT}" --type=merge \
    -p "{\"spec\":{\"modelName\":\"$1\"}}" >/dev/null
}

cleanup() {
  ensure_context
  section "Cleanup (E2E_CLEANUP=1)"
  kc -n "${E2E_MODELS_NAMESPACE}" delete inferenceendpoint "${ENDPOINT}" --ignore-not-found --wait=true --timeout=60s || true
  delete_second_endpoint || true
  hm uninstall "${OPERATOR_RELEASE}" --namespace "${E2E_OPERATOR_NAMESPACE}" --wait || true
  hm uninstall "${STACK_RELEASE}" --namespace "${E2E_STACK_NAMESPACE}" --wait || true
  kc delete namespace "${E2E_MODELS_NAMESPACE}" "${E2E_OPERATOR_NAMESPACE}" "${E2E_STACK_NAMESPACE}" --ignore-not-found --wait=true --timeout=120s || true
  # The CRD carries helm.sh/resource-policy: keep, so uninstall leaves it.
  kc delete crd inferenceendpoints.pylon.nvidia.com --ignore-not-found || true
}

# E2E_DEPLOY_ONLY=1: how to reach the gateway that stays installed. The key
# is referenced through the header file, never printed.
print_access() {
  section "Deployed"
  cat <<EOF
InferenceEndpoint ${E2E_MODELS_NAMESPACE}/${ENDPOINT} is ready and everything stays installed.
The gateway checks and the failure paths did not run; run the full test without E2E_DEPLOY_ONLY.

Port-forward the gateway in another terminal:

  kubectl --context ${E2E_KUBE_CONTEXT} -n ${E2E_STACK_NAMESPACE} port-forward svc/${GATEWAY_SERVICE} ${E2E_GATEWAY_LOCAL_PORT}:${GATEWAY_SERVICE_PORT}

Then call it with the CA and the API key in ${E2E_WORK_DIR}:

  curl --cacert "${CA_FILE}" https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}/v1/models
  curl --cacert "${CA_FILE}" https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}/v1/registry
  curl -N --cacert "${CA_FILE}" https://127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}/v1/chat/completions \\
    -H @"${AUTH_HEADER_FILE}" -H 'Content-Type: application/json' \\
    -d '{"model": "${MODEL}", "messages": [{"role": "user", "content": "hi"}], "stream": true}'
EOF
}

# Restart and failure cases: helpers, faults and run_restart_cases.
# shellcheck source=restart-cases.sh
source "${SCRIPT_DIR}/restart-cases.sh"

# ---------------------------------------------------------------------------
# Main

main() {
  local T="${E2E_TIMEOUT}"
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    if [ "${E2E_CLEANUP}" = "1" ]; then
      die "E2E_DEPLOY_ONLY=1 leaves everything installed and cannot be combined with E2E_CLEANUP=1"
    fi
    log "E2E_DEPLOY_ONLY=1: stopping once the InferenceEndpoint is ready"
    REMAINING_STEPS="setup|steady state conditions|registration status|printer columns|"
  else
    REMAINING_STEPS="setup|steady state conditions|registration status|printer columns|gateway checks|backend scaled to zero|backend restored|model name mismatch|model name restored|C7a backend process killed|C1 router restart|C2 gateway restart|C3 operator down|C5 transport restart|endpoint deleted|"
  fi

  section "Setup"
  preflight
  generate_credentials
  create_namespaces
  reset_endpoint
  create_credential_secret
  install_stack || abort
  copy_trust_bundle || abort
  install_operator || abort
  deploy_backend || abort
  apply_endpoint
  step_done

  section "Steady state"
  expect "Ready=True/HealthProbeSucceeded" "${T}" conditions_are Ready=True/HealthProbeSucceeded || abort
  expect "TransportReady=True/PylonConnected" "${T}" conditions_are TransportReady=True/PylonConnected || abort
  expect "Registered=True/RegisteredWithRouter" "${T}" conditions_are Registered=True/RegisteredWithRouter || abort
  step_done
  expect "status.registration.routersConnected >= 1" "${T}" routers_connected_at_least 1 || true
  expect "status.servers has one entry" "${T}" servers_count_is 1 || true
  expect "observedGeneration matches generation" "${T}" spec_observed || true
  step_done
  expect "kubectl get inferenceendpoints shows the printer columns" "${T}" printer_columns_ok || true
  kc -n "${E2E_MODELS_NAMESPACE}" get inferenceendpoints || true
  step_done

  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    if [ "${FAIL_COUNT}" -gt 0 ]; then
      dump_diagnostics
    fi
    print_access
    print_summary
    [ "${FAIL_COUNT}" -eq 0 ]
    return
  fi

  section "Gateway"
  ensure_context
  if ! start_port_forward; then
    fail "port-forward to svc/${GATEWAY_SERVICE}" "$(tail -n 5 "${PF_LOG}" | tr '\n' ' ')"
    abort
  fi
  pass "port-forward to svc/${GATEWAY_SERVICE} on 127.0.0.1:${E2E_GATEWAY_LOCAL_PORT}"
  expect "GET /v1/models lists ${MODEL}" "${T}" models_lists_test_model || true
  expect "GET /v1/registry lists ${MODEL} Healthy with 1 healthy server" "${T}" registry_lists_test_model || true
  expect "POST /v1/chat/completions with key streams SSE (200)" "${T}" chat_stream_ok || true
  log "chat stream: ${LAST_OBSERVED}"
  expect "POST /v1/chat/completions without key is 401" 30 chat_without_key_is_401 || true
  step_done

  section "Failure path: backend scaled to zero (C7b)"
  scale_backend 0
  expect "backend at 0: Ready=False/NoReadyEndpoints" "${T}" conditions_are Ready=False/NoReadyEndpoints || true
  hold "backend at 0: Registered stays True/RegisteredWithRouter" 20 conditions_are Registered=True/RegisteredWithRouter || true
  expect "C7b stack: registry Unhealthy, 1 registered, 0 healthy" 60 registry_is Unhealthy 1 0 || true
  expect "C7b stack: GET /v1/models omits ${MODEL}" 30 models_omit_test_model || true
  expect "C7b stack: chat returns 503 from the router" 30 chat_status_is 503 || true
  log "backend at 0: $(conditions_are Ready= TransportReady= Registered= || true; printf '%s' "${LAST_OBSERVED}")"
  step_done
  scale_backend 1
  expect "backend restored: Ready=True/HealthProbeSucceeded" "${T}" conditions_are Ready=True/HealthProbeSucceeded || true
  expect "backend restored: Registered=True, TransportReady=True" "${T}" \
    conditions_are Registered=True/RegisteredWithRouter TransportReady=True/PylonConnected || true
  step_done

  section "Failure path: model name mismatch"
  patch_model_name "${WRONG_MODEL}"
  expect "modelName ${WRONG_MODEL}: Ready=False/ModelNameMismatch" "${T}" conditions_are Ready=False/ModelNameMismatch || true
  expect "modelName ${WRONG_MODEL}: transport Deployment at 0 replicas" "${T}" transport_replicas_are 0 || true
  expect "modelName ${WRONG_MODEL}: TransportReady and Registered ScaledToZero" "${T}" \
    conditions_are TransportReady=False/ScaledToZero Registered=False/ScaledToZero || true
  step_done
  patch_model_name "${MODEL}"
  expect "modelName restored: Ready=True/HealthProbeSucceeded" "${T}" conditions_are Ready=True/HealthProbeSucceeded || true
  expect "modelName restored: transport Deployment at 1 replica" "${T}" transport_replicas_are 1 || true
  expect "modelName restored: TransportReady=True, Registered=True" "${T}" \
    conditions_are TransportReady=True/PylonConnected Registered=True/RegisteredWithRouter || true
  expect "modelName restored: GET /v1/models lists ${MODEL}" "${T}" models_lists_test_model || true
  step_done

  run_restart_cases

  section "Failure path: endpoint deleted"
  ensure_context
  kc -n "${E2E_MODELS_NAMESPACE}" delete inferenceendpoint "${ENDPOINT}" --wait=false >/dev/null
  expect "endpoint deleted: InferenceEndpoint gone" "${T}" endpoint_gone || true
  expect "endpoint deleted: transport Deployment garbage-collected" "${T}" transport_deployment_gone || true
  expect "endpoint deleted: GET /v1/models no longer lists ${MODEL}" "${T}" models_omit_test_model || true
  step_done

  if [ "${FAIL_COUNT}" -gt 0 ]; then
    dump_diagnostics
  fi

  if [ "${E2E_CLEANUP}" = "1" ]; then
    stop_port_forward
    cleanup
  else
    # Leave the stack in its steady state for inspection.
    ensure_context
    kc -n "${E2E_MODELS_NAMESPACE}" apply -f "${MANIFESTS}/inference-endpoint.yaml" >/dev/null || true
    log "re-applied InferenceEndpoint ${E2E_MODELS_NAMESPACE}/${ENDPOINT} for inspection; set E2E_CLEANUP=1 to remove everything"
  fi

  print_summary
  [ "${FAIL_COUNT}" -eq 0 ]
}

main "$@"
