#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root_dir/scripts/configure-isolated-control-plane-gateways.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

fake_bin="$tmpdir/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${KUBECTL_CALL_LOG:?}"
case " $* " in
  *" get gateways.gateway.networking.k8s.io "*)
    if [[ -n "${FAKE_GATEWAYS_JSON:-}" ]]; then
      printf '%s\n' "$FAKE_GATEWAYS_JSON"
    else
      printf '%s\n' '{"items":[]}'
    fi
    ;;
  *" get envoyproxies.gateway.envoyproxy.io "*)
    if [[ -n "${FAKE_ENVOY_PROXIES_JSON:-}" ]]; then
      printf '%s\n' "$FAKE_ENVOY_PROXIES_JSON"
    else
      printf '%s\n' '{"items":[]}'
    fi
    ;;
  *" apply -f - "*)
    cat >>"${KUBECTL_APPLY_LOG:?}"
    ;;
  *" wait "*)
    ;;
  *" delete gateway "*)
    ;;
  *" delete envoyproxy "*)
    ;;
  *)
    echo "unexpected kubectl arguments: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$fake_bin/kubectl"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render_plane() {
  local id="$1"
  local port_base="$2"
  CONTROL_PLANE_ID="$id" \
  SHARED_HTTP_PORT="$((port_base + 1))" \
  GRPC_API_PORT="$((port_base + 2))" \
  GRPC_WORKER_PORT="$((port_base + 3))" \
  NATS_PORT="$((port_base + 4))" \
    "$script" render
}

render_plane plane-a 18000 >"$tmpdir/a.yaml"
render_plane plane-b 19000 >"$tmpdir/b.yaml"

for plane in a b; do
  id="plane-$plane"
  manifest="$tmpdir/$plane.yaml"
  for gateway in shared-gw grpc-gw nats-gw; do
    grep -Fq "name: ${id}-${gateway}" "$manifest" ||
      fail "$id render missing ${id}-${gateway}"
  done
  grep -Fq "nvcf.nvidia.com/control-plane-id: ${id}" "$manifest" ||
    fail "$id render missing ownership label"
  ruby -ryaml -e '
    id = ARGV[1]
    docs = YAML.load_stream(File.read(ARGV[0])).compact
    gateways = docs.select { |doc| doc["kind"] == "Gateway" }
    proxies = docs.select { |doc| doc["kind"] == "EnvoyProxy" }
    abort "expected three EnvoyProxy resources" unless proxies.length == 3
    proxies.each do |proxy|
      service_name = proxy.dig("spec", "provider", "kubernetes", "envoyService", "name")
      abort "Envoy Service name is not explicit" if service_name.to_s.empty?
      abort "Envoy Service name exceeds k3s ServiceLB-safe length" if service_name.length > 47
      owner = proxy.dig("metadata", "labels", "nvcf.nvidia.com/control-plane-id")
      abort "EnvoyProxy owner #{owner.inspect} does not match #{id}" unless owner == id
    end
    gateways.each do |gateway|
      ref = gateway.dig("spec", "infrastructure", "parametersRef") || {}
      expected = gateway.dig("metadata", "name").sub(/-gw$/, "-proxy")
      abort "Gateway does not use EnvoyProxy #{expected}" unless
        ref["group"] == "gateway.envoyproxy.io" && ref["kind"] == "EnvoyProxy" && ref["name"] == expected
    end
    listeners = gateways.flat_map { |gateway| gateway.dig("spec", "listeners") || [] }
    abort "no Gateway listeners rendered" if listeners.empty?
    listeners.each do |listener|
      namespaces = listener.dig("allowedRoutes", "namespaces") || {}
      abort "listener does not use namespace Selector" unless namespaces["from"] == "Selector"
      actual = namespaces.dig("selector", "matchLabels", "nvcf.nvidia.com/control-plane-id")
      abort "listener selector #{actual.inspect} does not select #{id}" unless actual == id
    end
  ' "$manifest" "$id" || fail "$id render does not isolate Envoy resources and routes"
done

