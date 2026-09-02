#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

mode="${1:-apply}"
control_plane_id="${CONTROL_PLANE_ID:-}"
gateway_namespace="${GATEWAY_NAMESPACE:-envoy-gateway-system}"
gateway_class="${GATEWAY_CLASS:-eg}"

if [[ -z "$control_plane_id" || ${#control_plane_id} -gt 20 ||
      ! "$control_plane_id" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ||
      "$control_plane_id" == default ]]; then
  echo "CONTROL_PLANE_ID must be a DNS-1123 label of at most 20 characters and must not be default" >&2
  exit 1
fi

declare -a port_names=(SHARED_HTTP_PORT GRPC_API_PORT GRPC_WORKER_PORT NATS_PORT)
declare -a ports=()
for port_name in "${port_names[@]}"; do
  port="${!port_name:-}"
  if [[ ! "$port" =~ ^[0-9]+$ || "$port" -lt 1 || "$port" -gt 65535 ]]; then
    echo "$port_name must be an integer from 1 through 65535" >&2
    exit 1
  fi
  ports+=("$port")
done

if [[ "$(printf '%s\n' "${ports[@]}" | sort -u | wc -l | tr -d ' ')" -ne 4 ]]; then
  echo "Gateway listener ports must be distinct for control plane $control_plane_id" >&2
  exit 1
fi

render() {
  cat <<EOF
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: ${control_plane_id}-shared-proxy
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        name: ${control_plane_id}-shared-envoy
      envoyService:
        name: ${control_plane_id}-shared-svc
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${control_plane_id}-shared-gw
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  gatewayClassName: ${gateway_class}
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: ${control_plane_id}-shared-proxy
  listeners:
    - name: http
      protocol: HTTP
      port: ${SHARED_HTTP_PORT}
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              nvcf.nvidia.com/control-plane-id: ${control_plane_id}
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: ${control_plane_id}-grpc-proxy
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        name: ${control_plane_id}-grpc-envoy
      envoyService:
        name: ${control_plane_id}-grpc-svc
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${control_plane_id}-grpc-gw
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  gatewayClassName: ${gateway_class}
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: ${control_plane_id}-grpc-proxy
  listeners:
    - name: tcp
      protocol: TCP
      port: ${GRPC_API_PORT}
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              nvcf.nvidia.com/control-plane-id: ${control_plane_id}
    - name: worker-tcp
      protocol: TCP
      port: ${GRPC_WORKER_PORT}
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              nvcf.nvidia.com/control-plane-id: ${control_plane_id}
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: ${control_plane_id}-nats-proxy
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        name: ${control_plane_id}-nats-envoy
      envoyService:
        name: ${control_plane_id}-nats-svc
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${control_plane_id}-nats-gw
  namespace: ${gateway_namespace}
  labels:
    nvcf.nvidia.com/control-plane-id: ${control_plane_id}
spec:
  gatewayClassName: ${gateway_class}
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: ${control_plane_id}-nats-proxy
  listeners:
    - name: nats
      protocol: TCP
      port: ${NATS_PORT}
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              nvcf.nvidia.com/control-plane-id: ${control_plane_id}
EOF
}

kubectl_args=()
if [[ -n "${KUBECTL_CONTEXT:-}" ]]; then
  kubectl_args+=(--context "$KUBECTL_CONTEXT")
fi

gateway_names=(
  "${control_plane_id}-shared-gw"
  "${control_plane_id}-grpc-gw"
  "${control_plane_id}-nats-gw"
)

proxy_names=(
  "${control_plane_id}-shared-proxy"
  "${control_plane_id}-grpc-proxy"
  "${control_plane_id}-nats-proxy"
)

is_desired_gateway() {
  local candidate="$1"
  local gateway_name
  for gateway_name in "${gateway_names[@]}"; do
    if [[ "$candidate" == "$gateway_name" ]]; then
      return 0
    fi
  done
  return 1
}

is_desired_proxy() {
  local candidate="$1"
  local proxy_name
  for proxy_name in "${proxy_names[@]}"; do
    if [[ "$candidate" == "$proxy_name" ]]; then
      return 0
    fi
  done
  return 1
}

get_gateways() {
  kubectl "${kubectl_args[@]}" get gateways.gateway.networking.k8s.io \
    --namespace "$gateway_namespace" -o json
}

get_envoy_proxies() {
  kubectl "${kubectl_args[@]}" get envoyproxies.gateway.envoyproxy.io \
    --namespace "$gateway_namespace" -o json
}

validate_existing_gateways() {
  local gateways_json="$1"
  local name owner listener_port desired_port reference_group reference_kind reference_name

  while IFS=$'\t' read -r name owner; do
    if is_desired_gateway "$name" && [[ "$owner" != "$control_plane_id" ]]; then
      echo "Refusing to modify Gateway $gateway_namespace/$name owned by control plane ${owner:-<unowned>}" >&2
      return 1
    fi
  done < <(printf '%s\n' "$gateways_json" | jq -r \
    '.items[] | [.metadata.name, (.metadata.labels["nvcf.nvidia.com/control-plane-id"] // "")] | @tsv')

  while IFS=$'\t' read -r name reference_group reference_kind reference_name; do
    if ! is_desired_gateway "$name" &&
        [[ "$reference_group" == "gateway.envoyproxy.io" ]] &&
        [[ "$reference_kind" == "EnvoyProxy" ]] &&
        is_desired_proxy "$reference_name"; then
      echo "Refusing to modify EnvoyProxy $gateway_namespace/$reference_name: referenced by foreign Gateway $gateway_namespace/$name" >&2
      return 1
    fi
  done < <(printf '%s\n' "$gateways_json" | jq -r \
    '.items[] | [.metadata.name, (.spec.infrastructure.parametersRef.group // ""), (.spec.infrastructure.parametersRef.kind // ""), (.spec.infrastructure.parametersRef.name // "")] | @tsv')

  while IFS=$'\t' read -r name listener_port; do
    if is_desired_gateway "$name"; then
      continue
    fi
    for desired_port in "${ports[@]}"; do
      if [[ "$listener_port" == "$desired_port" ]]; then
        echo "Gateway listener port $desired_port is already used by $gateway_namespace/$name" >&2
        return 1
      fi
    done
  done < <(printf '%s\n' "$gateways_json" | jq -r \
    '.items[] as $gateway | $gateway.spec.listeners[]? | [$gateway.metadata.name, (.port | tostring)] | @tsv')
}

validate_existing_envoy_proxies() {
  local proxies_json="$1"
  local name owner

  while IFS=$'\t' read -r name owner; do
    if is_desired_proxy "$name" && [[ "$owner" != "$control_plane_id" ]]; then
      echo "Refusing to modify EnvoyProxy $gateway_namespace/$name owned by control plane ${owner:-<unowned>}" >&2
      return 1
    fi
  done < <(printf '%s\n' "$proxies_json" | jq -r \
    '.items[] | [.metadata.name, (.metadata.labels["nvcf.nvidia.com/control-plane-id"] // "")] | @tsv')
}

case "$mode" in
  render)
    render
    ;;
  apply)
    command -v jq >/dev/null 2>&1 || {
      echo "jq is required to validate existing Gateways" >&2
      exit 1
    }
    gateways_json="$(get_gateways)"
    proxies_json="$(get_envoy_proxies)"
    validate_existing_gateways "$gateways_json"
    validate_existing_envoy_proxies "$proxies_json"
    render | kubectl "${kubectl_args[@]}" apply -f -
    for gateway_name in "${gateway_names[@]}"; do
      kubectl "${kubectl_args[@]}" wait --namespace "$gateway_namespace" \
        --for=condition=Programmed --timeout="${GATEWAY_READY_TIMEOUT:-120s}" \
        "gateway/$gateway_name"
    done
    ;;
  delete)
    command -v jq >/dev/null 2>&1 || {
      echo "jq is required to validate existing Gateways" >&2
      exit 1
    }
    gateways_json="$(get_gateways)"
    proxies_json="$(get_envoy_proxies)"
    validate_existing_gateways "$gateways_json"
    validate_existing_envoy_proxies "$proxies_json"
    for gateway_name in "${gateway_names[@]}"; do
      if printf '%s\n' "$gateways_json" | jq -e --arg name "$gateway_name" \
          '.items[] | select(.metadata.name == $name)' >/dev/null; then
        kubectl "${kubectl_args[@]}" delete gateway "$gateway_name" \
          --namespace "$gateway_namespace" --wait=true
      fi
    done
    for proxy_name in "${proxy_names[@]}"; do
      if printf '%s\n' "$proxies_json" | jq -e --arg name "$proxy_name" \
          '.items[] | select(.metadata.name == $name)' >/dev/null; then
        kubectl "${kubectl_args[@]}" delete envoyproxy "$proxy_name" \
          --namespace "$gateway_namespace" --wait=true
      fi
    done
    ;;
  *)
    echo "usage: $0 [render|apply|delete]" >&2
    exit 1
    ;;
esac
