#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="llm-router-worker-address-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-router-worker-address: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  local enabled="$1"
  local worker_address="$2"

  {
    printf '%s\n' \
      'global:' \
      '  workerEndpoints:'
    printf '    llmRequestRouterAddress: %s\n' "$worker_address"
    printf '%s\n' \
      'addons:' \
      '  llm:'
    printf '    enabled: %s\n' "$enabled"
  } >"$environment_file"
}

render_api_values() {
  local output_file="$1"
  shift

  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      "$@" \
      --selector name=api \
      write-values \
      --output-file-template "$output_file"
}

read_remote_config_address() {
  local values_file="$1"

  awk '
    /^[[:space:]]*($|#)/ { next }

    /^[^[:space:]]/ {
      in_api = ($0 == "api:")
      in_remote_config = in_config_data = in_nvcf = in_router = 0
      next
    }

    in_api && /^  [^[:space:]]/ {
      in_remote_config = ($0 == "  remoteConfig:")
      in_config_data = in_nvcf = in_router = 0
      next
    }

    in_remote_config && /^    [^[:space:]]/ {
      in_config_data = ($0 == "    configData:")
      in_nvcf = in_router = 0
      next
    }

    in_config_data && /^      [^[:space:]]/ {
      in_nvcf = ($0 == "      nvcf:")
      in_router = 0
      next
    }

    in_nvcf && /^        [^[:space:]]/ {
      in_router = ($0 == "        llm-request-router:")
      next
    }

    in_router && /^          [^[:space:]]/ {
      if ($0 ~ /^          worker-address:[[:space:]]*/) {
        address = $0
        sub(/^          worker-address:[[:space:]]*/, "", address)
        print address
        found = 1
        exit
      }
      next
    }

    END { if (!found) exit 1 }
  ' "$values_file"
}

assert_remote_config_address() {
  local values_file="$1"
  local expected_address="$2"
  local actual_address

  actual_address="$(read_remote_config_address "$values_file")" || return 1
  test "$actual_address" = "$expected_address"
}

wrong_owner_address='wrong-owner.example.com:50071'
printf '%s\n' \
  'api:' \
  '  image:' \
  '    repository: example/api' \
  'wrongOwner:' \
  '  remoteConfig:' \
  '    configData:' \
  '      nvcf:' \
  '        llm-request-router:' \
  "          worker-address: $wrong_owner_address" \
  >"$work_dir/wrong-owner-values.yaml"
if assert_remote_config_address "$work_dir/wrong-owner-values.yaml" \
  "$wrong_owner_address"; then
  fail "remote-config assertion accepted a worker address outside the API values"
fi

local_worker_address='llm-request-router.nvcf.svc.cluster.local:50071'
write_environment true "$local_worker_address"
render_api_values "$work_dir/local-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/local-api-values.yaml" \
  "$local_worker_address" ||
  fail "enabled local LLM did not render the worker address in API remote config"
if grep -Eq 'NVCF_(LLM_REQUEST_ROUTER_WORKER_ADDRESS|STARGATE_ADDRESS)' \
  "$work_dir/local-api-values.yaml"; then
  fail "enabled LLM rendered the worker address through the legacy API env path"
fi

external_worker_address='router.example.com:443'
render_api_values \
  "$work_dir/external-api-values.yaml" \
  --state-values-set-string \
  "global.workerEndpoints.llmRequestRouterAddress=$external_worker_address" \
  >/dev/null
assert_remote_config_address "$work_dir/external-api-values.yaml" \
  "$external_worker_address" ||
  fail "enabled LLM did not honor an explicit external worker address"

write_environment true '""'
if render_api_values "$work_dir/blank-api-values.yaml" \
  >"$work_dir/blank-render.log" 2>&1; then
  fail "enabled LLM accepted a blank worker address"
fi
grep -Fq \
  'global.workerEndpoints.llmRequestRouterAddress is required when addons.llm.enabled is true' \
  "$work_dir/blank-render.log" ||
  fail "enabled LLM blank worker address did not return the expected error"

staged_worker_address='staged-router.example.com:50071'
write_environment false "$staged_worker_address"
render_api_values "$work_dir/disabled-api-values.yaml" >/dev/null
if read_remote_config_address "$work_dir/disabled-api-values.yaml" >/dev/null; then
  fail "disabled LLM supplied the worker-address API remote-config key"
fi
if grep -Fq "$staged_worker_address" "$work_dir/disabled-api-values.yaml"; then
  fail "disabled LLM supplied a staged worker address to the API chart"
fi

echo "llm-router-worker-address: all checks passed"
