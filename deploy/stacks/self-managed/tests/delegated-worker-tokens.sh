#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="delegated-worker-tokens-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "delegated-worker-tokens: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  local enabled="$1"

  if [ -z "$enabled" ]; then
    printf '{}\n' >"$environment_file"
    return
  fi
  printf '%s\n' \
    'security:' \
    '  delegatedWorkerTokens:' \
    "    enabled: $enabled" \
    >"$environment_file"
}

render_values() {
  local release_name="$1"
  local output_file="$2"

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
      --selector "name=$release_name" \
      write-values \
      --output-file-template "$output_file"
}

# Walks api.remoteConfig.configData.nvcf.worker.delegated-token.enabled by
# indentation so a key rendered under another release or path is not accepted.
read_nvcf_delegated_token_enabled() {
  local values_file="$1"

  awk '
    /^[[:space:]]*($|#)/ { next }

    /^[^[:space:]]/ {
      in_api = ($0 == "api:")
      in_remote_config = in_config_data = in_nvcf = in_worker = in_delegated = 0
      next
    }
    in_api && /^  [^[:space:]]/ {
      in_remote_config = ($0 == "  remoteConfig:")
      in_config_data = in_nvcf = in_worker = in_delegated = 0
      next
    }
    in_remote_config && /^    [^[:space:]]/ {
      in_config_data = ($0 == "    configData:")
      in_nvcf = in_worker = in_delegated = 0
      next
    }
    in_config_data && /^      [^[:space:]]/ {
      in_nvcf = ($0 == "      nvcf:")
      in_worker = in_delegated = 0
      next
    }
    in_nvcf && /^        [^[:space:]]/ {
      in_worker = ($0 == "        worker:")
      in_delegated = 0
      next
    }
    in_worker && /^          [^[:space:]]/ {
      in_delegated = ($0 == "          delegated-token:")
      next
    }
    in_delegated && /^            [^[:space:]]/ {
      if ($0 ~ /^            enabled:[[:space:]]*/) {
        value = $0
        sub(/^            enabled:[[:space:]]*/, "", value)
        print value
        found = 1
        exit
      }
      next
    }

    END { if (!found) exit 1 }
  ' "$values_file"
}

# Walks nvctApi.env.NVCT_WORKER_DELEGATEDTOKEN_ENABLED by indentation.
read_nvct_delegated_token_enabled() {
  local values_file="$1"

  awk '
    /^[[:space:]]*($|#)/ { next }

    /^[^[:space:]]/ {
      in_nvct = ($0 == "nvctApi:")
      in_env = 0
      next
    }
    in_nvct && /^  [^[:space:]]/ {
      in_env = ($0 == "  env:")
      next
    }
    in_env && /^    [^[:space:]]/ {
      if ($0 ~ /^    NVCT_WORKER_DELEGATEDTOKEN_ENABLED:[[:space:]]*/) {
        value = $0
        sub(/^    NVCT_WORKER_DELEGATEDTOKEN_ENABLED:[[:space:]]*/, "", value)
        first = substr(value, 1, 1)
        last = substr(value, length(value), 1)
        if ((first == "\"" && last == "\"") ||
            (first == sprintf("%c", 39) && last == sprintf("%c", 39))) {
          value = substr(value, 2, length(value) - 2)
        }
        print value
        found = 1
        exit
      }
      next
    }

    END { if (!found) exit 1 }
  ' "$values_file"
}

assert_rendered() {
  local case_name="$1"
  local expected="$2"
  local api_values="$work_dir/$case_name-api-values.yaml"
  local nvct_values="$work_dir/$case_name-nvct-values.yaml"
  local actual

  render_values api "$api_values" >/dev/null
  render_values nvct-api "$nvct_values" >/dev/null

  actual="$(read_nvcf_delegated_token_enabled "$api_values")" ||
    fail "$case_name: nvcf.worker.delegated-token.enabled was not rendered in API remote config"
  test "$actual" = "$expected" ||
    fail "$case_name: expected nvcf.worker.delegated-token.enabled=$expected, got $actual"

  actual="$(read_nvct_delegated_token_enabled "$nvct_values")" ||
    fail "$case_name: NVCT_WORKER_DELEGATEDTOKEN_ENABLED was not rendered in nvctApi.env"
  test "$actual" = "$expected" ||
    fail "$case_name: expected NVCT_WORKER_DELEGATEDTOKEN_ENABLED=$expected, got $actual"

  # The service-side property keys are nvcf.worker.delegated-token.enabled and
  # nvct.worker.delegated-token.enabled; the previous spellings did not bind.
  if grep -Eq 'delegated-token-enabled|NVCT_WORKER_DELEGATED_TOKEN_ENABLED' \
    "$api_values" "$nvct_values"; then
    fail "$case_name: a stale delegated-token key spelling was rendered"
  fi
}

write_environment ''
assert_rendered default false

write_environment false
assert_rendered disabled false

write_environment true
assert_rendered enabled true

echo "delegated-worker-tokens: all checks passed"
