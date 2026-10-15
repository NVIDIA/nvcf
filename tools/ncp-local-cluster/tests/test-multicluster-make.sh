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

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_eq() {
  local expected="$1"
  local actual="$2"
  local label="$3"

  if [ "$actual" != "$expected" ]; then
    fail "${label}: expected '${expected}', got '${actual}'"
  fi
}

run_make() {
  make --no-print-directory -s -C "$ROOT_DIR" "$@"
}

assert_missing_docker_config_blocks_target() {
  local target="$1"
  local missing_config_root
  local missing_config_bin
  local missing_config_output

  missing_config_root="$(mktemp -d)"
  missing_config_bin="$(mktemp -d)"
  missing_config_output="$(mktemp)"
  cp "$ROOT_DIR/Makefile" "$missing_config_root/Makefile"
  cat >"$missing_config_bin/k3d" <<'EOF'
#!/usr/bin/env bash
echo "k3d should not be called before docker config validation" >&2
exit 42
EOF
  cat >"$missing_config_bin/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "kubectl should not be called before docker config validation" >&2
exit 42
EOF
  cat >"$missing_config_bin/go" <<'EOF'
#!/usr/bin/env bash
echo "go should not be called before docker config validation" >&2
exit 42
EOF
  chmod +x "$missing_config_bin/k3d" "$missing_config_bin/kubectl" "$missing_config_bin/go"
  if PATH="$missing_config_bin:$PATH" make --no-print-directory -s -C "$missing_config_root" "$target" >"$missing_config_output" 2>&1; then
    cat "$missing_config_output" >&2
    rm -rf "$missing_config_root" "$missing_config_bin"
    rm -f "$missing_config_output"
    fail "${target} should fail when secrets/docker-config.json is missing"
  fi
  if ! grep -q "secrets/docker-config.json is required" "$missing_config_output"; then
    cat "$missing_config_output" >&2
    rm -rf "$missing_config_root" "$missing_config_bin"
    rm -f "$missing_config_output"
    fail "${target} must check docker config before invoking build or cluster tooling"
  fi
  rm -rf "$missing_config_root" "$missing_config_bin"
  rm -f "$missing_config_output"
}

assert_missing_docker_config_blocks_target start
assert_missing_docker_config_blocks_target build-and-deploy-cluster

print_directory_clusters="$(MAKEFLAGS=--print-directory; export MAKEFLAGS; run_make print-compute-clusters)"
assert_eq "ncp-local-compute-1" "$print_directory_clusters" "print-directory compute cluster output"
if ! grep -q '\$(MAKE) --no-print-directory -s print-compute-clusters' "$ROOT_DIR/Makefile"; then
  fail "recursive print-compute-clusters calls must suppress make directory noise"
fi

default_clusters="$(run_make print-compute-clusters)"
assert_eq "ncp-local-compute-1" "$default_clusters" "default compute cluster"

three_clusters="$(run_make print-compute-clusters COMPUTE_CLUSTER_COUNT=3)"
assert_eq "ncp-local-compute-1 ncp-local-compute-2 ncp-local-compute-3" "$three_clusters" "count-derived compute clusters"

explicit_clusters="$(run_make print-compute-clusters COMPUTE_CLUSTER_COUNT=3 COMPUTE_CLUSTERS="ncp-east ncp-west")"
assert_eq "ncp-east ncp-west" "$explicit_clusters" "explicit compute clusters override count"

for invalid_count in 0 abc ""; do
  tmp_output="$(mktemp)"
  if run_make print-compute-clusters COMPUTE_CLUSTER_COUNT="$invalid_count" >"$tmp_output" 2>&1; then
    cat "$tmp_output" >&2
    rm -f "$tmp_output"
    fail "invalid count '${invalid_count}' should fail"
  fi
  if ! grep -q "COMPUTE_CLUSTER_COUNT must be a positive integer" "$tmp_output"; then
    cat "$tmp_output" >&2
    rm -f "$tmp_output"
    fail "invalid count '${invalid_count}' did not print validation message"
  fi
  rm -f "$tmp_output"
