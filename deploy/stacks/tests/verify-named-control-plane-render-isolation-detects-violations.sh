#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="$script_dir/verify-named-control-plane-render-isolation.py"
work_dir="$(mktemp -d)"
stdout="$work_dir/stdout"
stderr="$work_dir/stderr"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-named-control-plane-render-isolation-detects-violations: $*" >&2
  exit 1
}

run_checker() {
  python3 "$checker" "$@" >"$stdout" 2>"$stderr"
}

expect_pass() {
  local name="$1"
  shift

  if ! run_checker "$@"; then
    fail "checker rejected $name: $(cat "$stderr")"
  fi
}

expect_fail() {
  local name="$1"
  local expected="$2"
  shift 2

  if run_checker "$@"; then
    fail "checker accepted $name"
  fi
  if ! grep -Fq -- "$expected" "$stderr"; then
    fail "checker rejected $name without expected message '$expected': $(cat "$stderr")"
  fi
}

write_good_render() {
  local owner="$1"
  local dir="$2"
  local prefix="${owner}-"

  mkdir -p "$dir"
  cat >"$dir/good.yaml" <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${prefix}nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: ${prefix}nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
data:
  openbao: "http://${prefix}openbao.${prefix}vault-system.svc.cluster.local:8200"
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: ${prefix}nvcf-api
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
spec:
  parentRefs:
    - name: shared-gateway
      namespace: gateway
  rules:
    - backendRefs:
        - name: api
          namespace: ${prefix}nvcf
---
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: ${prefix}allow-routes-to-nvcf
  namespace: ${prefix}nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      namespace: gateway
  to:
    - group: ""
      kind: Service
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${prefix}nvcf-openbao-pki
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
spec: {}
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: ${prefix}openbao-agent-injector-cfg
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
webhooks:
  - name: ${owner}.vault.hashicorp.com
    clientConfig:
      service:
        name: ${prefix}openbao-agent-injector-svc
        namespace: ${prefix}vault-system
        path: /mutate
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${prefix}openbao-agent-injector-clusterrole
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
rules: []
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${prefix}openbao-agent-injector-binding
  labels:
    nvcf.nvidia.com/control-plane-owner: ${owner}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${prefix}openbao-agent-injector-clusterrole
subjects:
  - kind: ServiceAccount
    name: ${prefix}openbao-agent-injector
    namespace: ${prefix}vault-system
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: shared.example.com
  labels:
    nvcf.nvidia.com/control-plane-owner: shared
spec: {}
YAML
}

good_a="$work_dir/good-a"
good_b="$work_dir/good-b"
shared_crd_identity="CustomResourceDefinition/<cluster>/shared.example.com"
write_good_render plane-a "$good_a"
write_good_render plane-b "$good_b"

expect_pass "single good named render" \
  --render "plane-a=$good_a" \
  --allow-shared-identity "$shared_crd_identity"
expect_pass "two good named renders with shared CRD" \
  --render "plane-a=$good_a" \
  --render "plane-b=$good_b" \
  --allow-shared-identity "$shared_crd_identity"

wrong_owner="$work_dir/wrong-owner"
mkdir -p "$wrong_owner"
cat >"$wrong_owner/bad.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: plane-a-nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-b
YAML
expect_fail "wrong owner label" "has nvcf.nvidia.com/control-plane-owner=plane-b" \
  --render "plane-a=$wrong_owner"

unlisted_shared="$work_dir/unlisted-shared"
mkdir -p "$unlisted_shared"
cat >"$unlisted_shared/bad.yaml" <<'YAML'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: shared.example.com
  labels:
    nvcf.nvidia.com/control-plane-owner: shared
spec: {}
YAML
expect_fail "unlisted shared cluster-scoped object" "--allow-shared-identity" \
  --render "plane-a=$unlisted_shared"

