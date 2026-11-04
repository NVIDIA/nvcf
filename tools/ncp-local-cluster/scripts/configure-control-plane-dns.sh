#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

dry_run=false
if [ "${1:-}" = "--dry-run" ]; then
  dry_run=true
fi

domain="${CONTROL_PLANE_DOMAIN:-nvcf-control-plane.test}"

sis_ip="${CONTROL_PLANE_SIS_SERVICE_IP:-}"
reval_ip="${CONTROL_PLANE_REVAL_SERVICE_IP:-}"
nats_ip="${CONTROL_PLANE_NATS_SERVICE_IP:-}"

if [ -z "$sis_ip" ]; then
  sis_ip="$(kubectl -n sis get service api -o jsonpath='{.spec.clusterIP}')"
fi
if [ -z "$reval_ip" ]; then
  reval_ip="$(kubectl -n nvcf get service reval -o jsonpath='{.spec.clusterIP}')"
fi
if [ -z "$nats_ip" ]; then
  nats_ip="$(kubectl -n nats-system get service nats -o jsonpath='{.spec.clusterIP}')"
fi

if [ -z "$sis_ip" ] || [ -z "$reval_ip" ] || [ -z "$nats_ip" ]; then
  echo "ERROR: unable to determine compute alias service ClusterIPs" >&2
  exit 1
fi

echo "Configuring CoreDNS ${domain} zone to compute alias Services"

yaml="$(cat <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  nvcf-control-plane.server: |
    ${domain}:53 {
        errors
        cache 30
        hosts {
            ${sis_ip} sis.${domain}
            ${reval_ip} reval.${domain}
            ${nats_ip} nats.${domain}
            fallthrough
        }
    }
YAML
)"

if [ "$dry_run" = true ]; then
  printf '%s\n' "$yaml"
  exit 0
fi

printf '%s\n' "$yaml" | kubectl apply -f -

kubectl -n kube-system rollout restart deployment/coredns >/dev/null
kubectl -n kube-system rollout status deployment/coredns --timeout=120s
