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

cluster_name="${CLUSTER_NAME:-ncp-local-compute-1}"
control_plane_cluster_name="${CONTROL_PLANE_CLUSTER_NAME:-ncp-local-cp}"
control_plane_lb_container="${CONTROL_PLANE_LB_CONTAINER:-k3d-${control_plane_cluster_name}-serverlb}"
domain="${CONTROL_PLANE_DOMAIN:-nvcf-control-plane.test}"
http_port="${CONTROL_PLANE_LB_HTTP_PORT:-80}"
nats_port="${CONTROL_PLANE_LB_NATS_PORT:-4222}"
network_name="k3d-${cluster_name}"

lb_ip="${CONTROL_PLANE_LB_IP:-}"
if [ -z "$lb_ip" ] && [ "$dry_run" = true ]; then
  lb_ip="192.0.2.10"
fi
if [ -z "$lb_ip" ]; then
  lb_ip="$(docker network inspect "$network_name" --format '{{range .Containers}}{{if eq .Name "'"${control_plane_lb_container}"'"}}{{.IPv4Address}}{{end}}{{end}}')"
  if [ -z "$lb_ip" ]; then
    docker network connect "$network_name" "$control_plane_lb_container"
    lb_ip="$(docker network inspect "$network_name" --format '{{range .Containers}}{{if eq .Name "'"${control_plane_lb_container}"'"}}{{.IPv4Address}}{{end}}{{end}}')"
  fi
  lb_ip="${lb_ip%%/*}"
fi

if [ -z "$lb_ip" ] || [ "$lb_ip" = "<no value>" ]; then
  echo "ERROR: unable to determine ${control_plane_lb_container} IP on ${network_name}" >&2
  exit 1
fi

echo "Configuring compute aliases for ${domain} via ${control_plane_lb_container} (${lb_ip})"
echo "  HTTP service port 8080 -> control-plane load balancer container port ${http_port}"
echo "  NATS service port 4222 -> control-plane load balancer container port ${nats_port}"

yaml="$(cat <<YAML
apiVersion: v1
kind: Endpoints
metadata:
  name: api
  namespace: sis
subsets:
  - addresses:
      - ip: ${lb_ip}
    ports:
      - name: http
        port: ${http_port}
        protocol: TCP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: reval
  namespace: nvcf
subsets:
  - addresses:
      - ip: ${lb_ip}
    ports:
      - name: http
        port: ${http_port}
        protocol: TCP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: nats
  namespace: nats-system
subsets:
  - addresses:
      - ip: ${lb_ip}
    ports:
      - name: client
        port: ${nats_port}
        protocol: TCP
YAML
)"

if [ "$dry_run" = true ]; then
  printf '%s\n' "$yaml"
  exit 0
fi

printf '%s\n' "$yaml" | kubectl apply -f -
