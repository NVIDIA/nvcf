#!/usr/bin/env bash
# Test that the in-cluster ESS base URL for the NVCF/NVCT API is configurable
# separately from the worker-advertised ESS endpoint. The stack used to derive
# both the API's own NVCF_ESS_BASE_URL / NVCT_ESS_BASE_URL and the worker alias
# (NVCF_ESS_WORKER_BASE_URL / NVCT_ESS_WORKER_BASE_URL) from the single
# global.workerEndpoints.essServiceURL value, so a split-plane operator could
# not keep workers on the public ESS URL while pointing the in-cluster API at
# the in-cluster ess-api Service. api.essBaseURL / nvctApi.essBaseURL now
# override only the self base URL and default to essServiceURL, so existing
# installs are unchanged.
#
# global.yaml.gotmpl is the shared values source for every release, so it emits
# both the api: and nvctApi: top-level blocks into any release it renders;
# rendering the api release therefore exposes both for assertion.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="ess-base-url-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "ess-base-url-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_values() {
  local output_file="$1"
  local render_log="$work_dir/write-values.log"
  if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=api \
      write-values \
      --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "helmfile wrote no values to $output_file"
}

assert_value() {
  local file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

public_url="https://ess.example.test"
incluster_url="http://ess-api.ess.svc.cluster.local:8080"

# ---------------------------------------------------------------------------
# 1. Default: essServiceURL set, no essBaseURL override. Both the self base URL
#    and the worker alias track essServiceURL -- unchanged behavior.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  workerEndpoints:
    essServiceURL: $public_url
EOF

default_values="$work_dir/default-values.yaml"
render_values "$default_values"
assert_value "$default_values" '.api.env.NVCF_ESS_BASE_URL' "$public_url" \
  "default: NVCF_ESS_BASE_URL tracks essServiceURL"
assert_value "$default_values" '.api.env.NVCF_ESS_WORKER_BASE_URL' "$public_url" \
  "default: NVCF_ESS_WORKER_BASE_URL tracks essServiceURL"
assert_value "$default_values" '.nvctApi.env.NVCT_ESS_BASE_URL' "$public_url" \
  "default: NVCT_ESS_BASE_URL tracks essServiceURL"
assert_value "$default_values" '.nvctApi.env.NVCT_ESS_WORKER_BASE_URL' "$public_url" \
  "default: NVCT_ESS_WORKER_BASE_URL tracks essServiceURL"
assert_value "$default_values" '.nvctApi.image.tag' null \
  "default: NVCT image tag is supplied by the chart"

# ---------------------------------------------------------------------------
# 2. Override: essServiceURL is the public URL for workers; essBaseURL points
#    the in-cluster API at the in-cluster Service. Only the self base URL
#    changes; the worker alias stays on essServiceURL.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  workerEndpoints:
    essServiceURL: $public_url
api:
  essBaseURL: $incluster_url
nvctApi:
  essBaseURL: $incluster_url
EOF

override_values="$work_dir/override-values.yaml"
render_values "$override_values"
assert_value "$override_values" '.api.env.NVCF_ESS_BASE_URL' "$incluster_url" \
  "override: NVCF_ESS_BASE_URL uses api.essBaseURL"
assert_value "$override_values" '.api.env.NVCF_ESS_WORKER_BASE_URL' "$public_url" \
  "override: NVCF_ESS_WORKER_BASE_URL stays on essServiceURL"
assert_value "$override_values" '.nvctApi.env.NVCT_ESS_BASE_URL' "$incluster_url" \
  "override: NVCT_ESS_BASE_URL uses nvctApi.essBaseURL"
assert_value "$override_values" '.nvctApi.env.NVCT_ESS_WORKER_BASE_URL' "$public_url" \
  "override: NVCT_ESS_WORKER_BASE_URL stays on essServiceURL"

# ---------------------------------------------------------------------------
# 3. Neither set: no ESS endpoint at all -> no base URL and no worker alias are
#    emitted, matching the pre-change empty case.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

empty_values="$work_dir/empty-values.yaml"
render_values "$empty_values"
assert_value "$empty_values" '.api.env.NVCF_ESS_BASE_URL' null \
  "empty: NVCF_ESS_BASE_URL not emitted"
assert_value "$empty_values" '.api.env.NVCF_ESS_WORKER_BASE_URL' null \
  "empty: NVCF_ESS_WORKER_BASE_URL not emitted"
assert_value "$empty_values" '.nvctApi.env.NVCT_ESS_BASE_URL' null \
  "empty: NVCT_ESS_BASE_URL not emitted"
assert_value "$empty_values" '.nvctApi.env.NVCT_ESS_WORKER_BASE_URL' null \
  "empty: NVCT_ESS_WORKER_BASE_URL not emitted"

# ---------------------------------------------------------------------------
# 4. Override without a worker endpoint: essBaseURL set, essServiceURL empty ->
#    the in-cluster API base URL is emitted with no worker alias.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
api:
  essBaseURL: $incluster_url
nvctApi:
  essBaseURL: $incluster_url
EOF

incluster_only_values="$work_dir/incluster-only-values.yaml"
render_values "$incluster_only_values"
assert_value "$incluster_only_values" '.api.env.NVCF_ESS_BASE_URL' "$incluster_url" \
  "incluster-only: NVCF_ESS_BASE_URL uses api.essBaseURL"
assert_value "$incluster_only_values" '.api.env.NVCF_ESS_WORKER_BASE_URL' null \
  "incluster-only: NVCF_ESS_WORKER_BASE_URL not emitted"
assert_value "$incluster_only_values" '.nvctApi.env.NVCT_ESS_BASE_URL' "$incluster_url" \
  "incluster-only: NVCT_ESS_BASE_URL uses nvctApi.essBaseURL"
assert_value "$incluster_only_values" '.nvctApi.env.NVCT_ESS_WORKER_BASE_URL' null \
  "incluster-only: NVCT_ESS_WORKER_BASE_URL not emitted"

echo "ess-base-url-wiring: OK"
