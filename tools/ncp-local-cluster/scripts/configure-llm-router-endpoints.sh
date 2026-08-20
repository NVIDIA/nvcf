#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: configure-llm-router-endpoints.sh [--dry-run]

Expose a control-cluster LLM request router to one compute k3d cluster. Run
this after the control-plane chart creates the shared and per-replica NodePort
Services. Dry-run discovers the topology and prints the compute aliases without
connecting Docker networks or applying Kubernetes resources.

Important environment variables:
  CONTROL_PLANE_CLUSTER_NAME       Default: ncp-local-cp
  COMPUTE_CLUSTER_NAME             Default: ncp-local-compute-1
  CONTROL_PLANE_CONTEXT            Default: k3d-$CONTROL_PLANE_CLUSTER_NAME
  COMPUTE_CONTEXT                  Default: k3d-$COMPUTE_CLUSTER_NAME
  CONTROL_PLANE_NODE_CONTAINER     Default: k3d-$CONTROL_PLANE_CLUSTER_NAME-server-0
  CONTROL_PLANE_NODE_IP            Optional discovery override
  LLM_REQUEST_ROUTER_REPLICAS      Optional StatefulSet replica override
  LLM_REQUEST_ROUTER_ALIAS_NAMESPACE  Default: nvcf-llm-router
EOF
}

dry_run=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      dry_run=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

control_cluster="${CONTROL_PLANE_CLUSTER_NAME:-ncp-local-cp}"
compute_cluster="${COMPUTE_CLUSTER_NAME:-ncp-local-compute-1}"
control_context="${CONTROL_PLANE_CONTEXT:-k3d-${control_cluster}}"
compute_context="${COMPUTE_CONTEXT:-k3d-${compute_cluster}}"
control_node="${CONTROL_PLANE_NODE_CONTAINER:-k3d-${control_cluster}-server-0}"
compute_network="${COMPUTE_DOCKER_NETWORK:-k3d-${compute_cluster}}"
control_namespace="${LLM_REQUEST_ROUTER_NAMESPACE:-nvcf}"
alias_namespace="${LLM_REQUEST_ROUTER_ALIAS_NAMESPACE:-nvcf-llm-router}"
router_name="${LLM_REQUEST_ROUTER_NAME:-llm-request-router}"

require_positive_integer() {
  local name="$1"
  local value="$2"
  case "$value" in
    ''|*[!0-9]*)
      echo "ERROR: ${name} must be a positive integer, got '${value}'" >&2
      exit 1
      ;;
  esac
  if [ "$value" -lt 1 ]; then
    echo "ERROR: ${name} must be a positive integer, got '${value}'" >&2
    exit 1
  fi
}

require_node_port() {
  local service_name="$1"
  local port_name="$2"
  local value="$3"
  case "$value" in
    ''|*[!0-9]*)
      echo "ERROR: ${service_name}/${port_name} has no allocated NodePort" >&2
      exit 1
      ;;
  esac
  if [ "$value" -lt 1 ] || [ "$value" -gt 65535 ]; then
    echo "ERROR: ${service_name}/${port_name} returned invalid NodePort '${value}'" >&2
    exit 1
  fi
}

discover_node_port() {
  local service_name="$1"
  local port_name="$2"
  kubectl --context "$control_context" --namespace "$control_namespace" \
    get service "$service_name" \
    -o "jsonpath={.spec.ports[?(@.name==\"${port_name}\")].nodePort}"
}

replicas="${LLM_REQUEST_ROUTER_REPLICAS:-}"
if [ -z "$replicas" ]; then
  replicas="$(kubectl --context "$control_context" --namespace "$control_namespace" \
    get statefulset "$router_name" -o 'jsonpath={.spec.replicas}')"
fi
require_positive_integer LLM_REQUEST_ROUTER_REPLICAS "$replicas"

