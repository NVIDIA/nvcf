#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Regression: two named control planes must render plane-owned releases into
# disjoint namespaces, while omitting the ID must preserve the legacy layout.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
gateway_chart_dir="$(cd "$stack_dir/../../helm/gateway-routes/chart" && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "control-plane-isolation: $*" >&2
  exit 1
}

mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
printf '{}\n' >"$test_stack_dir/secrets/base-secrets.yaml"

render_releases() {
  local control_plane_id="$1"
  local output_file="$2"
  local state_file
  local -a plane_args=()
  local state_output state_log
  local gateway_prefix=""

  if test -n "$control_plane_id"; then
    gateway_prefix="$control_plane_id-"
    plane_args=(
      --state-values-set-string "global.controlPlane.id=$control_plane_id"
      --state-values-set-string global.controlPlane.sharedInfrastructure=external
      --state-values-set certManager.enabled=false
      --state-values-set-string "global.domain=$control_plane_id.example.test"
      --state-values-set-string "ingress.gatewayApi.gateways.nats.name=$control_plane_id-nats-gateway"
    )
  fi

  : >"$output_file"
  for state_file in 00-observability-infrastructure 01-dependencies 02-core 03-observability; do
    state_output="$work_dir/$control_plane_id-$state_file.json"
    state_log="$work_dir/$control_plane_id-$state_file.log"
    if HELMFILE_ENV=base \
      HELMFILE_CACHE_HOME="$work_dir/helmfile-cache-$control_plane_id" \
      helmfile \
        --file "$test_stack_dir/helmfile.d/$state_file.yaml.gotmpl" \
        --environment default \
        --state-values-set ingress.gatewayApi.controllerNamespace=gateway-system \
        --state-values-set "ingress.gatewayApi.gateways.shared.name=${gateway_prefix}shared-gateway" \
        --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway-system \
        --state-values-set "ingress.gatewayApi.gateways.grpc.name=${gateway_prefix}grpc-gateway" \
        --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway-system \
        "${plane_args[@]}" \
        list --skip-charts --output json >"$state_output" 2>"$state_log"; then
      jq -c '.[] | select(.enabled != false and .installed != false)' "$state_output" >>"$output_file"
    elif ! grep -Fq 'no releases found' "$state_log"; then
      cat "$state_log" >&2
      fail "$state_file failed to render for ${control_plane_id:-legacy}"
    fi
  done
}

assert_release_namespace() {
  local releases_file="$1" release_name="$2" want_namespace="$3"
  local got
  got="$(jq -sr --arg name "$release_name" \
    '[.[] | select(.name == $name) | .namespace] | unique | if length == 1 then .[0] else "" end' \
    "$releases_file")"
  test "$got" = "$want_namespace" ||
    fail "$release_name: expected namespace $want_namespace, got ${got:-<none or multiple>}"
}

render_values() {
  local plane_id="$1" state_file="$2" release_name="$3" output_file="$4"
  shift 4
  HELMFILE_ENV=base \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache-values-$plane_id" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/$state_file.yaml.gotmpl" \
      --environment default \
      --state-values-set-string "global.controlPlane.id=$plane_id" \
      --state-values-set-string global.controlPlane.sharedInfrastructure=external \
      --state-values-set certManager.enabled=false \
      --state-values-set-string "global.domain=$plane_id.example.test" \
      --state-values-set ingress.gatewayApi.controllerNamespace=gateway-system \
      --state-values-set-string "ingress.gatewayApi.gateways.shared.name=$plane_id-shared-gateway" \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway-system \
      --state-values-set-string "ingress.gatewayApi.gateways.grpc.name=$plane_id-grpc-gateway" \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway-system \
      --state-values-set-string "ingress.gatewayApi.gateways.nats.name=$plane_id-nats-gateway" \
      "$@" \
      --selector "name=$release_name" \
      write-values --output-file-template "$output_file" >/dev/null
}

assert_value() {
  local values_file="$1" expression="$2" want="$3" label="$4"
  local got
  got="$(yq -r "$expression" "$values_file")"
  test "$got" = "$want" ||
    fail "$label: expected $want, got ${got:-<empty>}"
}

find_legacy_service_references() {
  ruby -ryaml -e '
    legacy = /\.(?:nvcf|api-keys|sis|ess|nats-system|vault-system|cassandra-system)\.svc(?:\.cluster\.local)?/
    walk = lambda do |value, path|
      case value
      when Hash
        return if value["name"] == "OPENBAO_JWT_AUDIENCE"
        value.each do |key, child|
          next if key.to_s == "audience"
          walk.call(child, path + [key])
        end
      when Array
        value.each_with_index { |child, index| walk.call(child, path + [index]) }
      when String
        puts "#{path.join(".")}:#{value}" if value.match?(legacy)
      end
    end
    ARGV.each do |file|
      YAML.load_stream(File.read(file)).compact.each { |doc| walk.call(doc, []) }
    end
  ' "$@"
}

