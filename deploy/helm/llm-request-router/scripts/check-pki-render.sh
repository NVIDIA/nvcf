#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

tmp_dir="$(mktemp -d)"
manifest="${tmp_dir}/enabled.yaml"
defaults_manifest="${tmp_dir}/defaults.yaml"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render_certificate_case() {
  output="$1"
  advertised_hostname_template="$2"
  dns_name="$3"

  helm template llm-request-router ./llm-request-router \
    --namespace nvcf \
    --values ./llm-request-router/values.yaml \
    --set llmRequestRouter.image.repository=stargate \
    --set llmRequestRouter.workload.kind=StatefulSet \
    --set llmRequestRouter.replicaCount=3 \
    --set llmRequestRouter.certificate.enabled=true \
    --set llmRequestRouter.certificate.secretName=stargate-quic-tls \
    --set llmRequestRouter.certificate.issuerRef.name=nvcf-openbao-pki \
    --set-string "llmRequestRouter.kubernetes.advertisedHostnameTemplate=${advertised_hostname_template}" \
    --set-string "llmRequestRouter.certificate.dnsNames[0]=${dns_name}" \
    > "${output}"
}

# Pass 1: defaults. PKI, certificate, and TLS are all off. Assert the chart does not
# emit any of the optional PKI resources so a regression that accidentally
# turns them on (or fails to gate them properly) is caught.
helm template llm-request-router ./llm-request-router \
  --namespace nvcf \
  --values ./llm-request-router/values.yaml \
  --set llmRequestRouter.image.repository=stargate \
  > "${defaults_manifest}"

# No Certificate resource should render at the chart's defaults.
default_cert="$(yq -rN 'select(.kind == "Certificate") | .metadata.name' "${defaults_manifest}" | head -n1)"
[ -z "${default_cert}" ] || { echo "FAIL: Certificate rendered with default values: ${default_cert}" >&2; exit 1; }

# No PKI provisioning Job should render at the chart's defaults.
default_job="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.name' "${defaults_manifest}" | head -n1)"
[ -z "${default_job}" ] || { echo "FAIL: addons-llm-migrations Job rendered with default values" >&2; exit 1; }

# The selected request-router workload should still render but with no
# stargate-tls volume or volumeMount.
default_workload="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .metadata.name' "${defaults_manifest}" | head -n1)"
[ "${default_workload}" = "llm-request-router" ] || { echo "FAIL: llm-request-router workload did not render at defaults" >&2; exit 1; }

default_workload_args="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${defaults_manifest}")"
if printf '%s\n' "${default_workload_args}" | grep -qx -- "--metrics-prefix=llm_request_router_"; then
  echo "FAIL: --metrics-prefix is not supported by the pinned stargate 0.3.0 image" >&2
  exit 1
fi
if printf '%s\n' "${default_workload_args}" | grep -qx -- "--otel-service-name=llm-request-router"; then
  echo "FAIL: --otel-service-name is not supported by the pinned stargate 0.3.0 image" >&2
  exit 1
fi

default_tls_mount="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].volumeMounts[]? | select(.name == "stargate-tls") | .name' "${defaults_manifest}" | head -n1)"
[ -z "${default_tls_mount}" ] || { echo "FAIL: stargate-tls volumeMount rendered with default values" >&2; exit 1; }

default_tls_volume="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.volumes[]? | select(.name == "stargate-tls") | .name' "${defaults_manifest}" | head -n1)"
[ -z "${default_tls_volume}" ] || { echo "FAIL: stargate-tls volume rendered with default values" >&2; exit 1; }