done

help_output="$(run_make help)"
for target in \
  build-and-deploy-control-plane-cluster \
  build-and-deploy-compute-plane-cluster \
  build-and-deploy-multicluster \
  configure-compute-control-plane-dns \
  deploy-compute-control-plane-endpoints \
  deploy-control-plane-endpoints \
  destroy-control-plane \
  destroy-compute-plane \
  destroy-multicluster; do
  if ! grep -q "$target" <<<"$help_output"; then
    fail "make help missing target '${target}'"
  fi
done

if ! grep -q '\${CONTROL_PLANE_NATS_PORT}:4222' "$ROOT_DIR/k3d-config-control-plane.yaml"; then
  fail "control-plane k3d config must expose CONTROL_PLANE_NATS_PORT to Gateway port 4222"
fi
if ! grep -q '\${CONTROL_PLANE_GRPC_PORT}:9090' "$ROOT_DIR/k3d-config-control-plane.yaml"; then
  fail "control-plane k3d config must expose CONTROL_PLANE_GRPC_PORT to Gateway port 9090"
fi
if ! grep -q '\${CONTROL_PLANE_GRPC_PROXY_PORT}:10081' "$ROOT_DIR/k3d-config-control-plane.yaml"; then
  fail "control-plane k3d config must expose CONTROL_PLANE_GRPC_PROXY_PORT to Gateway port 10081"
fi
if ! grep -q '\${CONTROL_PLANE_GRPC_WORKER_PORT}:10086' "$ROOT_DIR/k3d-config-control-plane.yaml"; then
  fail "control-plane k3d config must expose CONTROL_PLANE_GRPC_WORKER_PORT to Gateway port 10086"
fi
if ! grep -q '10081:10081' "$ROOT_DIR/k3d-config.yaml"; then
  fail "single-cluster k3d config must expose the stack-owned grpc-gw TCP listener on host port 10081"
fi
if ! grep -q 'service-nvct-api.yaml' "$ROOT_DIR/apps/compute-control-plane-endpoints/kustomization.yaml"; then
  fail "compute control-plane endpoint aliases must include the NVCT API Service"
fi
if ! grep -q 'targetPort: grpc' "$ROOT_DIR/apps/compute-control-plane-endpoints/service-nvct-api.yaml"; then
  fail "compute NVCT API alias Service must expose the worker gRPC port"
fi
if ! grep -q 'service-grpc.yaml' "$ROOT_DIR/apps/compute-control-plane-endpoints/kustomization.yaml"; then
  fail "compute control-plane endpoint aliases must include the grpc-proxy worker Service"
fi
if ! grep -q 'targetPort: worker-tcp' "$ROOT_DIR/apps/compute-control-plane-endpoints/service-grpc.yaml"; then
  fail "compute grpc-proxy alias Service must expose the worker TCP port"
fi

if ! grep -q 'name: nats' "$ROOT_DIR/apps/envoy-gateway/gateway.yaml"; then
  fail "control-plane Gateway must define a nats TCP listener"
fi
if ! grep -R -q 'name: api-grpc-gw' "$ROOT_DIR/apps/envoy-gateway"; then
  fail "control-plane Gateway must define a worker-facing API gRPC listener"
fi
if ! grep -R -q 'protocol: HTTP' "$ROOT_DIR/apps/envoy-gateway/gateway-grpc.yaml"; then
  fail "worker-facing API gRPC Gateway must use HTTP protocol for GRPCRoute"
fi
if ! grep -R -q 'name: grpc-gw' "$ROOT_DIR/apps/envoy-gateway"; then
  fail "control-plane Gateway must define the stack-owned grpc-gw TCP listener"
fi
if ! grep -R -q 'name: worker-tcp' "$ROOT_DIR/apps/envoy-gateway/gateway-grpc.yaml"; then
  fail "control-plane Gateway must define the grpc-proxy worker TCP listener"
fi
if ! grep -q 'kubectl apply -k .*apps/envoy-gateway' "$ROOT_DIR/scripts/setup-gateway-api.sh"; then
  fail "gateway setup must apply the full envoy-gateway kustomization"