render_releases alpha "$work_dir/alpha.jsonl"
render_releases beta "$work_dir/beta.jsonl"
render_releases '' "$work_dir/legacy.jsonl"

# These releases own the control plane's data or services. A missing prefix on
# any one of them allows one plane to read, mutate, or delete the other's state.
for spec in \
  'nats:nats-system' \
  'openbao-server:vault-system' \
  'cassandra:cassandra-system' \
  'api-keys:api-keys' \
  'sis:sis' \
  'api:nvcf' \
  'nvct-api:nvcf' \
  'invocation-service:nvcf' \
  'grpc-proxy:nvcf' \
  'ess-api:ess' \
  'notary-service:nvcf' \
  'admin-issuer-proxy:api-keys' \
  'reval:nvcf' \
  'nats-auth-callout-service:nats-system' \
  'state-metrics:nvcf' \
  'function-autoscaler:nvcf'; do
  release_name="${spec%%:*}"
  legacy_namespace="${spec#*:}"
  assert_release_namespace "$work_dir/alpha.jsonl" "$release_name" "alpha-$legacy_namespace"
  assert_release_namespace "$work_dir/beta.jsonl" "$release_name" "beta-$legacy_namespace"
  assert_release_namespace "$work_dir/legacy.jsonl" "$release_name" "$legacy_namespace"
done

assert_release_namespace "$work_dir/legacy.jsonl" ingress gateway-system
assert_release_namespace "$work_dir/alpha.jsonl" alpha-ingress alpha-ingress
assert_release_namespace "$work_dir/beta.jsonl" beta-ingress beta-ingress

alpha_owned="$(jq -r 'select(.namespace | startswith("alpha-")) | .namespace + "/" + .name' \
  "$work_dir/alpha.jsonl" | sort -u)"
beta_owned="$(jq -r 'select(.namespace | startswith("beta-")) | .namespace + "/" + .name' \
  "$work_dir/beta.jsonl" | sort -u)"
test -n "$alpha_owned" || fail 'alpha rendered no plane-owned releases'
test -n "$beta_owned" || fail 'beta rendered no plane-owned releases'
if comm -12 <(printf '%s\n' "$alpha_owned") <(printf '%s\n' "$beta_owned") | grep -q .; then
  fail 'named control planes rendered overlapping plane-owned release identities'
fi

for plane_id in alpha beta; do
  if jq -e --arg prefix "$plane_id-" \
      'select((.namespace | startswith($prefix)) | not)' \
      "$work_dir/$plane_id.jsonl" >/dev/null; then
    jq -c --arg prefix "$plane_id-" \
      'select((.namespace | startswith($prefix)) | not)' \
      "$work_dir/$plane_id.jsonl" >&2
    fail "$plane_id rendered a release outside its owned namespaces"
  fi
done

# The release namespace is only half of the isolation boundary. Values passed
# into the charts must point every in-cluster client, route, and bootstrap job
# back to the same plane.
for plane_id in alpha beta; do
  core_values="$work_dir/$plane_id-core-values.yaml"
  dependency_values="$work_dir/$plane_id-dependency-values.yaml"
  render_values "$plane_id" 02-core nvct-api "$core_values"
  render_values "$plane_id" 01-dependencies openbao-server "$dependency_values"

  assert_value "$dependency_values" '.openbao.fullnameOverride' \
    "$plane_id-openbao-server" "$plane_id OpenBao identity"
  assert_value "$dependency_values" '.openbao.controlPlane.id' \
    "$plane_id" "$plane_id OpenBao owner"
  assert_value "$core_values" '.api.accountBootstrap.openbaoServiceAddress' \
    "$plane_id-openbao-server.$plane_id-vault-system.svc.cluster.local:8200" \
    "$plane_id API bootstrap"
  assert_value "$core_values" '.api.env.NVCF_NATS_URL' \
    "nats://nats.$plane_id-nats-system.svc.cluster.local:4222" \
    "$plane_id API NATS"
  assert_value "$core_values" '.invocation.env.NVCF_API_ADDRESS' \
    "http://api.$plane_id-nvcf.svc.cluster.local:9090" \
    "$plane_id invocation API"
  assert_value "$core_values" '.adminIssuerProxy.fullnameOverride' \
    "$plane_id-admin-token-issuer-proxy" "$plane_id admin issuer identity"
  assert_value "$core_values" '.adminIssuerProxy.config.vaultAddr' \
    "http://$plane_id-openbao-server.$plane_id-vault-system.svc.cluster.local:8200" \
    "$plane_id admin issuer OpenBao"
  assert_value "$core_values" '.nvcfGatewayRoutes.routeNamespace' \
    "$plane_id-ingress" "$plane_id route namespace"
  assert_value "$core_values" '.nvcfGatewayRoutes.routes.nvcfApi.backend.namespace' \
    "$plane_id-nvcf" "$plane_id API route backend"
  assert_value "$core_values" '.nvcfGatewayRoutes.routes.apiKeys.backend.namespace' \
    "$plane_id-api-keys" "$plane_id API keys route backend"
  legacy_references="$(find_legacy_service_references "$core_values" "$dependency_values")"
  if test -n "$legacy_references"; then
    printf '%s\n' "$legacy_references" >&2
    fail "$plane_id values contain legacy cross-plane service references"
  fi