# Pass 2: PKI, certificate, and TLS are fully enabled. Assert that every
# expected resource and wiring is in place.
helm template llm-request-router ./llm-request-router \
  --namespace nvcf \
  --values ./llm-request-router/values.yaml \
  --set llmRequestRouter.image.repository=stargate \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.secretName=stargate-quic-tls \
  --set llmRequestRouter.certificate.issuerRef.kind=ClusterIssuer \
  --set llmRequestRouter.certificate.issuerRef.name=nvcf-openbao-pki \
  --set-string 'llmRequestRouter.certificate.dnsNames[0]=*.stargate.localhost' \
  --set-string 'llmRequestRouter.kubernetes.advertisedHostnameTemplate=\{pod_name\}.stargate.localhost' \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  --set llmRequestRouter.tls.quicInsecure=false \
  --set llmRequestRouter.pki.enabled=true \
  --set-string 'llmRequestRouter.pki.allowedDomains=stargate.localhost\,cluster.local' \
  --set llmRequestRouter.pki.image.registry=nvcr.io \
  --set 'llmRequestRouter.pki.image.repository=<your-org>/nvcf-openbao-migrations' \
  --set llmRequestRouter.pki.image.tag=0.12.1 \
  --set llmRequestRouter.pki.namespace=plane-a-vault-system \
  --set llmRequestRouter.pki.baoService=plane-a-openbao-server.plane-a-vault-system.svc.cluster.local \
  --set llmRequestRouter.pki.serviceAccountName=plane-a-openbao-server-initialize-cluster \
  --set llmRequestRouter.pki.rootTokenSecretName=plane-a-openbao-server-root-token \
  --set llmRequestRouter.pki.sisServiceAccountNamespace=plane-a-sis \
  > "${manifest}"

cert_secret="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.secretName' "${manifest}")"
cert_issuer_kind="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.issuerRef.kind' "${manifest}")"
cert_issuer_name="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.issuerRef.name' "${manifest}")"
cert_dns_name="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.dnsNames[0]' "${manifest}")"

[ "${cert_secret}" = "stargate-quic-tls" ]
[ "${cert_issuer_kind}" = "ClusterIssuer" ]
[ "${cert_issuer_name}" = "nvcf-openbao-pki" ]
[ "${cert_dns_name}" = "*.stargate.localhost" ]

workload_args="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${manifest}")"
printf '%s\n' "${workload_args}" | grep -qx -- "--tls-cert-path=/etc/stargate/tls/tls.crt"
printf '%s\n' "${workload_args}" | grep -qx -- "--tls-key-path=/etc/stargate/tls/tls.key"
if printf '%s\n' "${workload_args}" | grep -qx -- "--quic-insecure"; then
  echo "unexpected --quic-insecure flag rendered" >&2
  exit 1
fi

tls_mount_name="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "stargate-tls" and .mountPath == "/etc/stargate/tls" and .readOnly == true) | .name' "${manifest}")"
tls_volume_name="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.volumes[] | select(.name == "stargate-tls" and .secret.secretName == "stargate-quic-tls") | .name' "${manifest}")"

[ "${tls_mount_name}" = "stargate-tls" ]
[ "${tls_volume_name}" = "stargate-tls" ]

# PKI provisioning hook: Helm hook Job rendered with the right env, image, and root-token mount.
hook_job_name="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.name' "${manifest}")"
hook_helm_hook="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.annotations."helm.sh/hook"' "${manifest}")"
hook_image="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].image' "${manifest}")"
hook_addons_llm="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "ADDONS_LLM_ENABLED") | .value' "${manifest}")"
hook_core_off="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "CORE_MIGRATIONS_ENABLED") | .value' "${manifest}")"
hook_allowed_domains="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_SERVICE_PKI_ALLOWED_DOMAINS") | .value' "${manifest}")"
hook_namespace="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.namespace' "${manifest}")"
hook_service_account="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.serviceAccountName' "${manifest}")"
hook_bao_service="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "BAO_SERVICE") | .value' "${manifest}")"
hook_internal_url="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "OPENBAO_SERVER_INTERNAL_URL") | .value' "${manifest}")"
hook_jwt_audience="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "OPENBAO_JWT_AUDIENCE") | .value' "${manifest}")"
hook_sis_namespace="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "SIS_SERVICE_ACCOUNT_NAMESPACE") | .value' "${manifest}")"
hook_root_token_secret="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.volumes[] | select(.name == "root-token") | .secret.secretName' "${manifest}")"

[ "${hook_job_name}" = "addons-llm-migrations" ]
[ "${hook_helm_hook}" = "pre-install,pre-upgrade" ]
[ "${hook_image}" = "nvcr.io/<your-org>/nvcf-openbao-migrations:0.12.1" ]
[ "${hook_addons_llm}" = "true" ]
[ "${hook_core_off}" = "false" ]
[ "${hook_allowed_domains}" = "stargate.localhost,cluster.local" ]
[ "${hook_namespace}" = "plane-a-vault-system" ]
[ "${hook_service_account}" = "plane-a-openbao-server-initialize-cluster" ]
[ "${hook_bao_service}" = "plane-a-openbao-server.plane-a-vault-system.svc.cluster.local" ]
[ "${hook_internal_url}" = "http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200" ]
[ "${hook_jwt_audience}" = "http://openbao-server.vault-system.svc.cluster.local:8200" ]
[ "${hook_sis_namespace}" = "plane-a-sis" ]
[ "${hook_root_token_secret}" = "plane-a-openbao-server-root-token" ]