ruby -ryaml -e 'YAML.load_stream(File.read(ARGV[0])).compact.each { |doc| puts doc.dig("metadata", "name") if doc["kind"] == "Gateway" }' \
  "$tmpdir/a.yaml" | sort >"$tmpdir/a-names"
ruby -ryaml -e 'YAML.load_stream(File.read(ARGV[0])).compact.each { |doc| puts doc.dig("metadata", "name") if doc["kind"] == "Gateway" }' \
  "$tmpdir/b.yaml" | sort >"$tmpdir/b-names"
if comm -12 "$tmpdir/a-names" "$tmpdir/b-names" | grep -q .; then
  fail "plane A and B Gateway names collide"
fi

[[ "$(ruby -ryaml -e 'puts YAML.load_stream(File.read(ARGV[0])).compact.find { |doc| doc.dig("metadata", "name") == "plane-a-shared-gw" }.dig("spec", "listeners", 0, "port")' "$tmpdir/a.yaml")" == 18001 ]] ||
  fail "plane A shared HTTP port mismatch"
[[ "$(ruby -ryaml -e 'puts YAML.load_stream(File.read(ARGV[0])).compact.find { |doc| doc.dig("metadata", "name") == "plane-b-nats-gw" }.dig("spec", "listeners", 0, "port")' "$tmpdir/b.yaml")" == 19004 ]] ||
  fail "plane B NATS port mismatch"

if CONTROL_PLANE_ID=INVALID_ID SHARED_HTTP_PORT=18001 GRPC_API_PORT=18002 \
    GRPC_WORKER_PORT=18003 NATS_PORT=18004 "$script" render >/dev/null 2>&1; then
  fail "invalid control-plane ID was accepted"
fi
if CONTROL_PLANE_ID=plane-a SHARED_HTTP_PORT=18001 GRPC_API_PORT=18002 \
    GRPC_WORKER_PORT=18003 "$script" render >/dev/null 2>&1; then
  fail "missing NATS port was accepted"
fi

apply_plane() {
  PATH="$fake_bin:$PATH" \
  KUBECTL_CALL_LOG="$tmpdir/kubectl-calls" \
  KUBECTL_APPLY_LOG="$tmpdir/kubectl-apply" \
  CONTROL_PLANE_ID=plane-a \
  SHARED_HTTP_PORT=18001 \
  GRPC_API_PORT=18002 \
  GRPC_WORKER_PORT=18003 \
  NATS_PORT=18004 \
    "$script" apply
}

: >"$tmpdir/kubectl-calls"
: >"$tmpdir/kubectl-apply"
FAKE_GATEWAYS_JSON='{"items":[]}' apply_plane
[[ "$(grep -c '^wait ' "$tmpdir/kubectl-calls")" == 3 ]] ||
  fail "apply did not wait for all three Gateways to become Programmed"
grep -Fq 'get envoyproxies.gateway.envoyproxy.io' "$tmpdir/kubectl-calls" ||
  fail "apply did not preflight existing EnvoyProxy ownership"

foreign_owner='{"items":[{"metadata":{"name":"plane-a-shared-gw","labels":{"nvcf.nvidia.com/control-plane-id":"plane-b"}},"spec":{"listeners":[{"port":19001}]}}]}'
if FAKE_GATEWAYS_JSON="$foreign_owner" apply_plane >/dev/null 2>&1; then
  fail "apply accepted a desired Gateway owned by another control plane"
fi

foreign_proxy_owner='{"items":[{"metadata":{"name":"plane-a-shared-proxy","labels":{"nvcf.nvidia.com/control-plane-id":"plane-b"}}}]}'
if FAKE_GATEWAYS_JSON='{"items":[]}' FAKE_ENVOY_PROXIES_JSON="$foreign_proxy_owner" \
    apply_plane >/dev/null 2>&1; then
  fail "apply accepted a desired EnvoyProxy owned by another control plane"
fi

colliding_port='{"items":[{"metadata":{"name":"plane-b-shared-gw","labels":{"nvcf.nvidia.com/control-plane-id":"plane-b"}},"spec":{"listeners":[{"port":18001}]}}]}'
if FAKE_GATEWAYS_JSON="$colliding_port" apply_plane >/dev/null 2>&1; then
  fail "apply accepted a listener port already used by another control plane"