done

# A stack-managed ClusterIssuer is cluster-scoped and retained by Helm. Prefix
# matching alone is ambiguous: alpha-beta's canonical issuer also begins with
# alpha's prefix. Require alpha's exact canonical name at both render layers.
managed_issuer_error='managed ClusterIssuer name "alpha-beta-nvcf-openbao-pki" must equal canonical name "alpha-nvcf-openbao-pki"'
named_managed_args=(
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true
  --state-values-set-string addons.llm.pki.issuerName=alpha-beta-nvcf-openbao-pki
)
if HELMFILE_ENV=base helmfile \
    --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
    --environment default \
    --state-values-set-string global.controlPlane.id=alpha \
    --state-values-set-string global.controlPlane.sharedInfrastructure=external \
    --state-values-set certManager.enabled=false \
    --state-values-set-string global.domain=alpha.example.test \
    "${named_managed_args[@]}" \
    list --skip-charts >"$work_dir/unprefixed-managed-dependency.log" 2>&1; then
  fail 'dependency state accepted another plane canonical name through a shared prefix'
fi
grep -Fq "$managed_issuer_error" "$work_dir/unprefixed-managed-dependency.log" ||
  fail 'dependency state did not return the named managed ClusterIssuer ownership error'

if render_values alpha 02-core nvct-api \
    "$work_dir/unprefixed-managed-global.yaml" \
    "${named_managed_args[@]}" \
    >"$work_dir/unprefixed-managed-global.log" 2>&1; then
  fail 'global values accepted another plane canonical name through a shared prefix'
fi
grep -Fq "$managed_issuer_error" "$work_dir/unprefixed-managed-global.log" ||
  fail 'global values did not return the named managed ClusterIssuer ownership error'

# The exact canonical issuer is owned by this plane. Both the dependency release
# and chart values must carry that stable name and owner identity.
canonical_managed_args=(
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true
  --state-values-set-string addons.llm.pki.issuerName=alpha-nvcf-openbao-pki
)
HELMFILE_ENV=base helmfile \
  --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  --environment default \
  --state-values-set-string global.controlPlane.id=alpha \
  --state-values-set-string global.controlPlane.sharedInfrastructure=external \
  --state-values-set certManager.enabled=false \
  --state-values-set-string global.domain=alpha.example.test \
  "${canonical_managed_args[@]}" \
  list --skip-charts --output json >"$work_dir/canonical-managed-dependency.json"
jq -e '.[] | select(.name == "alpha-nvcf-pki" and .enabled != false and .installed != false)' \
  "$work_dir/canonical-managed-dependency.json" >/dev/null ||
  fail 'dependency state omitted the canonical managed ClusterIssuer'
render_values alpha 01-dependencies alpha-nvcf-pki \
  "$work_dir/canonical-managed-dependency.yaml" \
  "${canonical_managed_args[@]}"
assert_value "$work_dir/canonical-managed-dependency.yaml" \
  '.clusterIssuer.name' alpha-nvcf-openbao-pki \
  'named canonical managed ClusterIssuer name'
assert_value "$work_dir/canonical-managed-dependency.yaml" \
  '.clusterIssuer.controlPlaneID' alpha \
  'named canonical managed ClusterIssuer owner'
render_values alpha 02-core nvct-api "$work_dir/canonical-managed-global.yaml" \
  "${canonical_managed_args[@]}"
assert_value "$work_dir/canonical-managed-global.yaml" \
  '.llmRequestRouter.certificate.issuerRef.name' alpha-nvcf-openbao-pki \
  'named canonical managed Certificate issuer'

# An explicitly external issuer is not owned or deleted by the stack and may
# therefore keep an unprefixed name in named mode.
external_issuer_args=(
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set addons.llm.pki.clusterIssuer.enabled=false
  --state-values-set-string addons.llm.pki.issuerName=external-shared-pki
)
HELMFILE_ENV=base helmfile \
  --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  --environment default \
  --state-values-set-string global.controlPlane.id=alpha \
  --state-values-set-string global.controlPlane.sharedInfrastructure=external \
  --state-values-set certManager.enabled=false \
  --state-values-set-string global.domain=alpha.example.test \
  "${external_issuer_args[@]}" \
  list --skip-charts --output json >"$work_dir/external-issuer-dependency.json"
