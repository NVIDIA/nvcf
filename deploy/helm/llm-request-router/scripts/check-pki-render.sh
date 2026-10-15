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

manifest="$(mktemp)"
defaults_manifest="$(mktemp)"
trap 'rm -f "${manifest}" "${defaults_manifest}"' EXIT

# Pass 1: defaults — pki/certificate/tls all off. Assert the chart does NOT
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

# Pass 2: fully enabled — pki + certificate + tls. Assert that every
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
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  --set llmRequestRouter.tls.quicInsecure=false \
  --set llmRequestRouter.pki.enabled=true \
  --set-string 'llmRequestRouter.pki.allowedDomains=stargate.localhost\,cluster.local' \
  --set llmRequestRouter.pki.image.registry=nvcr.io \
  --set llmRequestRouter.pki.image.repository=<your-org>/nvcf-openbao-migrations \
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
[ "${hook_image}" = "<your-registry>/<your-org>/nvcf-openbao-migrations:0.12.1" ]
[ "${hook_addons_llm}" = "true" ]
[ "${hook_core_off}" = "false" ]
[ "${hook_allowed_domains}" = "stargate.localhost,cluster.local" ]