fi

foreign_proxy_reference='{"items":[{"metadata":{"name":"plane-b-shared-gw","labels":{"nvcf.nvidia.com/control-plane-id":"plane-b"}},"spec":{"infrastructure":{"parametersRef":{"group":"gateway.envoyproxy.io","kind":"EnvoyProxy","name":"plane-a-shared-proxy"}},"listeners":[{"port":19001}]}}]}'
: >"$tmpdir/kubectl-calls"
: >"$tmpdir/kubectl-apply"
if FAKE_GATEWAYS_JSON="$foreign_proxy_reference" apply_plane >/dev/null 2>&1; then
  fail "apply accepted a foreign Gateway coupled to this plane's EnvoyProxy"
fi
if grep -Eq '(^| )apply -f -($| )|(^| )delete (gateway|envoyproxy)($| )' "$tmpdir/kubectl-calls"; then
  fail "apply mutated resources after finding a foreign EnvoyProxy reference"
fi

: >"$tmpdir/kubectl-calls"
if FAKE_GATEWAYS_JSON="$foreign_proxy_reference" \
    PATH="$fake_bin:$PATH" \
    KUBECTL_CALL_LOG="$tmpdir/kubectl-calls" \
    KUBECTL_APPLY_LOG="$tmpdir/kubectl-apply" \
    CONTROL_PLANE_ID=plane-a \
    SHARED_HTTP_PORT=18001 \
    GRPC_API_PORT=18002 \
    GRPC_WORKER_PORT=18003 \
    NATS_PORT=18004 \
      "$script" delete >/dev/null 2>&1; then
  fail "delete accepted a foreign Gateway coupled to this plane's EnvoyProxy"
fi
if grep -Eq '(^| )apply -f -($| )|(^| )delete (gateway|envoyproxy)($| )' "$tmpdir/kubectl-calls"; then
  fail "delete mutated resources after finding a foreign EnvoyProxy reference"
fi

: >"$tmpdir/kubectl-calls"
FAKE_GATEWAYS_JSON='{"items":[{"metadata":{"name":"plane-a-shared-gw","labels":{"nvcf.nvidia.com/control-plane-id":"plane-a"}},"spec":{"listeners":[{"port":18001}]}}]}' \
  FAKE_ENVOY_PROXIES_JSON='{"items":[{"metadata":{"name":"plane-a-shared-proxy","labels":{"nvcf.nvidia.com/control-plane-id":"plane-a"}}}]}' \
  PATH="$fake_bin:$PATH" \
  KUBECTL_CALL_LOG="$tmpdir/kubectl-calls" \
  KUBECTL_APPLY_LOG="$tmpdir/kubectl-apply" \
  CONTROL_PLANE_ID=plane-a \
  SHARED_HTTP_PORT=18001 \
  GRPC_API_PORT=18002 \
  GRPC_WORKER_PORT=18003 \
  NATS_PORT=18004 \
    "$script" delete
grep -Fq 'delete gateway plane-a-shared-gw' "$tmpdir/kubectl-calls" ||
  fail "delete did not remove the owned Gateway"
grep -Fq 'delete envoyproxy plane-a-shared-proxy' "$tmpdir/kubectl-calls" ||
  fail "delete did not remove the owned EnvoyProxy"

if FAKE_GATEWAYS_JSON="$foreign_owner" \
    PATH="$fake_bin:$PATH" \
    KUBECTL_CALL_LOG="$tmpdir/kubectl-calls" \
    KUBECTL_APPLY_LOG="$tmpdir/kubectl-apply" \
    CONTROL_PLANE_ID=plane-a \
    SHARED_HTTP_PORT=18001 \
    GRPC_API_PORT=18002 \
    GRPC_WORKER_PORT=18003 \
    NATS_PORT=18004 \
      "$script" delete >/dev/null 2>&1; then
  fail "delete accepted a desired Gateway owned by another control plane"
fi

echo "Isolated control-plane Gateway lifecycle checks passed."