node_ip="${CONTROL_PLANE_NODE_IP:-}"
if [ -z "$node_ip" ]; then
  node_ip="$(docker network inspect "$compute_network" \
    --format '{{range .Containers}}{{if eq .Name "'"${control_node}"'"}}{{.IPv4Address}}{{end}}{{end}}')"
  if [ -z "$node_ip" ] && [ "$dry_run" = false ]; then
    docker network connect "$compute_network" "$control_node"
    node_ip="$(docker network inspect "$compute_network" \
      --format '{{range .Containers}}{{if eq .Name "'"${control_node}"'"}}{{.IPv4Address}}{{end}}{{end}}')"
  fi
  node_ip="${node_ip%%/*}"
fi
if [ -z "$node_ip" ] || [ "$node_ip" = "<no value>" ]; then
  echo "ERROR: unable to discover ${control_node} on ${compute_network}" >&2
  if [ "$dry_run" = true ]; then
    echo "Set CONTROL_PLANE_NODE_IP for an offline dry-run." >&2
  fi
  exit 1
fi

shared_grpc_node_port="${LLM_REQUEST_ROUTER_SHARED_GRPC_NODE_PORT:-$(discover_node_port "$router_name" grpc)}"
require_node_port "$router_name" grpc "$shared_grpc_node_port"

declare -a grpc_node_ports=()
declare -a quic_node_ports=()
for ((ordinal = 0; ordinal < replicas; ordinal++)); do
  service_name="${router_name}-${ordinal}"
  grpc_node_port="$(discover_node_port "$service_name" grpc)"
  quic_node_port="$(discover_node_port "$service_name" quic)"
  require_node_port "$service_name" grpc "$grpc_node_port"
  require_node_port "$service_name" quic "$quic_node_port"
  grpc_node_ports+=("$grpc_node_port")
  quic_node_ports+=("$quic_node_port")
done

echo "LLM router endpoint source: ${control_node} (${node_ip})" >&2
echo "Shared seed: ${router_name}.${control_namespace}.svc.cluster.local:50071 -> ${node_ip}:${shared_grpc_node_port}/TCP" >&2
echo "Per-replica dial domain: ${alias_namespace}.svc.cluster.local" >&2

render_aliases() {
  cat <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${alias_namespace}
  labels:
    app.kubernetes.io/managed-by: ncp-local-cluster
---
apiVersion: v1
kind: Service
metadata:
  name: ${router_name}
  namespace: ${control_namespace}
  labels:
    app.kubernetes.io/managed-by: ncp-local-cluster
spec:
  ports:
    - name: grpc
      port: 50071
      targetPort: grpc
      protocol: TCP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: ${router_name}
  namespace: ${control_namespace}
  labels:
    app.kubernetes.io/managed-by: ncp-local-cluster
subsets:
  - addresses:
      - ip: ${node_ip}
    ports:
      - name: grpc
        port: ${shared_grpc_node_port}
        protocol: TCP
YAML

  for ((ordinal = 0; ordinal < replicas; ordinal++)); do
    service_name="${router_name}-${ordinal}"
    cat <<YAML
---
apiVersion: v1
kind: Service
metadata:
  name: ${service_name}
  namespace: ${alias_namespace}
  labels:
    app.kubernetes.io/managed-by: ncp-local-cluster
spec:
  ports:
    - name: grpc
      port: 50071
      targetPort: grpc
      protocol: TCP
    - name: quic
      port: 50072
      targetPort: quic
      protocol: UDP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: ${service_name}
  namespace: ${alias_namespace}
  labels:
    app.kubernetes.io/managed-by: ncp-local-cluster
subsets:
  - addresses:
      - ip: ${node_ip}
    ports:
      - name: grpc
        port: ${grpc_node_ports[$ordinal]}
        protocol: TCP
      - name: quic
        port: ${quic_node_ports[$ordinal]}
        protocol: UDP
YAML
  done
}

if [ "$dry_run" = true ]; then
  render_aliases
  exit 0
fi

render_aliases | kubectl --context "$compute_context" apply -f -
