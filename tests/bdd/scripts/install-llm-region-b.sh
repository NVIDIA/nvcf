#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

control_context="${CONTROL_CONTEXT:-k3d-ncp-local-cp}"
compute_context="${COMPUTE_CONTEXT:-k3d-ncp-local-compute-1}"
namespace="nvcf"
region_b_release="llm-request-router-region-b"
region_b_watch_host="region-b-watch.nvcf.svc.cluster.local"
region_b_headless_host="*.llm-request-router-region-b-headless.nvcf.svc.cluster.local"
chart="${REPO_ROOT:?REPO_ROOT is required}/deploy/helm/llm-request-router/llm-request-router"

values_json="$(helm --kube-context "${control_context}" get values llm-request-router \
  --namespace "${namespace}" --output json)"

printf '%s' "${values_json}" | jq --arg watch_host "${region_b_watch_host}" '
  {
    llmRequestRouter: (
      .llmRequestRouter
      | .fullnameOverride = "llm-request-router-region-b"
      | .replicaCount = 2
      | .workload.kind = "StatefulSet"
      | .service.headlessName = "llm-request-router-region-b-headless"
      | .kubernetes.advertisedHostnameTemplate = "{pod_name}.llm-request-router-region-b-headless.nvcf.svc.cluster.local"
      | .discovery.remoteWatchUrls = []
      | .backendRouter.enabled = true
      | .backendRouter.pylonGrpcDialAddress = ("https://" + $watch_host + ":50071")
      | .backendRouter.pylonReverseTunnelDialAddress = ($watch_host + ":50072")
      | .serviceAccount.create = false
      | .serviceAccount.name = "llm-request-router"
      | .pki.enabled = false
      | .certificate.enabled = false
      | .tls.mode = "existingSecret"
      | .tls.secretName = "stargate-quic-tls"
      | .image.pullPolicy = "IfNotPresent"
      | .backendRouter.image.pullPolicy = "IfNotPresent"
    )
  }
' | helm --kube-context "${control_context}" upgrade --install "${region_b_release}" "${chart}" \
  --namespace "${namespace}" --values - --wait --timeout 10m

kubectl --context "${control_context}" apply -f - <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata:
  name: llm-worker-region-b-grpc
  namespace: envoy-gateway-system
spec:
  parentRefs:
    - name: grpc-gw
      namespace: envoy-gateway-system
      sectionName: llm-grpc
  hostnames:
    - "${region_b_watch_host}"
    - "${region_b_headless_host}"
  rules:
    - backendRefs:
        - name: llm-request-router-region-b-backend-router
          namespace: nvcf
          port: 50071
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: llm-worker-region-b-grpc-streams
  namespace: envoy-gateway-system
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: GRPCRoute
      name: llm-worker-region-b-grpc
  timeout:
    http:
      requestTimeout: 0s
---
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-llm-worker-region-b-route
  namespace: nvcf
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: GRPCRoute
      namespace: envoy-gateway-system
  to:
    - group: ""
      kind: Service
      name: llm-request-router-region-b-backend-router
YAML

control_plane_ip="$(kubectl --context "${compute_context}" get endpoints llm-request-router \
  --namespace "${namespace}" --output jsonpath='{.subsets[0].addresses[0].ip}')"
if [[ -z "${control_plane_ip}" ]]; then
  echo "control-plane endpoint alias has no address" >&2
  exit 1
fi

kubectl --context "${compute_context}" apply -f - <<YAML
apiVersion: v1
kind: Service
metadata:
  name: region-b-watch
  namespace: nvcf
spec:
  ports:
    - name: llm-grpc
      port: 50071
      targetPort: llm-grpc
      protocol: TCP
    - name: llm-quic
      port: 50072
      targetPort: llm-quic
      protocol: UDP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: region-b-watch
  namespace: nvcf
subsets:
  - addresses:
      - ip: ${control_plane_ip}
    ports:
      - name: llm-grpc
        port: 50071
        protocol: TCP
      - name: llm-quic
        port: 50072
        protocol: UDP
YAML

kubectl --context "${control_context}" rollout status \
  statefulset/llm-request-router-region-b --namespace "${namespace}" --timeout=10m
kubectl --context "${control_context}" rollout status \
  deployment/llm-request-router-region-b-backend-router --namespace "${namespace}" --timeout=10m