legacy_namespace="$work_dir/legacy-namespace"
mkdir -p "$legacy_namespace"
cat >"$legacy_namespace/bad.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
YAML
expect_fail "legacy metadata.namespace" "references legacy plane-owned namespace nvcf" \
  --render "plane-a=$legacy_namespace"

legacy_route_backend="$work_dir/legacy-route-backend"
mkdir -p "$legacy_route_backend"
cat >"$legacy_route_backend/bad.yaml" <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: plane-a-nvcf-api
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
spec:
  rules:
    - backendRefs:
        - name: api
          namespace: nvcf
YAML
expect_fail "legacy route backend namespace" "references legacy plane-owned namespace nvcf" \
  --render "plane-a=$legacy_route_backend"

legacy_dns="$work_dir/legacy-dns"
mkdir -p "$legacy_dns"
cat >"$legacy_dns/bad.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: plane-a-nvcf
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
data:
  vault: "http://openbao-server.vault-system.svc.cluster.local:8200"
YAML
expect_fail "legacy service DNS" "service DNS openbao-server.vault-system.svc.cluster.local:8200" \
  --render "plane-a=$legacy_dns"

legacy_service_name_dns="$work_dir/legacy-service-name-dns"
mkdir -p "$legacy_service_name_dns"
cat >"$legacy_service_name_dns/bad.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: plane-a-vault-system
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
data:
  issuerServer: "http://openbao-server.plane-a-vault-system.svc.cluster.local:8200"
YAML
expect_fail "legacy service name in named namespace DNS" \
  "legacy plane-owned service name openbao-server" \
  --render "plane-a=$legacy_service_name_dns"

legacy_object_reference="$work_dir/legacy-object-reference"
mkdir -p "$legacy_object_reference"
cat >"$legacy_object_reference/bad.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: plane-a-vault-system
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
data:
  rootTokenSecretName: openbao-server-root-token
YAML
expect_fail "legacy object reference in named namespace" \
  "legacy plane-owned object reference openbao-server-root-token" \
  --render "plane-a=$legacy_object_reference"

legacy_subject_name="$work_dir/legacy-subject-name"
mkdir -p "$legacy_subject_name"
cat >"$legacy_subject_name/bad.yaml" <<'YAML'
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: nkey-bao-access
  namespace: plane-a-nats-system
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
subjects:
  - kind: ServiceAccount
    name: openbao-server-initialize-cluster
    namespace: plane-a-vault-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: nkey-reader
YAML
expect_fail "legacy subject name in named namespace" \
  "legacy plane-owned object reference openbao-server-initialize-cluster" \
  --render "plane-a=$legacy_subject_name"

legacy_route_name="$work_dir/legacy-route-name"
mkdir -p "$legacy_route_name"
cat >"$legacy_route_name/bad.yaml" <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: nvcf-api
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
spec: {}
YAML
expect_fail "unprefixed gateway route name" "gateway route in unprefixed namespace gateway" \
  --render "plane-a=$legacy_route_name"

custom_gateway_route="$work_dir/custom-gateway-route"
mkdir -p "$custom_gateway_route"
cat >"$custom_gateway_route/bad.yaml" <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: nvcf-api
  namespace: edge-gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
spec: {}
YAML
expect_fail "unprefixed route name in custom gateway namespace" \
  "gateway route in unprefixed namespace edge-gateway" \
  --render "plane-a=$custom_gateway_route"

shared_label_route="$work_dir/shared-label-route"
mkdir -p "$shared_label_route"
cat >"$shared_label_route/bad.yaml" <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: nvcf-api
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: shared
spec: {}
YAML
expect_fail "shared-labelled gateway route without allowlist" \
  "has shared owner but gateway routes in unprefixed namespaces must be listed in --allow-shared-identity" \
  --render "plane-a=$shared_label_route"
expect_pass "shared-labelled gateway route with explicit allowlist" \
  --render "plane-a=$shared_label_route" \
  --allow-shared-identity HTTPRoute/gateway/nvcf-api