# Exact and wildcard SANs cover a static advertised hostname.
exact_manifest="${tmp_dir}/exact.yaml"
render_certificate_case \
  "${exact_manifest}" \
  "Router.NVCF.Example.Internal" \
  "router.nvcf.example.internal"

wildcard_manifest="${tmp_dir}/wildcard.yaml"
render_certificate_case \
  "${wildcard_manifest}" \
  "router.example.internal" \
  "*.example.internal"

default_template_manifest="${tmp_dir}/default-template.yaml"
render_certificate_case \
  "${default_template_manifest}" \
  "" \
  "*.llm-request-router-headless.nvcf.svc.cluster.local"

# A single replica advertises its stable service DNS name. Render it with an
# exact Certificate SAN so this hostname and certificate path stay covered.
single_replica_manifest="${tmp_dir}/single-replica.yaml"
helm template llm-request-router ./llm-request-router \
  --namespace nvcf \
  --values ./llm-request-router/values.yaml \
  --set llmRequestRouter.image.repository=stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.discovery.disableDnsDiscovery=true \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.secretName=stargate-quic-tls \
  --set llmRequestRouter.certificate.issuerRef.name=nvcf-openbao-pki \
  --set-string 'llmRequestRouter.certificate.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
  > "${single_replica_manifest}"

single_replica_dns_name="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.dnsNames[0]' "${single_replica_manifest}")"
[ "${single_replica_dns_name}" = "llm-request-router.nvcf.svc.cluster.local" ] || fail "single-replica Certificate SAN did not render the advertised hostname"

single_replica_args="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${single_replica_manifest}")"
printf '%s\n' "${single_replica_args}" | grep -qx -- "--advertised-hostname-template=llm-request-router.nvcf.svc.cluster.local" || fail "single-replica render missing the advertised hostname template"

# Certificate validation resolves both supported placeholders. The pod name
# remains one DNS label, while the namespace is known at chart render time.
placeholder_manifest="${tmp_dir}/placeholder.yaml"
render_certificate_case \
  "${placeholder_manifest}" \
  "router-\{pod_name\}.\{namespace\}.stargate.internal" \
  "*.nvcf.stargate.internal"

# Preserve the existing empty-list rejection.
empty_dns_error="${tmp_dir}/empty-dns.err"
if helm template llm-request-router ./llm-request-router \
  --namespace nvcf \
  --values ./llm-request-router/values.yaml \
  --set llmRequestRouter.image.repository=stargate \
  --set llmRequestRouter.workload.kind=StatefulSet \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.issuerRef.name=nvcf-openbao-pki \
  > /dev/null 2> "${empty_dns_error}"; then
  fail "certificate render with empty dnsNames unexpectedly succeeded"
fi
grep -Fq \
  "llmRequestRouter.certificate.dnsNames is required when certificate.enabled is true" \
  "${empty_dns_error}" || fail "empty dnsNames render did not return the expected guard message"

uncovered_error="${tmp_dir}/uncovered.err"
if render_certificate_case \
  /dev/null \
  "router.example.internal" \
  "router.other.internal" \
  2> "${uncovered_error}"; then
  fail "uncovered advertised hostname unexpectedly rendered"
fi
grep -Fq \
  'advertised hostname template "router.example.internal" is not covered by llmRequestRouter.certificate.dnsNames ["router.other.internal"]' \
  "${uncovered_error}" || fail "uncovered hostname render did not return the expected guard message"

invalid_wildcard_error="${tmp_dir}/invalid-wildcard.err"
if render_certificate_case \
  /dev/null \
  "router.sub.example.internal" \
  "*.example.internal" \
  2> "${invalid_wildcard_error}"; then
  fail "wildcard SAN covering more than one hostname label unexpectedly rendered"
fi
grep -Fq \
  'advertised hostname template "router.sub.example.internal" is not covered by llmRequestRouter.certificate.dnsNames ["*.example.internal"]' \
  "${invalid_wildcard_error}" || fail "invalid wildcard render did not return the expected guard message"

