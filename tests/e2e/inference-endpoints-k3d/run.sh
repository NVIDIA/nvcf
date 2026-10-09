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
E2E_SAMPLE_IMAGE="${E2E_SAMPLE_IMAGE:-docker.io/library/openai-compatible-sample:e2e}"
E2E_DRY_RUN="${E2E_DRY_RUN:-0}"
# Where the charts and images come from. local installs the charts from this
# checkout with the images imported into k3d. ngc installs the published
# charts and images named in PUBLISHED_VERSIONS_FILE, the versions file
# produced by your publishing pipeline (README.md documents its format), and
# pulls them with NGC_API_KEY.
IMAGE_SOURCE="${IMAGE_SOURCE:-local}"
PUBLISHED_VERSIONS_FILE="${PUBLISHED_VERSIONS_FILE:-}"
NGC_API_KEY="${NGC_API_KEY:-}"

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
readonly PULL_SECRET="ngc-pull"

CA_FILE="${E2E_WORK_DIR}/ca.crt"
AUTH_HEADER_FILE="${E2E_WORK_DIR}/auth-header"
API_KEY_FILE="${E2E_WORK_DIR}/api-key"
PF_LOG="${E2E_WORK_DIR}/port-forward.log"
PF_PID=""
REGISTRY_CONFIG="${E2E_WORK_DIR}/registry-config.json"

# Chart sources and image settings, set by resolve_image_source. The defaults
# are those of IMAGE_SOURCE=local. A chart source is the chart reference,
# followed by --version for a published chart.
STACK_SOURCE=("${STACK_CHART}")
OPERATOR_SOURCE=("${OPERATOR_CHART}")
STACK_ARGS=()
OPERATOR_ARGS=()
SAMPLE_IMAGE="${E2E_SAMPLE_IMAGE}"
SAMPLE_IMAGE_PULL_SECRETS="[]"
REGISTRY_HOST=""
ROUTER_IMAGE_REGISTRY=""
ROUTER_IMAGE_REPOSITORY=""
ROUTER_IMAGE_TAG=""
GATEWAY_IMAGE_REGISTRY=""
GATEWAY_IMAGE_REPOSITORY=""
GATEWAY_IMAGE_TAG=""
OPERATOR_IMAGE_REPOSITORY=""
OPERATOR_IMAGE_TAG=""
PYLON_IMAGE_REPOSITORY=""
PYLON_IMAGE_TAG=""

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
# Image source

# read_version VAR KEY...: sets VAR to the string at the key path KEY... of
# PUBLISHED_VERSIONS_FILE, for example read_version tag images stargate tag.
# Dies when it is missing or has characters that do not belong in an image or
# chart reference, so the value is safe in YAML and sed.
read_version() {
  local var="$1" path="" key value label
  shift
  for key in "$@"; do
    path="${path}${path:+,}\"${key}\""
  done
  label="$(IFS=.; printf '%s' "$*")"
  # $path is a jq variable.
  # shellcheck disable=SC2016
  value="$(jq -r --argjson path "[${path}]" 'getpath($path) // empty | strings' "${PUBLISHED_VERSIONS_FILE}")" \
    || die "cannot read ${label} from ${PUBLISHED_VERSIONS_FILE} with jq"
  case "${value}" in
    "") die "${PUBLISHED_VERSIONS_FILE}: ${label} is missing or not a string" ;;
    *[!A-Za-z0-9._:/@+-]*) die "${PUBLISHED_VERSIONS_FILE}: ${label} has unexpected characters: ${value}" ;;
  esac
  printf -v "${var}" '%s' "${value}"
}

# split_repository REGISTRY_VAR REPOSITORY_VAR REGISTRY REPOSITORY: the
# subcharts join image.registry and image.repository with a slash, so a
# repository under REGISTRY is split into the two. Any other repository is
# used whole, with an empty registry.
split_repository() {
  case "$4" in
    "$3"/?*)
      printf -v "$1" '%s' "$3"
      printf -v "$2" '%s' "${4#"$3"/}"
      ;;
    *)
      printf -v "$1" '%s' ""
      printf -v "$2" '%s' "$4"
      ;;
  esac
}

