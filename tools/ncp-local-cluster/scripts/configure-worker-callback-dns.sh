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

# Resolves *.<WORKER_CALLBACK_DOMAIN> to the worker-callback-proxy Service.
# Server-side apply owns only the worker-callback.override key, so other
# coredns-custom keys, such as the ones configure-control-plane-dns.sh
# writes, are left in place.

set -euo pipefail

dry_run=false
if [ "${1:-}" = "--dry-run" ]; then
  dry_run=true
fi

domain="${WORKER_CALLBACK_DOMAIN:?set WORKER_CALLBACK_DOMAIN to workerCallbackProxy.workerDomain}"
service="${WORKER_CALLBACK_PROXY_SERVICE:-worker-callback-proxy.nvcf.svc.cluster.local}"

if ! [[ "$domain" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$ ]]; then
  echo "ERROR: WORKER_CALLBACK_DOMAIN must be a lowercase DNS name, got ${domain}" >&2
  exit 1
fi
domain_regex="${domain//./\\.}"

yaml="$(cat <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  worker-callback.override: |
    rewrite stop {
      name regex (.+)\.${domain_regex}\.$ ${service}.
      answer auto
    }
YAML
)"

if [ "$dry_run" = true ]; then
  printf '%s\n' "$yaml"
  exit 0
fi

echo "Configuring CoreDNS *.${domain} -> ${service}"
printf '%s\n' "$yaml" | kubectl apply --server-side --field-manager=nvcf-worker-callback-dns -f -

kubectl -n kube-system rollout restart deployment/coredns >/dev/null
kubectl -n kube-system rollout status deployment/coredns --timeout=120s