legacy_clusterissuer="$work_dir/legacy-clusterissuer"
mkdir -p "$legacy_clusterissuer"
cat >"$legacy_clusterissuer/bad.yaml" <<'YAML'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: nvcf-openbao-pki
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
spec: {}
YAML
expect_fail "legacy ClusterIssuer name" "cluster-scoped object in a named render" \
  --render "plane-a=$legacy_clusterissuer"

unknown_cluster_scoped="$work_dir/unknown-cluster-scoped"
mkdir -p "$unknown_cluster_scoped"
cat >"$unknown_cluster_scoped/bad.yaml" <<'YAML'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: custom-policy
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
webhooks: []
YAML
expect_fail "unprefixed unknown cluster-scoped singleton" \
  "cluster-scoped object in a named render" \
  --render "plane-a=$unknown_cluster_scoped"

legacy_webhook="$work_dir/legacy-webhook"
mkdir -p "$legacy_webhook"
cat >"$legacy_webhook/bad.yaml" <<'YAML'
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: openbao-server-agent-injector-cfg
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
webhooks: []
YAML
expect_fail "legacy OpenBao injector webhook name" \
  "cluster-scoped object in a named render" \
  --render "plane-a=$legacy_webhook"

legacy_role_ref="$work_dir/legacy-role-ref"
mkdir -p "$legacy_role_ref"
cat >"$legacy_role_ref/bad.yaml" <<'YAML'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: plane-a-openbao-server-agent-injector-binding
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: openbao-server-agent-injector-clusterrole
subjects: []
YAML
expect_fail "legacy OpenBao injector ClusterRoleBinding reference" \
  "roleRef.name references unprefixed ClusterRole openbao-server-agent-injector-clusterrole" \
  --render "plane-a=$legacy_role_ref"

collision_a="$work_dir/collision-a"
collision_b="$work_dir/collision-b"
mkdir -p "$collision_a" "$collision_b"
cat >"$collision_a/collision.yaml" <<'YAML'
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: shared-collision
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-a
webhooks: []
YAML
cat >"$collision_b/collision.yaml" <<'YAML'
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: shared-collision
  labels:
    nvcf.nvidia.com/control-plane-owner: plane-b
webhooks: []
YAML
expect_fail "two named renders with same object identity" "appears in multiple named renders" \
  --render "plane-a=$collision_a" \
  --render "plane-b=$collision_b"

shared_collision_a="$work_dir/shared-collision-a"
shared_collision_b="$work_dir/shared-collision-b"
shared_identity_file="$work_dir/shared-identities.txt"
mkdir -p "$shared_collision_a" "$shared_collision_b"
cat >"$shared_collision_a/collision.yaml" <<'YAML'
apiVersion: v1
kind: Service
metadata:
  name: collide
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: shared
spec:
  ports:
    - port: 80
YAML
cat >"$shared_collision_b/collision.yaml" <<'YAML'
apiVersion: v1
kind: Service
metadata:
  name: collide
  namespace: gateway
  labels:
    nvcf.nvidia.com/control-plane-owner: shared
spec:
  ports:
    - port: 80
YAML
expect_fail "two shared-labelled renders with same object identity but no allowlist" \
  "appears in multiple named renders" \
  --render "plane-a=$shared_collision_a" \
  --render "plane-b=$shared_collision_b"
cat >"$shared_identity_file" <<'EOF'
Service/gateway
EOF
expect_fail "malformed shared identity file line" \
  "shared identity must use KIND/NAMESPACE/NAME" \
  --render "plane-a=$good_a" \
  --allow-shared-identity-file "$shared_identity_file"
cat >"$shared_identity_file" <<'EOF'
# Shared gateway service owned outside a single control plane.
Service/gateway/collide
EOF
expect_pass "two shared-labelled renders with explicit identity file allowlist" \
  --render "plane-a=$shared_collision_a" \
  --render "plane-b=$shared_collision_b" \
  --allow-shared-identity-file "$shared_identity_file"

echo "verify-named-control-plane-render-isolation-detects-violations: all checks passed"