fi

if grep -R -q 'type: ExternalName' "$ROOT_DIR/apps/compute-control-plane-endpoints"; then
  fail "compute control-plane endpoint aliases must support port translation for custom control-plane host ports"
fi

if ! grep -q 'CONTROL_PLANE_CLUSTER_NAME' "$ROOT_DIR/scripts/configure-control-plane-endpoints.sh"; then
  fail "compute control-plane endpoint configuration must know the control-plane cluster name"
fi
if ! grep -q 'docker network connect' "$ROOT_DIR/scripts/configure-control-plane-endpoints.sh"; then
  fail "compute control-plane endpoint configuration must connect the control-plane load balancer to the compute Docker network"
fi
if ! grep -q 'deploy-compute-control-plane-endpoints configure-compute-control-plane-dns' "$ROOT_DIR/Makefile"; then
  fail "compute DNS must be configured after alias Services exist"
fi
if ! grep -q 'CONTROL_PLANE_LB_HTTP_PORT=80' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass the control-plane HTTP container port to endpoint configuration"
fi
if ! grep -q 'CONTROL_PLANE_LB_GRPC_PORT=9090' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass the control-plane gRPC container port to endpoint configuration"
fi
if ! grep -q 'CONTROL_PLANE_GRPC_PROXY_PORT ?= 10081' "$ROOT_DIR/Makefile"; then
  fail "Makefile must define the host port for the stack-owned grpc-gw TCP listener"
fi
if ! grep -q 'CONTROL_PLANE_GRPC_PROXY_PORT="$(CONTROL_PLANE_GRPC_PROXY_PORT)"' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass CONTROL_PLANE_GRPC_PROXY_PORT to the control-plane k3d config"
fi
if ! grep -q 'CONTROL_PLANE_GRPC_WORKER_PORT ?= 10086' "$ROOT_DIR/Makefile"; then
  fail "Makefile must define the host port for the grpc-proxy worker TCP listener"
fi
if ! grep -q 'CONTROL_PLANE_GRPC_WORKER_PORT="$(CONTROL_PLANE_GRPC_WORKER_PORT)"' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass CONTROL_PLANE_GRPC_WORKER_PORT to the control-plane k3d config"
fi
if ! grep -q 'CONTROL_PLANE_LB_NATS_PORT=4222' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass the control-plane NATS container port to endpoint configuration"
fi
if ! grep -q 'CONTROL_PLANE_LB_GRPC_WORKER_PORT=10086' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass the grpc-proxy worker container port to endpoint configuration"
fi
dns_target="$(awk '/^configure-compute-control-plane-dns:/{show=1} /^deploy-compute-control-plane-endpoints:/{show=0} show{print}' "$ROOT_DIR/Makefile")"
if grep -q 'CLUSTER_NAME' <<<"$dns_target"; then
  fail "compute DNS configuration must not pass unused CLUSTER_NAME"
fi

for unsupported_alias in 'api.${domain}' 'api-keys.${domain}'; do
  if grep -q "$unsupported_alias" "$ROOT_DIR/scripts/configure-control-plane-dns.sh"; then
    fail "compute DNS must not advertise unsupported control-plane alias '${unsupported_alias}'"
  fi
done

custom_domain="control-plane.dev.test"

rendered_routes="$(CONTROL_PLANE_DOMAIN="$custom_domain" "$ROOT_DIR/scripts/render-control-plane-endpoints.sh")"
if ! grep -q "sis.${custom_domain}" <<<"$rendered_routes"; then
  fail "control-plane SIS route must use CONTROL_PLANE_DOMAIN"
fi
if ! grep -q "reval.${custom_domain}" <<<"$rendered_routes"; then
  fail "control-plane ReVal route must use CONTROL_PLANE_DOMAIN"
fi
if ! grep -q "ess-api.ess.svc.cluster.local" <<<"$rendered_routes"; then
  fail "control-plane ESS route must advertise the service DNS hostname used by workers"
