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

# StatefulSet should still render (chart's primary purpose) but with no
# stargate-tls volume or volumeMount.
default_workload="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .metadata.name' "${defaults_manifest}" | head -n1)"
[ "${default_workload}" = "llm-request-router" ] || { echo "FAIL: llm-request-router StatefulSet did not render at defaults" >&2; exit 1; }

default_workload_args="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${defaults_manifest}")"
if printf '%s\n' "${default_workload_args}" | grep -qx -- "--metrics-prefix=llm_request_router_"; then
  echo "FAIL: --metrics-prefix is not supported by the pinned stargate 0.3.0 image" >&2
  exit 1
fi
if printf '%s\n' "${default_workload_args}" | grep -qx -- "--otel-service-name=llm-request-router"; then
  echo "FAIL: --otel-service-name is not supported by the pinned stargate 0.3.0 image" >&2
  exit 1
fi

default_tls_mount="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].volumeMounts[]? | select(.name == "stargate-tls") | .name' "${defaults_manifest}" | head -n1)"
[ -z "${default_tls_mount}" ] || { echo "FAIL: stargate-tls volumeMount rendered with default values" >&2; exit 1; }

default_tls_volume="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.volumes[]? | select(.name == "stargate-tls") | .name' "${defaults_manifest}" | head -n1)"
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
  > "${manifest}"

cert_secret="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.secretName' "${manifest}")"
cert_issuer_kind="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.issuerRef.kind' "${manifest}")"
cert_issuer_name="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.issuerRef.name' "${manifest}")"
cert_dns_name="$(yq -rN 'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.dnsNames[0]' "${manifest}")"

[ "${cert_secret}" = "stargate-quic-tls" ]
[ "${cert_issuer_kind}" = "ClusterIssuer" ]
[ "${cert_issuer_name}" = "nvcf-openbao-pki" ]
[ "${cert_dns_name}" = "*.stargate.localhost" ]

workload_args="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${manifest}")"
printf '%s\n' "${workload_args}" | grep -qx -- "--tls-cert-path=/etc/stargate/tls/tls.crt"
printf '%s\n' "${workload_args}" | grep -qx -- "--tls-key-path=/etc/stargate/tls/tls.key"
if printf '%s\n' "${workload_args}" | grep -qx -- "--quic-insecure"; then
  echo "unexpected --quic-insecure flag rendered" >&2
  exit 1
fi

tls_mount_name="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "stargate-tls" and .mountPath == "/etc/stargate/tls" and .readOnly == true) | .name' "${manifest}")"
tls_volume_name="$(yq -rN 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.volumes[] | select(.name == "stargate-tls" and .secret.secretName == "stargate-quic-tls") | .name' "${manifest}")"

[ "${tls_mount_name}" = "stargate-tls" ]
[ "${tls_volume_name}" = "stargate-tls" ]

# PKI provisioning hook: Helm hook Job rendered with the right env, image, and root-token mount.
hook_job_name="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.name' "${manifest}")"
hook_helm_hook="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .metadata.annotations."helm.sh/hook"' "${manifest}")"
hook_image="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].image' "${manifest}")"
hook_addons_llm="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "ADDONS_LLM_ENABLED") | .value' "${manifest}")"
hook_core_off="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "CORE_MIGRATIONS_ENABLED") | .value' "${manifest}")"
hook_allowed_domains="$(yq -rN 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "NVCF_SERVICE_PKI_ALLOWED_DOMAINS") | .value' "${manifest}")"

[ "${hook_job_name}" = "addons-llm-migrations" ]
[ "${hook_helm_hook}" = "pre-install,pre-upgrade" ]
[ "${hook_image}" = "nvcr.io/<your-org>/nvcf-openbao-migrations:0.12.1" ]
[ "${hook_addons_llm}" = "true" ]
[ "${hook_core_off}" = "false" ]
[ "${hook_allowed_domains}" = "stargate.localhost,cluster.local" ]

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

echo "PKI render checks passed"