misplaced_placeholder_error="${tmp_dir}/misplaced-placeholder.err"
if render_certificate_case \
  /dev/null \
  "router.\{pod_name\}.example.internal" \
  "*.llm-request-router-0.example.internal" \
  2> "${misplaced_placeholder_error}"; then
  fail "pod-name placeholder outside the wildcard label unexpectedly rendered"
fi
grep -Fq \
  'advertised hostname template "router.{pod_name}.example.internal" is not covered by llmRequestRouter.certificate.dnsNames ["*.llm-request-router-0.example.internal"]' \
  "${misplaced_placeholder_error}" || fail "misplaced placeholder render did not return the expected guard message"

short_wildcard_error="${tmp_dir}/short-wildcard.err"
if render_certificate_case \
  /dev/null \
  "router.internal" \
  "*.internal" \
  2> "${short_wildcard_error}"; then
  fail "wildcard SAN with fewer than two suffix labels unexpectedly rendered"
fi
grep -Fq \
  'advertised hostname template "router.internal" is not covered by llmRequestRouter.certificate.dnsNames ["*.internal"]' \
  "${short_wildcard_error}" || fail "short wildcard render did not return the expected guard message"

invalid_hostname_error="${tmp_dir}/invalid-hostname.err"
if render_certificate_case \
  /dev/null \
  "router..example.internal" \
  "router..example.internal" \
  2> "${invalid_hostname_error}"; then
  fail "advertised hostname with an empty DNS label unexpectedly rendered"
fi

literal_wildcard_hostname_error="${tmp_dir}/literal-wildcard-hostname.err"
if render_certificate_case \
  /dev/null \
  "*.example.internal" \
  "*.example.internal" \
  2> "${literal_wildcard_hostname_error}"; then
  fail "advertised hostname containing a literal wildcard unexpectedly rendered"
fi

malformed_san_error="${tmp_dir}/malformed-san.err"
if render_certificate_case \
  /dev/null \
  "router.example-.internal" \
  "*.example-.internal" \
  2> "${malformed_san_error}"; then
  fail "malformed wildcard SAN unexpectedly rendered"
fi

invalid_character_error="${tmp_dir}/invalid-character.err"
if render_certificate_case \
  /dev/null \
  "router-{}.example.internal" \
  "*.example.internal" \
  2> "${invalid_character_error}"; then
  fail "advertised hostname containing non-DNS braces unexpectedly rendered"
fi

invalid_underscore_error="${tmp_dir}/invalid-underscore.err"
if render_certificate_case \
  /dev/null \
  "router_name.example.internal" \
  "*.example.internal" \
  2> "${invalid_underscore_error}"; then
  fail "advertised hostname containing an underscore unexpectedly rendered"
fi

# Pass 3: existing-Secret identity mode. The operator owns issuance, so the
# chart must mount the pre-created Secret without rendering a Certificate or
# the OpenBao provisioning hook.
existing_secret_manifest="${tmp_dir}/existing-secret.yaml"
render_existing_secret_case() {
  output="$1"
  shift

  helm template llm-request-router ./llm-request-router \
    --namespace nvcf \
    --values ./llm-request-router/values.yaml \
    --set llmRequestRouter.image.repository=stargate \
    --set-string llmRequestRouter.tls.mode=existingSecret \
    --set llmRequestRouter.tls.quicInsecure=false \
    "$@" \
    > "${output}"
}

render_existing_secret_case \
  "${existing_secret_manifest}" \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set-string llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key

existing_secret_cert="$(yq -rN 'select(.kind == "Certificate") | .metadata.name' "${existing_secret_manifest}" | head -n1)"
[ -z "${existing_secret_cert}" ] || fail "existing-Secret mode rendered a Certificate: ${existing_secret_cert}"

existing_secret_job="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.name' "${existing_secret_manifest}" | head -n1)"
[ -z "${existing_secret_job}" ] || fail "existing-Secret mode rendered the OpenBao provisioning hook"

existing_secret_volume="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.volumes[] | select(.name == "stargate-tls" and .secret.secretName == "operator-quic-tls") | .name' "${existing_secret_manifest}")"
[ "${existing_secret_volume}" = "stargate-tls" ] || fail "existing-Secret mode did not mount the pre-created Secret"

