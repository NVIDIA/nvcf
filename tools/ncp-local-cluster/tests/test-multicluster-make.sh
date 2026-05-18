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

print_directory_clusters="$(MAKEFLAGS=--print-directory; export MAKEFLAGS; run_make print-compute-clusters)"
assert_eq "ncp-local-compute-1" "$print_directory_clusters" "print-directory compute cluster output"

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

if ! grep -q 'name: nats' "$ROOT_DIR/apps/envoy-gateway/gateway.yaml"; then
  fail "control-plane Gateway must define a nats TCP listener"
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
if ! grep -q 'CONTROL_PLANE_LB_HTTP_PORT="$(CONTROL_PLANE_HTTP_PORT)"' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass CONTROL_PLANE_HTTP_PORT as CONTROL_PLANE_LB_HTTP_PORT to endpoint configuration"
fi
if ! grep -q 'CONTROL_PLANE_LB_NATS_PORT="$(CONTROL_PLANE_NATS_PORT)"' "$ROOT_DIR/Makefile"; then
  fail "Makefile must pass CONTROL_PLANE_NATS_PORT as CONTROL_PLANE_LB_NATS_PORT to endpoint configuration"
fi
dns_target="$(awk '/^configure-compute-control-plane-dns:/{show=1} /^deploy-compute-control-plane-endpoints:/{show=0} show{print}' "$ROOT_DIR/Makefile")"
if grep -q 'CLUSTER_NAME' <<<"$dns_target"; then
  fail "compute DNS configuration must not pass unused CLUSTER_NAME"
fi

for unsupported_alias in 'api.${domain}' 'api-keys.${domain}' 'invocation.${domain}'; do
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
if grep -q "kind: TCPRoute" <<<"$rendered_routes"; then
  fail "ncp-local must not render the NATS TCPRoute; nvcf-gateway-routes owns that route"
fi
if grep -q "sis.nvcf-control-plane.test" <<<"$rendered_routes"; then
  fail "custom control-plane routes must not keep the default domain"
fi

endpoints_yaml="$(CONTROL_PLANE_DOMAIN="$custom_domain" CONTROL_PLANE_LB_IP=172.18.0.7 CONTROL_PLANE_LB_HTTP_PORT=18080 CONTROL_PLANE_LB_NATS_PORT=14222 CLUSTER_NAME=ncp-local-compute-1 "$ROOT_DIR/scripts/configure-control-plane-endpoints.sh" --dry-run)"
if ! grep -q "ip: 172.18.0.7" <<<"$endpoints_yaml"; then
  fail "compute endpoint dry-run must point at the control-plane load balancer container IP"
fi
if ! grep -q "port: 18080" <<<"$endpoints_yaml"; then
  fail "compute HTTP endpoint dry-run must use CONTROL_PLANE_LB_HTTP_PORT"
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
for unsupported_alias in "api.${custom_domain}" "api-keys.${custom_domain}" "invocation.${custom_domain}"; do
  if grep -q "$unsupported_alias" <<<"$dns_yaml"; then
    fail "compute DNS must not advertise unsupported alias '${unsupported_alias}'"
  fi
done

if ! grep -q 'SAMPLE_NGC_ORG' "$ROOT_DIR/Makefile"; then
  fail "Makefile must expose SAMPLE_NGC_ORG for sample image configuration"
fi
if ! grep -q 'scripts/render-sample.sh' "$ROOT_DIR/Makefile"; then
  fail "deploy-sample must render the sample image from Makefile inputs"
fi

tmp_output="$(mktemp)"
if "$ROOT_DIR/scripts/render-sample.sh" --dry-run >"$tmp_output" 2>&1; then
  cat "$tmp_output" >&2
  rm -f "$tmp_output"
  fail "sample render must fail when ngc-org/ngc-team placeholders are not replaced"
fi
if grep -q 'Internal example' "$tmp_output"; then
  cat "$tmp_output" >&2
  rm -f "$tmp_output"
  fail "sample render placeholder error must not include internal examples"
fi
if ! grep -q 'SAMPLE_NGC_ORG=my-org SAMPLE_NGC_TEAM=my-team' "$tmp_output"; then
  cat "$tmp_output" >&2
  rm -f "$tmp_output"
  fail "sample render placeholder error must include a generic example"
fi
rm -f "$tmp_output"

sample_yaml="$(SAMPLE_NGC_ORG=my-org SAMPLE_NGC_TEAM=my-team "$ROOT_DIR/scripts/render-sample.sh" --dry-run)"
if ! grep -q 'image: nvcr.io/my-org/my-team/alpine-k8s:1.30.12' <<<"$sample_yaml"; then
  fail "sample render must use SAMPLE_NGC_ORG and SAMPLE_NGC_TEAM overrides"
fi

custom_sample_yaml="$(SAMPLE_IMAGE=registry.example.com/custom/team/image SAMPLE_IMAGE_TAG=v1 "$ROOT_DIR/scripts/render-sample.sh" --dry-run)"
if ! grep -q 'image: registry.example.com/custom/team/image:v1' <<<"$custom_sample_yaml"; then
  fail "sample render must allow SAMPLE_IMAGE to override the NGC path"
fi

echo "PASS: multicluster Makefile dry tests"
