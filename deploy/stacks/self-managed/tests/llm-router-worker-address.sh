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
    printf "    llmRequestRouterAddress: '%s'\n" "$worker_address"
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
        first = substr(address, 1, 1)
        last = substr(address, length(address), 1)
        if ((first == "\"" && last == "\"") ||
            (first == sprintf("%c", 39) && last == sprintf("%c", 39))) {
          address = substr(address, 2, length(address) - 2)
        }
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

invalid_worker_address_error='global.workerEndpoints.llmRequestRouterAddress must use DNS-or-IPv4:port or [IPv6]:port with port 1-65535'

assert_worker_address_rejected() {
  local case_name="$1"
  local worker_address="$2"
  local values_file="$work_dir/$case_name-api-values.yaml"
  local log_file="$work_dir/$case_name-render.log"

  write_environment true "$worker_address"
  if render_api_values "$values_file" >"$log_file" 2>&1; then
    echo "llm-router-worker-address: enabled LLM accepted invalid worker address: $worker_address" >&2
    return 1
  fi
  if ! grep -Fq "$invalid_worker_address_error" "$log_file"; then
    echo "llm-router-worker-address: invalid worker address did not return the expected error: $worker_address" >&2
    return 1
  fi
}

assert_worker_address_accepted() {
  local case_name="$1"
  local worker_address="$2"
  local values_file="$work_dir/$case_name-api-values.yaml"
  local log_file="$work_dir/$case_name-render.log"

  write_environment true "$worker_address"
  if ! render_api_values "$values_file" >"$log_file" 2>&1; then
    echo "llm-router-worker-address: enabled LLM rejected valid worker address: $worker_address" >&2
    return 1
  fi
  if ! assert_remote_config_address "$values_file" "$worker_address"; then
    echo "llm-router-worker-address: valid worker address was not rendered in API remote config: $worker_address" >&2
    return 1
  fi
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
printf '%s\n' \
  'addons:' \
  '  llm:' \
  '    enabled: true' \
  >"$environment_file"
render_api_values "$work_dir/local-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/local-api-values.yaml" \
  "$local_worker_address" ||
  fail "enabled local LLM did not default the worker address in API remote config"
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

ipv4_worker_address='192.0.2.10:50071'
write_environment true "$ipv4_worker_address"
render_api_values "$work_dir/ipv4-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/ipv4-api-values.yaml" \
  "$ipv4_worker_address" ||
  fail "enabled LLM did not accept an IPv4 worker address"

ipv6_worker_address='[2001:db8::1]:50071'
write_environment true "$ipv6_worker_address"
render_api_values "$work_dir/ipv6-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/ipv6-api-values.yaml" \
  "$ipv6_worker_address" ||
  fail "enabled LLM did not accept a bracketed IPv6 worker address"

valid_ipv6_address_cases=(
  'ipv6-full|[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:50071'
  'ipv6-unspecified|[::]:50071'
  'ipv6-loopback|[::1]:50071'
  'ipv6-trailing-compression|[2001:db8::]:50071'
  'ipv6-embedded-ipv4|[::ffff:192.0.2.128]:50071'
  'ipv6-full-embedded-ipv4|[2001:db8:0:1:1:1:192.0.2.128]:50071'
  'ipv6-compressed-embedded-ipv4|[2001:db8:3:4::192.0.2.33]:50071'
)
for valid_ipv6_address_case in "${valid_ipv6_address_cases[@]}"; do
  IFS='|' read -r case_name worker_address <<<"$valid_ipv6_address_case"
  assert_worker_address_accepted "$case_name" "$worker_address" ||
    fail "enabled LLM rejected a valid bracketed IPv6 worker address"
done

minimum_port_worker_address='router.example.com:1'
write_environment true "$minimum_port_worker_address"
render_api_values "$work_dir/minimum-port-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/minimum-port-api-values.yaml" \
  "$minimum_port_worker_address" ||
  fail "enabled LLM did not accept worker-address port 1"

maximum_port_worker_address='router.example.com:65535'
write_environment true "$maximum_port_worker_address"
render_api_values "$work_dir/maximum-port-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/maximum-port-api-values.yaml" \
  "$maximum_port_worker_address" ||
  fail "enabled LLM did not accept worker-address port 65535"

write_environment true ''
render_api_values "$work_dir/blank-api-values.yaml" >/dev/null
assert_remote_config_address "$work_dir/blank-api-values.yaml" \
  "$local_worker_address" ||
  fail "enabled local LLM did not default a blank worker address"

invalid_address_cases=(
  'missing-port|router'
  'missing-host|:50071'
  'non-numeric-port|router:not-a-port'
  'port-zero|router:0'
  'port-too-large|router:65536'
  'port-too-long|router:99999999999999999999999999999999999999'
  'malformed-ipv6|[::::]:50071'
  'incomplete-ipv6|[1:2:3]:50071'
  'triple-colon-ipv6|[2001:db8:::1]:50071'
  'multiple-compression-ipv6|[2001::db8::1]:50071'
  'oversized-hextet-ipv6|[12345::1]:50071'
  'too-many-hextets-ipv6|[1:2:3:4:5:6:7:8:9]:50071'
  'too-few-hextets-ipv6|[1:2:3:4:5:6:7]:50071'
  'invalid-embedded-ipv4-octet|[::ffff:256.0.2.1]:50071'
  'incomplete-embedded-ipv4|[::ffff:192.0.2]:50071'
  'leading-single-colon-ipv6|[:1::2]:50071'
  'trailing-single-colon-ipv6|[1::2:]:50071'
)
address_validation_failed=false
for invalid_address_case in "${invalid_address_cases[@]}"; do
  IFS='|' read -r case_name worker_address <<<"$invalid_address_case"
  if ! assert_worker_address_rejected "$case_name" "$worker_address"; then
    address_validation_failed=true
  fi
done
if "$address_validation_failed"; then
  fail "enabled LLM accepted one or more invalid worker addresses"
fi

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