fi
if ! grep -q "api.nvcf.svc.cluster.local" <<<"$rendered_routes"; then
  fail "control-plane API route must advertise the in-cluster API hostname used by workers"
fi
if ! grep -q "invocation.nvcf.svc.cluster.local" <<<"$rendered_routes"; then
  fail "control-plane invocation route must advertise the service DNS hostname used by workers"
fi
if ! grep -q "nvct-api.nvcf.svc.cluster.local" <<<"$rendered_routes"; then
  fail "control-plane NVCT route must advertise the service DNS hostname used by task workers"
fi
if grep -q "ess.${custom_domain}" <<<"$rendered_routes"; then
  fail "control-plane ESS route must not add a topology-specific .test hostname"
fi
if grep -q "invocation.${custom_domain}" <<<"$rendered_routes"; then
  fail "control-plane invocation route must not add a topology-specific .test hostname"
fi
if ! grep -q "nvcf-api-control-plane-grpc" <<<"$rendered_routes"; then
  fail "control-plane API gRPC route must expose the in-cluster API gRPC port used by workers"
fi
if ! grep -q "nvct-api-control-plane-grpc" <<<"$rendered_routes"; then
  fail "control-plane NVCT gRPC route must expose the in-cluster NVCT gRPC port used by task workers"
fi
if ! grep -q "kind: GRPCRoute" <<<"$rendered_routes"; then
  fail "control-plane API gRPC route must use GRPCRoute for worker gRPC clients"
fi
if ! grep -A12 "nvcf-api-control-plane-grpc" <<<"$rendered_routes" | grep -q "sectionName: http"; then
  fail "control-plane API gRPC route must attach to the HTTP listener"
fi
if ! grep -A14 "nvct-api-control-plane-grpc" <<<"$rendered_routes" | grep -q "sectionName: http"; then
  fail "control-plane NVCT gRPC route must attach to the HTTP listener"
fi
if grep -A4 'name: nats' <<<"$rendered_routes" | grep -q "kind: TCPRoute"; then
  fail "ncp-local must not render the NATS TCPRoute; nvcf-gateway-routes owns that route"
fi
if grep -q "sis.nvcf-control-plane.test" <<<"$rendered_routes"; then
  fail "custom control-plane routes must not keep the default domain"
fi

endpoints_yaml="$(CONTROL_PLANE_DOMAIN="$custom_domain" CONTROL_PLANE_LB_IP=172.18.0.7 CONTROL_PLANE_LB_HTTP_PORT=18080 CONTROL_PLANE_LB_GRPC_PORT=19090 CONTROL_PLANE_LB_GRPC_WORKER_PORT=20086 CONTROL_PLANE_LB_NATS_PORT=14222 CLUSTER_NAME=ncp-local-compute-1 "$ROOT_DIR/scripts/configure-control-plane-endpoints.sh" --dry-run)"
if ! grep -q "ip: 172.18.0.7" <<<"$endpoints_yaml"; then
  fail "compute endpoint dry-run must point at the control-plane load balancer container IP"
fi
if ! grep -q "port: 18080" <<<"$endpoints_yaml"; then
  fail "compute HTTP endpoint dry-run must use CONTROL_PLANE_LB_HTTP_PORT"
fi
if ! grep -A12 "name: nvct-api" <<<"$endpoints_yaml" | grep -q "port: 18080"; then
  fail "compute NVCT endpoint dry-run must use CONTROL_PLANE_LB_HTTP_PORT"
fi
if ! grep -A16 "name: nvct-api" <<<"$endpoints_yaml" | grep -q "port: 19090"; then
  fail "compute NVCT endpoint dry-run must use CONTROL_PLANE_LB_GRPC_PORT"
fi
if ! grep -q "port: 19090" <<<"$endpoints_yaml"; then
  fail "compute gRPC endpoint dry-run must use CONTROL_PLANE_LB_GRPC_PORT"
