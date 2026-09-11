#!/usr/bin/env bash
# Verify the self-managed stack passes the runtime values that the
# nats-auth-callout-service chart reads directly.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nats-auth-callout-service-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-auth-callout-service-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
nats:
  authCallout:
    secretName: custom-auth-callout-nkeys
EOF

values_file="$work_dir/nats-auth-callout-service-values.yaml"
manifest_file="$work_dir/nats-auth-callout-service-manifest.yaml"

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
    --selector name=nats-auth-callout-service \
    write-values \
    --output-file-template "$values_file" >/dev/null

helm template nats-auth-callout-service "$repo_dir/deploy/helm/nats-auth-callout" \
  --namespace nats-system \
  --values "$values_file" >"$manifest_file"

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq ea -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

assert_yaml_value "$values_file" '.image.repository' \
  nvcr.io/nvidia/nvcf/nvcf-nats-auth-callout-service "image repository"
assert_yaml_value "$values_file" '.serviceConfig.service.nats_url' \
  nats://nats.nats-system.svc.cluster.local:4222 "NATS service URL"

assert_yaml_value "$manifest_file" \
  'select(.kind == "ConfigMap" and .metadata.name == "nats-auth-callout-service-helm-nvcf-nats-auth-callout-service-config") | .data."config.yaml" | from_yaml | .service.nats_url' \
  nats://nats.nats-system.svc.cluster.local:4222 "rendered NATS service URL"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED") | .valueFrom.secretKeyRef.name' \
  custom-auth-callout-nkeys "NKey seed Secret name"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED") | .valueFrom.secretKeyRef.key' \
  nkey_seed "NKey seed Secret key"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE") | .valueFrom.secretKeyRef.name' \
  custom-auth-callout-nkeys "NKey signature Secret name"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE") | .valueFrom.secretKeyRef.key' \
  nkey_signature "NKey signature Secret key"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_PLUGIN__CONFIGS_NKEY_PLUGIN__TYPE") | .value' \
  nkey "NKey plugin type"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_PLUGIN__CONFIGS_NKEY_CONFIG_NKEY__MAPPINGS_0_NKEY") | .valueFrom.secretKeyRef.name' \
  nats-nkeys "shared worker public NKey Secret name"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_PLUGIN__CONFIGS_NKEY_CONFIG_NKEY__MAPPINGS_0_NKEY") | .valueFrom.secretKeyRef.key' \
  user.pub "shared worker public NKey Secret key"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_PLUGIN__CONFIGS_NKEY_CONFIG_NKEY__MAPPINGS_0_ACCOUNT") | .value' \
  APP "shared worker NKey account mapping"
assert_yaml_value "$manifest_file" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_ACCOUNT__CONFIGS_APP_ENABLED__PLUGINS_0_ID") | .value' \
  nkey "APP enabled NKey plugin"

echo "nats-auth-callout-service-wiring: OK"
