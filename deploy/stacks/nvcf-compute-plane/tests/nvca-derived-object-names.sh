#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
chart_dir="$repo_dir/src/compute-plane-services/nvca/deployments/nvca-operator"
helmfile_path="$stack_dir/helmfile.d/02-nvca.yaml.gotmpl"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nvca-derived-object-names: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"

grep -Fq '{{- $nvcaOperatorName := printf "%snvca-operator" $controlPlaneNamespacePrefix }}' "$helmfile_path" ||
  fail "compute-plane helmfile does not define the derived nvca-operator name"
grep -Fq 'fullnameOverride: {{ $nvcaOperatorName | quote }}' "$helmfile_path" ||
  fail "compute-plane helmfile does not pass the derived name to the nvca-operator chart"

manifest="$work_dir/nvca-operator.yaml"
release_name="nvca-operator"
plane_name="plane-a-nvca-operator"
plane_namespace="plane-a-nvca-operator"
cleanup_name="${plane_name}-pre-delete-cleanup"

helm template "$release_name" "$chart_dir" \
  --namespace "$plane_namespace" \
  --show-only templates/pre-delete-cleanup-rbac.yaml \
  --show-only templates/pre-delete-cleanup-job.yaml \
  --set-string fullnameOverride="$plane_name" \
  --set generateImagePullSecret=false \
  --set-string imagePullSecretName=dummy \
  --set-string image.repository=example.com/nvca-operator \
  --set-string logLevel=info \
  >"$manifest"

python3 - "$manifest" "$cleanup_name" "$plane_name" "$plane_namespace" <<'PY' ||
import sys

import yaml

manifest_path, cleanup_name, plane_name, plane_namespace = sys.argv[1:]

with open(manifest_path, "r", encoding="utf-8") as handle:
    docs = [doc for doc in yaml.safe_load_all(handle) if isinstance(doc, dict)]

errors = []


def describe(doc):
    metadata = doc.get("metadata") or {}
    return f"{doc.get('kind')}/{metadata.get('name')}"


def expect(condition, message):
    if not condition:
        errors.append(message)


by_kind = {
    doc.get("kind"): doc
    for doc in docs
    if (doc.get("metadata") or {}).get("name") == cleanup_name
}

for kind in ("ServiceAccount", "ClusterRole", "ClusterRoleBinding", "Job"):
    expect(kind in by_kind, f"missing {kind}/{cleanup_name}")

for doc in docs:
    metadata = doc.get("metadata") or {}
    if metadata.get("name") == "nvca-operator-pre-delete-cleanup":
        errors.append(f"{describe(doc)} still uses the legacy cleanup hook name")

service_account = by_kind.get("ServiceAccount")
if service_account:
    metadata = service_account.get("metadata") or {}
    expect(metadata.get("namespace") == plane_namespace, "cleanup ServiceAccount namespace is not plane-derived")

binding = by_kind.get("ClusterRoleBinding")
if binding:
    role_ref = binding.get("roleRef") or {}
    expect(role_ref.get("name") == cleanup_name, "ClusterRoleBinding roleRef does not use the derived cleanup name")
    subjects = binding.get("subjects") or []
    expect(len(subjects) == 1, "ClusterRoleBinding should have one ServiceAccount subject")
    if subjects:
        subject = subjects[0]
        expect(subject.get("name") == cleanup_name, "ClusterRoleBinding subject does not use the derived cleanup name")
        expect(subject.get("namespace") == plane_namespace, "ClusterRoleBinding subject namespace is not plane-derived")

job = by_kind.get("Job")
if job:
    pod_spec = (((job.get("spec") or {}).get("template") or {}).get("spec") or {})
    expect(pod_spec.get("serviceAccountName") == cleanup_name, "cleanup Job serviceAccountName is not derived")
    containers = pod_spec.get("containers") or []
    expect(len(containers) == 1, "cleanup Job should have one container")
    args = containers[0].get("args") if containers else []

    def value_after(flag):
        try:
            return args[args.index(flag) + 1]
        except (ValueError, IndexError):
            errors.append(f"cleanup Job args are missing {flag}")
            return None

    expect(value_after("--namespace") == plane_namespace, "cleanup Job namespace arg is not plane-derived")
    expect(value_after("--cluster-role-name") == plane_name, "cleanup Job ClusterRole arg is not plane-derived")
    expect(value_after("--cluster-role-binding-name") == plane_name, "cleanup Job ClusterRoleBinding arg is not plane-derived")
    expect(value_after("--service-account-name") == plane_name, "cleanup Job ServiceAccount arg is not plane-derived")

if errors:
    for error in errors:
        print(error, file=sys.stderr)
    sys.exit(1)
PY
  fail "rendered cleanup hook names are not plane-derived"

echo "nvca-derived-object-names: all checks passed"