existing_secret_mount="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "stargate-tls" and .mountPath == "/etc/stargate/tls" and .readOnly == true) | .name' "${existing_secret_manifest}")"
[ "${existing_secret_mount}" = "stargate-tls" ] || fail "existing-Secret mode did not mount stargate-tls read-only"

existing_secret_args="$(yq -rN 'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${existing_secret_manifest}")"
printf '%s\n' "${existing_secret_args}" | grep -qx -- "--tls-cert-path=/etc/stargate/tls/tls.crt" || fail "existing-Secret mode did not pass the certificate path"
printf '%s\n' "${existing_secret_args}" | grep -qx -- "--tls-key-path=/etc/stargate/tls/tls.key" || fail "existing-Secret mode did not pass the private key path"
if printf '%s\n' "${existing_secret_args}" | grep -qx -- "--quic-insecure"; then
  fail "existing-Secret mode enabled insecure request-router transport"
fi

# Mixed ownership: cert-manager and the operator cannot both own the identity.
mixed_ownership_error="${tmp_dir}/mixed-ownership.err"
if render_existing_secret_case \
  /dev/null \
  --set llmRequestRouter.certificate.enabled=true \
  --set-string llmRequestRouter.certificate.issuerRef.name=nvcf-openbao-pki \
  --set-string 'llmRequestRouter.certificate.dnsNames[0]=*.llm-request-router-headless.nvcf.svc.cluster.local' \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set-string llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  2> "${mixed_ownership_error}"; then
  fail "mixed certificate ownership unexpectedly rendered"
fi
grep -Fq \
  "llmRequestRouter.certificate.enabled must be false when llmRequestRouter.tls.mode is existingSecret" \
  "${mixed_ownership_error}" || fail "mixed ownership render did not return the expected guard message"

# Incomplete configuration: every required value reports itself by name. The
# mount and the Stargate arguments are conditional, so a missing value would
# otherwise leave the router on plaintext QUIC without any diagnostic.
check_required_existing_secret_value() {
  case_name="$1"
  expected_message="$2"
  shift 2

  error_file="${tmp_dir}/${case_name}.err"
  if render_existing_secret_case /dev/null "$@" 2> "${error_file}"; then
    fail "existing-Secret render without ${case_name} unexpectedly succeeded"
  fi
  grep -Fq "${expected_message}" "${error_file}" ||
    fail "${case_name} render did not return the expected guard message"
}

check_required_existing_secret_value \
  secret-name \
  "llmRequestRouter.tls.secretName is required when llmRequestRouter.tls.mode is existingSecret" \
  --set-string llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set-string llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key

check_required_existing_secret_value \
  cert-path \
  "llmRequestRouter.tls.certPath is required when llmRequestRouter.tls.mode is existingSecret" \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key

check_required_existing_secret_value \
  key-path \
  "llmRequestRouter.tls.keyPath is required when llmRequestRouter.tls.mode is existingSecret" \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt

# The Secret is mounted as a directory, so changing its mount location without
# changing the certificate paths would leave the router unable to read them.
mismatched_mount_path_error="${tmp_dir}/mismatched-mount-path.err"
if render_existing_secret_case \
  /dev/null \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.mountPath=/var/run/router-tls \
  --set-string llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set-string llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  2> "${mismatched_mount_path_error}"; then
  fail "existing-Secret mode accepted a mount path that does not contain the TLS files"
fi
grep -Fq \
  "llmRequestRouter.tls.mountPath must match the directory containing llmRequestRouter.tls.certPath and llmRequestRouter.tls.keyPath when llmRequestRouter.tls.mode is existingSecret" \
  "${mismatched_mount_path_error}" || fail "mismatched mount path did not return the expected guard message"

# Kubernetes volume mounts must be absolute paths. Matching relative paths
# would otherwise pass the directory consistency check but fail at deployment.
relative_mount_path_error="${tmp_dir}/relative-mount-path.err"
if render_existing_secret_case \
  /dev/null \
  --set-string llmRequestRouter.tls.secretName=operator-quic-tls \
  --set-string llmRequestRouter.tls.mountPath=tls \
  --set-string llmRequestRouter.tls.certPath=tls/tls.crt \
  --set-string llmRequestRouter.tls.keyPath=tls/tls.key \
  2> "${relative_mount_path_error}"; then
  fail "existing-Secret mode accepted a relative TLS mount path"