resolve_image_source() {
  case "${IMAGE_SOURCE}" in
    local) return 0 ;;
    ngc) ;;
    *) die "IMAGE_SOURCE must be local or ngc, got '${IMAGE_SOURCE}'" ;;
  esac
  command -v jq >/dev/null 2>&1 || die "jq is required with IMAGE_SOURCE=ngc"
  [ -n "${PUBLISHED_VERSIONS_FILE}" ] \
    || die "PUBLISHED_VERSIONS_FILE is required with IMAGE_SOURCE=ngc: the versions file produced by your publishing pipeline"
  [ -f "${PUBLISHED_VERSIONS_FILE}" ] && [ -r "${PUBLISHED_VERSIONS_FILE}" ] \
    || die "PUBLISHED_VERSIONS_FILE '${PUBLISHED_VERSIONS_FILE}' is not a readable file"
  if [ -z "${NGC_API_KEY}" ] && [ "${E2E_DRY_RUN}" != "1" ]; then
    die "NGC_API_KEY is required with IMAGE_SOURCE=ngc"
  fi
  jq -e '.version == 1' "${PUBLISHED_VERSIONS_FILE}" >/dev/null 2>&1 \
    || die "${PUBLISHED_VERSIONS_FILE}: expected a versions file with \"version\": 1"

  local registry chart_ref chart_version repository tag
  read_version registry registry
  REGISTRY_HOST="${registry%%/*}"

  read_version chart_ref charts llm-gateway-stack ref
  read_version chart_version charts llm-gateway-stack version
  case "${chart_ref}" in oci://?*) ;; *) die "${PUBLISHED_VERSIONS_FILE}: charts.llm-gateway-stack.ref must start with oci://" ;; esac
  STACK_SOURCE=("${chart_ref}" --version "${chart_version}")
  read_version chart_ref charts pylon-operator ref
  read_version chart_version charts pylon-operator version
  case "${chart_ref}" in oci://?*) ;; *) die "${PUBLISHED_VERSIONS_FILE}: charts.pylon-operator.ref must start with oci://" ;; esac
  OPERATOR_SOURCE=("${chart_ref}" --version "${chart_version}")

  read_version repository images stargate repository
  split_repository ROUTER_IMAGE_REGISTRY ROUTER_IMAGE_REPOSITORY "${registry}" "${repository}"
  read_version ROUTER_IMAGE_TAG images stargate tag
  read_version repository images llm-api-gateway repository
  split_repository GATEWAY_IMAGE_REGISTRY GATEWAY_IMAGE_REPOSITORY "${registry}" "${repository}"
  read_version GATEWAY_IMAGE_TAG images llm-api-gateway tag
  # The operator image is published under its service name,
  # nvcf-pylon-operator, because the chart already takes pylon-operator in
  # the same registry path. Its chart key stays pylon-operator.
  if ! jq -e '.images | has("nvcf-pylon-operator")' "${PUBLISHED_VERSIONS_FILE}" >/dev/null 2>&1 \
    && jq -e '.images | has("pylon-operator")' "${PUBLISHED_VERSIONS_FILE}" >/dev/null 2>&1; then
    die "${PUBLISHED_VERSIONS_FILE}: the operator image key is images.nvcf-pylon-operator, but the file has only images.pylon-operator; see the versions file format in tests/e2e/inference-endpoints-k3d/README.md"
  fi
  read_version OPERATOR_IMAGE_REPOSITORY images nvcf-pylon-operator repository
  read_version OPERATOR_IMAGE_TAG images nvcf-pylon-operator tag
  read_version PYLON_IMAGE_REPOSITORY images pylon repository
  read_version PYLON_IMAGE_TAG images pylon tag
  read_version repository images openai-compatible-sample repository
  read_version tag images openai-compatible-sample tag
  SAMPLE_IMAGE="${repository}:${tag}"
  SAMPLE_IMAGE_PULL_SECRETS="[{\"name\": \"${PULL_SECRET}\"}]"

  log "IMAGE_SOURCE=ngc: ${PUBLISHED_VERSIONS_FILE}, run $(jq -r '.run // "?"' "${PUBLISHED_VERSIONS_FILE}"), commit $(jq -r '.commit // "?"' "${PUBLISHED_VERSIONS_FILE}" | cut -c1-12), registry ${registry}"
}

