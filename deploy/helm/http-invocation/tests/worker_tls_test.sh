#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "worker_tls_test.sh: $*" >&2
  exit 1
}

render() {
  local output="$1"
  shift
  helm template invocation-service "$chart_root" --namespace nvcf \
    --set-string invocation.image.registry=registry.example.test \
    --set-string invocation.image.repository=nvcf-invocation-service \
    "$@" >"$output"
}

env_value() {
  local file="$1" key="$2"
  ENV_KEY="$key" yq ea -r \
    'select(.kind == "ConfigMap" and (.metadata.name | test("-env$"))) | .data[strenv(ENV_KEY)] // "absent"' \
    "$file"
}

checksum() {
  yq ea -r 'select(.kind == "Deployment") | .spec.template.metadata.annotations."checksum/config-env"' "$1"
}

deployment() {
  yq ea -r "select(.kind == \"Deployment\") | $2" "$1"
}

count_kind() {
  KIND="$2" yq ea -r '[select(.kind == strenv(KIND))] | length' "$1"
}

enable_cert=(
  --set invocation.workerTls.enabled=true
  --set invocation.workerTls.tls.certificate.enabled=true
  --set-string invocation.workerTls.tls.certificate.issuerRef.name=nvcf-worker-callback
)

render "$work_dir/default.yaml"
[[ "$(env_value "$work_dir/default.yaml" WORKER_TLS__ENABLED)" == absent ]] || fail "worker TLS must be off by default"
[[ "$(env_value "$work_dir/default.yaml" WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS)" == http://invocation.nvcf.svc.cluster.local:8080 ]] || fail "default callback address changed"
[[ "$(count_kind "$work_dir/default.yaml" Certificate)" == 0 ]] || fail "Certificate rendered while disabled"
[[ "$(deployment "$work_dir/default.yaml" '[.spec.template.spec.volumes[]? | select(.name == "worker-tls")] | length')" == 0 ]] || fail "worker-tls volume rendered while disabled"
[[ "$(yq ea -r 'select(.kind == "Service") | [.spec.ports[] | select(.name == "worker-tls")] | length' "$work_dir/default.yaml")" == 0 ]] || fail "worker-tls Service port rendered while disabled"

render "$work_dir/cert.yaml" "${enable_cert[@]}"
secret="invocation-service-helm-nvcf-invocation-service-worker-tls"
[[ "$(env_value "$work_dir/cert.yaml" WORKER_TLS__ENABLED)" == true ]] || fail "WORKER_TLS__ENABLED not rendered"
[[ "$(env_value "$work_dir/cert.yaml" WORKER_TLS__PORT)" == 8443 ]] || fail "WORKER_TLS__PORT not rendered"
[[ "$(env_value "$work_dir/cert.yaml" WORKER_TLS__CERT_PATH)" == /etc/invocation-service/worker-tls/tls.crt ]] || fail "cert path not rendered"
[[ "$(env_value "$work_dir/cert.yaml" WORKER_TLS__KEY_PATH)" == /etc/invocation-service/worker-tls/tls.key ]] || fail "key path not rendered"
[[ "$(env_value "$work_dir/cert.yaml" WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS)" == https://invocation.nvcf.svc.cluster.local:8443 ]] || fail "callback address must advertise https on the TLS port"
[[ "$(yq ea -r 'select(.kind == "Certificate") | .spec.secretName' "$work_dir/cert.yaml")" == "$secret" ]] || fail "Certificate secretName mismatch"
[[ "$(yq ea -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "$work_dir/cert.yaml")" == '*.invocation.nvcf.svc.cluster.local' ]] || fail "default dnsNames must cover per-pod names"
[[ "$(yq ea -r 'select(.kind == "Certificate") | .spec.usages | join(",")' "$work_dir/cert.yaml")" == "server auth" ]] || fail "Certificate must be server auth only"
[[ "$(yq ea -r 'select(.kind == "Certificate") | .spec.issuerRef.name' "$work_dir/cert.yaml")" == nvcf-worker-callback ]] || fail "issuerRef not rendered"
[[ "$(deployment "$work_dir/cert.yaml" '.spec.template.spec.volumes[] | select(.name == "worker-tls") | .secret.secretName')" == "$secret" ]] || fail "worker-tls volume must mount the Certificate secret"
[[ "$(deployment "$work_dir/cert.yaml" '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "worker-tls") | .mountPath + ":" + (.readOnly | tostring)')" == /etc/invocation-service/worker-tls:true ]] || fail "worker-tls mount must be read-only at the configured path"
[[ "$(deployment "$work_dir/cert.yaml" '.spec.template.spec.containers[0].ports[] | select(.name == "worker-tls") | .containerPort')" == 8443 ]] || fail "worker-tls containerPort not rendered"
[[ "$(yq ea -r 'select(.kind == "Service") | .spec.ports[] | select(.name == "worker-tls") | .port + "/" + .targetPort' "$work_dir/cert.yaml")" == 8443/worker-tls ]] || fail "worker-tls Service port not rendered"
[[ "$(checksum "$work_dir/default.yaml")" != "$(checksum "$work_dir/cert.yaml")" ]] || fail "enabling worker TLS must restart pods"

render "$work_dir/existing.yaml" \
  --set invocation.workerTls.enabled=true \
  --set-string invocation.workerTls.tls.secretName=bring-your-own \
  --set invocation.workerTls.port=9443 \
  --set invocation.workerTls.advertise=false
[[ "$(count_kind "$work_dir/existing.yaml" Certificate)" == 0 ]] || fail "Certificate rendered for an existing secret"
[[ "$(deployment "$work_dir/existing.yaml" '.spec.template.spec.volumes[] | select(.name == "worker-tls") | .secret.secretName')" == bring-your-own ]] || fail "existing secret not mounted"
[[ "$(env_value "$work_dir/existing.yaml" WORKER_TLS__PORT)" == 9443 ]] || fail "custom port not rendered"
[[ "$(env_value "$work_dir/existing.yaml" WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS)" == http://invocation.nvcf.svc.cluster.local:8080 ]] || fail "advertise=false must keep the http callback address"

render "$work_dir/override.yaml" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS=https://callbacks.example:443 \
  --set-string 'invocation.workerTls.tls.certificate.dnsNames[0]=*.callbacks.example'
[[ "$(env_value "$work_dir/override.yaml" WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS)" == https://callbacks.example:443 ]] || fail "explicit env callback address must win"
count="$(awk '$1 == "WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS:" { count++ } END { print count+0 }' "$work_dir/override.yaml")"
[[ "$count" == 1 ]] || fail "duplicate SERVICE_ADDRESS key"
[[ "$(yq ea -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "$work_dir/override.yaml")" == '*.callbacks.example' ]] || fail "custom dnsNames not rendered"

dns_names() {
  yq ea -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "$1"
}

render "$work_dir/service-address.yaml" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS=https://callbacks.example.test:8443
[[ "$(dns_names "$work_dir/service-address.yaml")" == '*.callbacks.example.test' ]] || fail "default dnsNames must follow SERVICE_ADDRESS"

render "$work_dir/self-address.yaml" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS=https://Callbacks.Example.test:8443 \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS=https://ignored.example.test
[[ "$(dns_names "$work_dir/self-address.yaml")" == callbacks.example.test ]] || fail "default dnsNames must follow SELF_ADDRESS without a pod prefix"

render "$work_dir/self-wildcard.yaml" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS=https://callbacks.example.test \
  --set-string 'invocation.workerTls.tls.certificate.dnsNames[0]=*.example.test'
[[ "$(dns_names "$work_dir/self-wildcard.yaml")" == '*.example.test' ]] || fail "a parent wildcard must cover SELF_ADDRESS"

render "$work_dir/self-http.yaml" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS=http://10.0.0.5:8080
[[ "$(dns_names "$work_dir/self-http.yaml")" == '*.invocation.nvcf.svc.cluster.local' ]] || fail "an http callback must keep the default dnsNames"

render "$work_dir/no-env.yaml" "${enable_cert[@]}" --set invocation.env=null
[[ "$(env_value "$work_dir/no-env.yaml" WORKER_TLS__ENABLED)" == true ]] || fail "worker TLS must work without the default env map"
[[ "$(deployment "$work_dir/no-env.yaml" '.spec.template.spec.containers[0].envFrom[0].configMapRef.name')" == *-env ]] || fail "worker TLS ConfigMap must reach the pod"

expect_failure() {
  local want="$1"
  shift
  if render "$work_dir/invalid.yaml" "$@" 2>"$work_dir/error.txt"; then
    fail "invalid values were accepted: $*"
  fi
  grep -q -- "$want" "$work_dir/error.txt" || fail "expected '$want' for $*; got: $(cat "$work_dir/error.txt")"
}

expect_failure "secretName is required" --set invocation.workerTls.enabled=true
expect_failure "issuerRef.name is required" --set invocation.workerTls.enabled=true --set invocation.workerTls.tls.certificate.enabled=true
expect_failure "must be an integer between 1 and 65535" "${enable_cert[@]}" --set invocation.workerTls.port=0
expect_failure "must be an integer between 1 and 65535" "${enable_cert[@]}" --set invocation.workerTls.port=70000
expect_failure "must be an integer between 1 and 65535" "${enable_cert[@]}" --set-string invocation.workerTls.port=https
expect_failure "already used by container port" "${enable_cert[@]}" --set invocation.workerTls.port=8080
expect_failure "already used by invocation.metrics.port" "${enable_cert[@]}" --set invocation.metrics.enabled=true --set invocation.workerTls.port=41337
expect_failure "is set by invocation.workerTls" "${enable_cert[@]}" --set-string invocation.env.WORKER_TLS__PORT=1
expect_failure "must be a boolean" --set-string invocation.workerTls.enabled=yes
expect_failure "must be a boolean" --set-string invocation.workerTls.advertise=yes
expect_failure "dnsNames must cover \*.callbacks.example.test" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS=https://callbacks.example.test:8443 \
  --set-string 'invocation.workerTls.tls.certificate.dnsNames[0]=callbacks.example.test'
expect_failure "dnsNames must cover callbacks.example.test" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS=https://callbacks.example.test \
  --set-string 'invocation.workerTls.tls.certificate.dnsNames[0]=*.invocation.nvcf.svc.cluster.local'
expect_failure 'dnsNames must cover \*.invocation.nvcf.svc.cluster.local' "${enable_cert[@]}" \
  --set-string 'invocation.workerTls.tls.certificate.dnsNames[0]=*.callbacks.example'
expect_failure "not an IP address" "${enable_cert[@]}" \
  --set-string invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS=https://10.0.0.5:8443

echo "worker_tls_test.sh: default-off, cert-manager, existing secret, overrides, and validation passed"