fi
if ! grep -A10 "name: grpc" <<<"$endpoints_yaml" | grep -q "port: 20086"; then
  fail "compute grpc-proxy worker endpoint dry-run must use CONTROL_PLANE_LB_GRPC_WORKER_PORT"
fi
if ! grep -q "port: 14222" <<<"$endpoints_yaml"; then
  fail "compute NATS endpoint dry-run must use CONTROL_PLANE_LB_NATS_PORT"
fi

fake_bin="$(mktemp -d)"
cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker must not be called during endpoint dry-run without CONTROL_PLANE_LB_IP" >&2
exit 42
EOF
chmod +x "$fake_bin/docker"
PATH="$fake_bin:$PATH" CONTROL_PLANE_DOMAIN="$custom_domain" "$ROOT_DIR/scripts/configure-control-plane-endpoints.sh" --dry-run >/dev/null
rm -rf "$fake_bin"

dns_yaml="$(CONTROL_PLANE_DOMAIN="$custom_domain" CONTROL_PLANE_SIS_SERVICE_IP=10.43.174.3 CONTROL_PLANE_REVAL_SERVICE_IP=10.43.201.160 CONTROL_PLANE_NATS_SERVICE_IP=10.43.194.180 CLUSTER_NAME=ncp-local-compute-1 "$ROOT_DIR/scripts/configure-control-plane-dns.sh" --dry-run)"
for hostname in "sis.${custom_domain}" "reval.${custom_domain}" "nats.${custom_domain}"; do
  if ! grep -q "$hostname" <<<"$dns_yaml"; then
    fail "compute DNS dry-run missing '${hostname}'"
  fi
done
for service_ip in 10.43.174.3 10.43.201.160 10.43.194.180; do
  if ! grep -q "$service_ip" <<<"$dns_yaml"; then
    fail "compute DNS dry-run missing alias service IP '${service_ip}'"
  fi
done
if grep -q "172.18.0.1" <<<"$dns_yaml"; then
  fail "compute DNS dry-run must not point control-plane hostnames at the Docker gateway"
fi
for unsupported_alias in "api.${custom_domain}" "api-keys.${custom_domain}" "ess.${custom_domain}" "invocation.${custom_domain}"; do
  if grep -q "$unsupported_alias" <<<"$dns_yaml"; then
    fail "compute DNS must not advertise unsupported alias '${unsupported_alias}'"
  fi
done

if ! grep -q 'SAMPLE_IMAGE' "$ROOT_DIR/Makefile"; then
  fail "Makefile must expose SAMPLE_IMAGE for sample image configuration"
fi
if ! grep -q 'scripts/render-sample.sh' "$ROOT_DIR/Makefile"; then
  fail "deploy-sample must render the sample image from Makefile inputs"
fi

default_sample_yaml="$(SAMPLE_IMAGE=registry.k8s.io/e2e-test-images/agnhost:2.53 "$ROOT_DIR/scripts/render-sample.sh" --dry-run)"
if ! grep -q 'image: registry.k8s.io/e2e-test-images/agnhost:2.53' <<<"$default_sample_yaml"; then
  fail "sample render must use the default agnhost image"
fi
if ! grep -q 'netexec' <<<"$default_sample_yaml"; then
  fail "default sample render must run agnhost netexec"
fi

custom_sample_yaml="$(SAMPLE_IMAGE=registry.example.com/custom/team/image:v1 "$ROOT_DIR/scripts/render-sample.sh" --dry-run)"
if ! grep -q 'image: registry.example.com/custom/team/image:v1' <<<"$custom_sample_yaml"; then
  fail "sample render must allow SAMPLE_IMAGE overrides"
fi

digest_sample_yaml="$(SAMPLE_IMAGE=registry.example.com/custom/team/image@sha256:deadbeef "$ROOT_DIR/scripts/render-sample.sh" --dry-run)"
if ! grep -q 'image: registry.example.com/custom/team/image@sha256:deadbeef' <<<"$digest_sample_yaml"; then
  fail "sample render must preserve digest-pinned SAMPLE_IMAGE references"
fi

echo "PASS: multicluster Makefile dry tests"