# IMAGE_SOURCE=ngc: writes REGISTRY_CONFIG, a Docker config file (mode 600)
# with the NGC credential for REGISTRY_HOST. It is the .dockerconfigjson of
# the pull Secret and Helm's registry config for the OCI charts, so Helm
# neither reads nor changes your own registry login or credential store. The
# API key never appears on a command line, and on_exit removes the file.
write_registry_config() {
  local auth
  # $oauthtoken is the literal NGC user name, not a variable.
  # shellcheck disable=SC2016
  auth="$(printf '%s:%s' '$oauthtoken' "${NGC_API_KEY}" | base64 | tr -d '\n')"
  (
    umask 077
    printf '{"auths":{"%s":{"auth":"%s"}}}\n' "${REGISTRY_HOST}" "${auth}" >"${REGISTRY_CONFIG}"
  )
  export HELM_REGISTRY_CONFIG="${REGISTRY_CONFIG}"
}

# sed_escape STRING: STRING as a sed replacement with | as the delimiter.
sed_escape() {
  printf '%s' "$1" | sed -e 's/[\\|&]/\\&/g'
}

# render_sample_backend OUTPUT: manifests/sample-backend.yaml with the sample
# image and its pull Secrets filled in.
render_sample_backend() {
  sed -e "s|\${E2E_SAMPLE_IMAGE}|$(sed_escape "${SAMPLE_IMAGE}")|g" \
    -e "s|\${E2E_SAMPLE_IMAGE_PULL_SECRETS}|$(sed_escape "${SAMPLE_IMAGE_PULL_SECRETS}")|g" \
    "${MANIFESTS}/sample-backend.yaml" >"$1"
  if grep -qF "\${" "$1"; then
    die "$1 has a placeholder that run.sh does not fill in"
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
  rm -f "${REGISTRY_CONFIG}"
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

# IMAGE_SOURCE=ngc: the pull Secret in every namespace that runs a pod. The
# operator does not copy it, so the transport pods need it in the models
# namespace.
create_pull_secrets() {
  ensure_context
  local ns
  for ns in "${E2E_STACK_NAMESPACE}" "${E2E_OPERATOR_NAMESPACE}" "${E2E_MODELS_NAMESPACE}"; do
    kc -n "${ns}" create secret generic "${PULL_SECRET}" --type=kubernetes.io/dockerconfigjson \
      --from-file=.dockerconfigjson="${REGISTRY_CONFIG}" --dry-run=client -o yaml | kc apply -f - >/dev/null
  done
  log "image pull Secret ${PULL_SECRET} for ${REGISTRY_HOST} in ${E2E_STACK_NAMESPACE}, ${E2E_OPERATOR_NAMESPACE}, ${E2E_MODELS_NAMESPACE}"
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

# prepare_stack: writes the stack values files and sets STACK_ARGS, the helm
# arguments after the chart source. With IMAGE_SOURCE=ngc a second values
# file points the images at the registry and adds the pull Secret.
prepare_stack() {
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
  STACK_ARGS=(--namespace "${E2E_STACK_NAMESPACE}" --values "${values}")
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    local image_values="${E2E_WORK_DIR}/llm-gateway-stack-ngc-values.yaml"
    cat >"${image_values}" <<EOF
llm-request-router:
  llmRequestRouter:
    image:
      registry: "${ROUTER_IMAGE_REGISTRY}"
      repository: "${ROUTER_IMAGE_REPOSITORY}"
      tag: "${ROUTER_IMAGE_TAG}"
    imagePullSecrets:
      - name: ${PULL_SECRET}
llm-api-gateway:
  llmApiGateway:
    image:
      registry: "${GATEWAY_IMAGE_REGISTRY}"
      repository: "${GATEWAY_IMAGE_REPOSITORY}"
      tag: "${GATEWAY_IMAGE_TAG}"
    imagePullSecrets:
      - name: ${PULL_SECRET}
EOF
    STACK_ARGS+=(--values "${image_values}")
  fi
}

install_stack() {
  ensure_context
  prepare_stack
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    log "chart ${STACK_SOURCE[*]}"
  else
    log "helm dependency build ${STACK_CHART}"
    helm dependency build --skip-refresh "${STACK_CHART}" >/dev/null
  fi
  log "helm upgrade --install ${STACK_RELEASE} (namespace ${E2E_STACK_NAMESPACE})"
  if ! hm upgrade --install "${STACK_RELEASE}" "${STACK_SOURCE[@]}" "${STACK_ARGS[@]}" \
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

# prepare_operator: writes the operator values files and sets OPERATOR_ARGS,
# like prepare_stack. With IMAGE_SOURCE=ngc the second values file also gives
# the transport pods the pull Secret through pylon.imagePullSecrets.
prepare_operator() {
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
  OPERATOR_ARGS=(--namespace "${E2E_OPERATOR_NAMESPACE}" --values "${values}")
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    local image_values="${E2E_WORK_DIR}/pylon-operator-ngc-values.yaml"
    cat >"${image_values}" <<EOF
image:
  repository: "${OPERATOR_IMAGE_REPOSITORY}"
  tag: "${OPERATOR_IMAGE_TAG}"
imagePullSecrets:
  - name: ${PULL_SECRET}
pylon:
  image:
    repository: "${PYLON_IMAGE_REPOSITORY}"
    tag: "${PYLON_IMAGE_TAG}"
  imagePullSecrets:
    - name: ${PULL_SECRET}
EOF
    OPERATOR_ARGS+=(--values "${image_values}")
  fi
}

install_operator() {
  ensure_context
  prepare_operator
  local insecure=false
  [ "${E2E_DEV_INSECURE_TRANSPORT}" = "1" ] && insecure=true
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    log "chart ${OPERATOR_SOURCE[*]}"
  fi
  log "helm upgrade --install ${OPERATOR_RELEASE} (namespace ${E2E_OPERATOR_NAMESPACE}), router ${E2E_ROUTER_GRPC_ADDRESS}, devInsecureTransport=${insecure}"
  if ! hm upgrade --install "${OPERATOR_RELEASE}" "${OPERATOR_SOURCE[@]}" "${OPERATOR_ARGS[@]}" \
    --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-operator.log" 2>&1; then
    cat "${E2E_WORK_DIR}/helm-operator.log"
    fail "helm install ${OPERATOR_RELEASE}"
    return 1
  fi
  pass "helm install ${OPERATOR_RELEASE}"
}

deploy_backend() {
  ensure_context
  local manifest="${E2E_WORK_DIR}/sample-backend.yaml"
  render_sample_backend "${manifest}"
  kc -n "${E2E_MODELS_NAMESPACE}" apply -f "${manifest}" >/dev/null
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

# print_command ARGS...: one shell-quoted command line.
print_command() {
  printf ' '
  printf ' %q' "$@"
  printf '\n'
}

# E2E_DRY_RUN=1: writes the values files and the sample manifest to the work
# directory, renders both charts from this checkout with those values, and
# prints the commands a run would use. It touches no cluster and reads or
# writes no credential. With IMAGE_SOURCE=ngc a run installs the published
# charts instead of the checkout's; they are the same charts with their
# image tags set.
dry_run() {
  command -v helm >/dev/null 2>&1 || die "helm is required"
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  section "Dry run (E2E_DRY_RUN=1, IMAGE_SOURCE=${IMAGE_SOURCE}): no cluster access"
  if [ "${IMAGE_SOURCE}" = "ngc" ] && [ -z "${NGC_API_KEY}" ]; then
    log "NGC_API_KEY is not set; a run needs it"
  fi
  CLUSTER_TOKEN_SHA256="$(sha256_hex dry-run-cluster-token)"
  API_KEY_SHA256="$(sha256_hex dry-run-api-key)"
  prepare_stack
  prepare_operator
  local sample="${E2E_WORK_DIR}/sample-backend.yaml"
  local stack_manifest="${E2E_WORK_DIR}/dry-run-${STACK_RELEASE}.yaml"
  local operator_manifest="${E2E_WORK_DIR}/dry-run-${OPERATOR_RELEASE}.yaml"
  render_sample_backend "${sample}"
  helm dependency build --skip-refresh "${STACK_CHART}" >/dev/null
  helm template "${STACK_RELEASE}" "${STACK_CHART}" "${STACK_ARGS[@]}" >"${stack_manifest}" \
    || die "helm template ${STACK_RELEASE} failed"
  helm template "${OPERATOR_RELEASE}" "${OPERATOR_CHART}" "${OPERATOR_ARGS[@]}" >"${operator_manifest}" \
    || die "helm template ${OPERATOR_RELEASE} failed"

  printf '\nA run with these settings installs:\n'
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    printf '  (Helm reads the NGC credential from a registry config in %s)\n' "${E2E_WORK_DIR}"
    printf '  (pull Secret %s in %s, %s and %s)\n' "${PULL_SECRET}" \
      "${E2E_STACK_NAMESPACE}" "${E2E_OPERATOR_NAMESPACE}" "${E2E_MODELS_NAMESPACE}"
  fi
  print_command helm --kube-context "${E2E_KUBE_CONTEXT}" upgrade --install "${STACK_RELEASE}" \
    "${STACK_SOURCE[@]}" "${STACK_ARGS[@]}" --wait --timeout "${E2E_HELM_TIMEOUT}"
  print_command helm --kube-context "${E2E_KUBE_CONTEXT}" upgrade --install "${OPERATOR_RELEASE}" \
    "${OPERATOR_SOURCE[@]}" "${OPERATOR_ARGS[@]}" --wait --timeout "${E2E_HELM_TIMEOUT}"
  print_command kubectl --context "${E2E_KUBE_CONTEXT}" -n "${E2E_MODELS_NAMESPACE}" apply -f "${sample}"

  printf '\nImages and pull Secrets in the rendered manifests:\n'
  local file
  for file in "${stack_manifest}" "${operator_manifest}" "${sample}"; do
    printf '%s\n' "${file}"
    grep -E -- "^ *image: |--pylon-image|^ *imagePullSecrets:|^ *- name: ${PULL_SECRET}\$" "${file}" || true
  done
}

# Restart and failure cases: helpers, faults and run_restart_cases.
# shellcheck source=restart-cases.sh
source "${SCRIPT_DIR}/restart-cases.sh"

# ---------------------------------------------------------------------------
# Main

main() {
  local T="${E2E_TIMEOUT}"
  resolve_image_source
  if [ "${E2E_DRY_RUN}" = "1" ]; then
    dry_run
    return
  fi
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    if [ "${E2E_CLEANUP}" = "1" ]; then
      die "E2E_DEPLOY_ONLY=1 leaves everything installed and cannot be combined with E2E_CLEANUP=1"
    fi
    log "E2E_DEPLOY_ONLY=1: stopping once the InferenceEndpoint is ready"
    REMAINING_STEPS="setup|steady state conditions|registration status|printer columns|"
  else
    REMAINING_STEPS="setup|steady state conditions|registration status|printer columns|gateway checks|backend scaled to zero|backend restored|model name mismatch|model name restored|backend killed|router restart|gateway restart|operator down|Pylon restart|endpoint deleted|"
  fi

  section "Setup"
  preflight
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    write_registry_config
  fi
  generate_credentials
  create_namespaces
  if [ "${IMAGE_SOURCE}" = "ngc" ]; then
    create_pull_secrets
  fi
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

  section "Failure path: backend scaled to zero"
  scale_backend 0
  expect "backend at 0: Ready=False/NoReadyEndpoints" "${T}" conditions_are Ready=False/NoReadyEndpoints || true
  hold "backend at 0: Registered stays True/RegisteredWithRouter" 20 conditions_are Registered=True/RegisteredWithRouter || true
  expect "backend at 0, stack: registry Unhealthy, 1 registered, 0 healthy" 60 registry_is Unhealthy 1 0 || true
  expect "backend at 0, stack: GET /v1/models omits ${MODEL}" 30 models_omit_test_model || true
  expect "backend at 0, stack: chat returns 503 from the router" 30 chat_status_is 503 || true
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
