#!/usr/bin/env bash
# Test that NATS server-side TLS threads from environments/<env>.yaml through
# global.yaml.gotmpl into the nats release values. The nats release's chart
# values are only global.yaml.gotmpl + secrets/<env>-secrets.yaml, so server
# TLS has to be re-emitted from the nats: block. It lives in a single merged
# config: block (a second config: key under nats: would collapse), so this
# guards two things: (a) nats.config.nats.tls and nats.config.merge.allow_non_tls
# thread through when set, and (b) adding allow_non_tls next to server_tags does
# not clobber server_tags. Unset renders no tls / no allow_non_tls (default off).
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="nats-tls-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-tls-wiring: $*" >&2
  exit 1
}

# Copy the whole stacks directory: the self-managed helmfile.d reaches a sibling
# stack through a relative path, so self-managed alone is not a working stack.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
mkdir -p "$(dirname "$secrets_file")"
printf '{}\n' >"$secrets_file"

# Each stack the copy reaches resolves ../environments/$HELMFILE_ENV.yaml
# against its own directory, so every one needs a file under this env name.
for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

render_nats_values() {
  local output_file="$1"
  local render_log="$work_dir/nats-write-values.log"
  if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" \
      --selector name=nats \
      write-values \
      --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "nats: helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "nats: helmfile wrote no values to $output_file"
}

write_env() {
  cat >"$environment_file"
}

# ---------------------------------------------------------------------------
# 1. Default: no TLS set. server_tags stays, no tls, no allow_non_tls.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_values="$work_dir/nats-default-values.yaml"
render_nats_values "$default_values"
[[ "$(yq -r '.nats.config.nats.tls' "$default_values")" == null ]] ||
  fail "default: nats.config.nats.tls must not be emitted when unset"
[[ "$(yq -r '.nats.config.merge.allow_non_tls' "$default_values")" == null ]] ||
  fail "default: nats.config.merge.allow_non_tls must not be emitted when unset"
[[ "$(yq -r '.nats.config.merge.server_tags | length' "$default_values")" == 2 ]] ||
  fail "default: nats.config.merge.server_tags must be preserved"

# ---------------------------------------------------------------------------
# 2. Override: server TLS + allow_non_tls thread through, and adding
#    allow_non_tls next to server_tags does not drop server_tags.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nats:
  config:
    nats:
      tls:
        enabled: true
        secretName: nats-server-tls
    merge:
      allow_non_tls: true
EOF

override_values="$work_dir/nats-tls-values.yaml"
render_nats_values "$override_values"
yq -e '.nats.config.nats.tls.enabled == true' "$override_values" >/dev/null ||
  fail "override: nats.config.nats.tls.enabled did not reach chart values"
[[ "$(yq -r '.nats.config.nats.tls.secretName' "$override_values")" == nats-server-tls ]] ||
  fail "override: nats.config.nats.tls.secretName did not reach chart values"
yq -e '.nats.config.merge.allow_non_tls == true' "$override_values" >/dev/null ||
  fail "override: nats.config.merge.allow_non_tls did not reach chart values"
[[ "$(yq -r '.nats.config.merge.server_tags | length' "$override_values")" == 2 ]] ||
  fail "override: server_tags must survive alongside allow_non_tls"

# ---------------------------------------------------------------------------
# 3. allow_non_tls: false is honored (explicit bool, not just truthy).
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nats:
  config:
    merge:
      allow_non_tls: false
EOF

false_values="$work_dir/nats-allow-false-values.yaml"
render_nats_values "$false_values"
yq -e '.nats.config.merge.allow_non_tls == false' "$false_values" >/dev/null ||
  fail "explicit false: nats.config.merge.allow_non_tls: false did not reach chart values"

echo "nats-tls-wiring: all checks passed"