if jq -e '.[] | select(.name == "alpha-nvcf-pki" and .enabled != false and .installed != false)' \
    "$work_dir/external-issuer-dependency.json" >/dev/null; then
  fail 'dependency state managed an explicitly external ClusterIssuer'
fi
render_values alpha 02-core nvct-api "$work_dir/external-issuer-global.yaml" \
  "${external_issuer_args[@]}"
assert_value "$work_dir/external-issuer-global.yaml" \
  '.llmRequestRouter.certificate.issuerRef.name' external-shared-pki \
  'named external ClusterIssuer'

# The currently pinned UI chart has cluster-wide RBAC and therefore cannot be
# presented as isolated merely by changing its namespace.
if HELMFILE_ENV=base helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --state-values-set-string global.controlPlane.id=alpha \
    --state-values-set-string global.controlPlane.sharedInfrastructure=external \
    --state-values-set certManager.enabled=false \
    --state-values-set-string global.domain=alpha.example.test \
    --state-values-set-string ingress.gatewayApi.gateways.shared.name=alpha-shared-gateway \
    --state-values-set-string ingress.gatewayApi.gateways.grpc.name=alpha-grpc-gateway \
    --state-values-set-string ingress.gatewayApi.gateways.nats.name=alpha-nats-gateway \
    --state-values-set addons.nvcfUi.enabled=true \
    list --skip-charts >"$work_dir/named-ui.log" 2>&1; then
  fail 'named control plane accepted the non-isolated NVCF UI addon'
fi
grep -Fq 'addons.nvcfUi.enabled must be false for a named control plane' \
  "$work_dir/named-ui.log" ||
  fail 'named NVCF UI rejection did not return the expected error'

# Worker-facing TLS material is created in the shared Gateway namespace, so
# its name, SAN, issuer, and Gateway references must still be plane-specific.
llm_manifest="$work_dir/alpha-llm-gateway.yaml"
llm_hostname=alpha-llm-grpc.alpha.example.test
HELMFILE_ENV=base helmfile \
  --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
  --environment default \
  --state-values-set-string global.controlPlane.id=alpha \
  --state-values-set-string global.controlPlane.sharedInfrastructure=external \
  --state-values-set certManager.enabled=false \
  --state-values-set-string global.domain=alpha.example.test \
  --state-values-set-string ingress.gatewayApi.gateways.shared.name=alpha-shared-gateway \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway-system \
  --state-values-set-string ingress.gatewayApi.gateways.grpc.name=alpha-grpc-gateway \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway-system \
  --state-values-set-string ingress.gatewayApi.gateways.nats.name=alpha-nats-gateway \
  --state-values-set addons.llm.enabled=true \
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true \
  --state-values-set addons.llm.requestRouter.grpcTls.enabled=true \
  --state-values-set-string "addons.llm.requestRouter.grpcTls.dnsNames[0]=$llm_hostname" \
  --state-values-set-string "global.workerEndpoints.llmRequestRouterAddress=https://$llm_hostname:443" \
  --state-values-set-string "addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=https://$llm_hostname:443" \
  --state-values-set-string "addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=alpha-llm-quic.alpha.example.test:443" \
  --selector name=alpha-ingress \
  --chart "$gateway_chart_dir" \
  --skip-deps template >"$llm_manifest"

certificate="$(yq -o=json -I=0 \
  'select(.kind == "Certificate" and .metadata.name == "alpha-llm-request-router-grpc-tls")' \
  "$llm_manifest")"
test -n "$certificate" || fail 'named LLM render omitted its gRPC Certificate'
test "$(jq -r '.metadata.namespace' <<<"$certificate")" = gateway-system ||
  fail 'named LLM Certificate was not rendered in the shared Gateway namespace'
test "$(jq -r '.spec.secretName' <<<"$certificate")" = alpha-llm-request-router-grpc-tls ||
  fail 'named LLM Certificate secret is not plane-prefixed'
test "$(jq -r '.spec.dnsNames[0]' <<<"$certificate")" = "$llm_hostname" ||
  fail 'named LLM Certificate SAN does not match its endpoint'
test "$(jq -r '.spec.issuerRef.name' <<<"$certificate")" = alpha-nvcf-openbao-pki ||
  fail 'named LLM Certificate does not use the plane-scoped issuer'
grep -Fq 'name: alpha-llm-grpc-gw' "$llm_manifest" ||
  fail 'named LLM gRPC route did not reference a plane-prefixed Gateway'
grep -Fq 'name: alpha-llm-quic-gw' "$llm_manifest" ||
  fail 'named LLM QUIC route did not reference a plane-prefixed Gateway'

echo 'control-plane-isolation: all checks passed'