fi
grep -Fq \
  "llmRequestRouter.tls.mountPath must be an absolute path when llmRequestRouter.tls.mode is existingSecret" \
  "${relative_mount_path_error}" || fail "relative mount path did not return the expected guard message"

# An unknown mode must fail rather than silently fall back to cert-manager.
invalid_mode_error="${tmp_dir}/invalid-mode.err"
if helm template llm-request-router ./llm-request-router \
  --namespace nvcf \
  --values ./llm-request-router/values.yaml \
  --set llmRequestRouter.image.repository=stargate \
  --set-string llmRequestRouter.tls.mode=externalSecret \
  > /dev/null 2> "${invalid_mode_error}"; then
  fail "unknown llmRequestRouter.tls.mode unexpectedly rendered"
fi
grep -Fq \
  'llmRequestRouter.tls.mode must be certManager or existingSecret, got "externalSecret"' \
  "${invalid_mode_error}" || fail "unknown mode render did not return the expected guard message"

# The OpenBao signing role is created with allow_subdomains=true and
# allow_bare_domains=false, so a SAN outside allowed_domains renders cleanly and
# then fails at cert-manager issuance. Catch it at render instead. These cases
# pin the coverage rules, including the ones that must NOT fail: a wrong guard
# here would block valid deployments, which is worse than the trap it replaces.
assert_allowed_domains_case() {
  local description="$1"
  local expectation="$2"
  local allowed_domains="$3"
  local dns_name="$4"
  local advertised_hostname_template="${5:-}"
  local case_values="${tmp_dir}/allowed-domains-values.yaml"
  local case_error="${tmp_dir}/allowed-domains.err"

  cat > "${case_values}" <<EOF
llmRequestRouter:
  image:
    repository: nvcf/stargate
  backendRouter:
    enabled: true
  certificate:
    enabled: true
    issuerRef:
      name: nvcf-openbao-pki
    dnsNames: ["${dns_name}"]
  tls:
    certPath: /etc/stargate/tls/tls.crt
    keyPath: /etc/stargate/tls/tls.key
    quicInsecure: false
  pki:
    enabled: true
    allowedDomains: "${allowed_domains}"
    image:
      repository: nvcf-openbao-migrations
      tag: "1"
EOF

  if [ -n "${advertised_hostname_template}" ]; then
    cat >> "${case_values}" <<EOF
  kubernetes:
    advertisedHostnameTemplate: "${advertised_hostname_template}"
EOF
  fi

  if helm template llm-request-router ./llm-request-router \
    --namespace nvcf \
    --values ./llm-request-router/values.yaml \
    --values "${case_values}" \
    > /dev/null 2> "${case_error}"; then
    [ "${expectation}" = "pass" ] || fail "${description} unexpectedly rendered"
  else
    [ "${expectation}" = "fail" ] || fail "${description} unexpectedly failed to render"
    grep -Fq "is not covered by llmRequestRouter.pki.allowedDomains" "${case_error}" ||
      fail "${description} did not return the allowed-domains guard message"
  fi
}

# Must render: these are valid deployments.
assert_allowed_domains_case "documented customer-domain plus cluster.local" \
  pass "example.com,cluster.local" "llm-request-router.nvcf.svc.cluster.local"
assert_allowed_domains_case "whitespace around the comma separators" \
  pass " example.com , cluster.local " "llm-request-router.nvcf.svc.cluster.local"
assert_allowed_domains_case "allowed domain deeper in the suffix" \
  pass "svc.cluster.local" "llm-request-router.nvcf.svc.cluster.local"
assert_allowed_domains_case "direct wildcard covering the advertised pod hostname" \
  pass "example.com" "*.example.com" "{pod_name}.example.com"

# Must fail: issuance would be rejected.
assert_allowed_domains_case "no overlap with the certificate names" \
  fail "example.com" "llm-request-router.nvcf.svc.cluster.local"
assert_allowed_domains_case "name that ends with the domain but is not a subdomain" \
  fail "cluster.local" "evilcluster.local"
assert_allowed_domains_case "bare domain, which the role refuses to issue" \
  fail "llm-request-router-headless.nvcf.svc.cluster.local" "llm-request-router-headless.nvcf.svc.cluster.local"

echo "PKI render checks passed"
