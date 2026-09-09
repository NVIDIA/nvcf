#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

manifest_dir="${1:?rendered manifest directory is required}"
owner_label="nvcf.nvidia.com/control-plane-owner"
control_plane_owner="${NVCF_CONTROL_PLANE_OWNER:-default}"

fail() {
  echo "verify-control-plane-owner-label-placement: $*" >&2
  exit 1
}

command -v python3 >/dev/null 2>&1 || fail "python3 is required"
test -d "$manifest_dir" || fail "$manifest_dir does not exist"

python3 - "$manifest_dir" "$owner_label" "$control_plane_owner" <<'PY' || fail "rendered manifests failed owner-label placement checks"
import pathlib
import sys

import yaml

manifest_dir = pathlib.Path(sys.argv[1])
owner_label = sys.argv[2]
control_plane_owner = sys.argv[3]
manifest_files = sorted(
    path
    for path in manifest_dir.rglob("*")
    if path.is_file() and path.suffix in {".yaml", ".yml"}
)

if not manifest_files:
    print(f"no YAML manifests found under {manifest_dir}", file=sys.stderr)
    sys.exit(1)

errors = []


def object_id(doc):
    metadata = doc.get("metadata")
    name = metadata.get("name") if isinstance(metadata, dict) else "<missing-name>"
    return f"{doc.get('kind', '<missing-kind>')}/{name}"


def expected_owner(path):
    rel = path.relative_to(manifest_dir).as_posix()
    if rel.startswith("01-dependencies.yaml-") or "/01-dependencies.yaml-" in rel:
        return "shared"
    return control_plane_owner


def leak_paths(node, path):
    if isinstance(node, dict):
        if path and path[-1] in {"selector", "namespaceSelector", "objectSelector", "matchLabels"}:
            if owner_label in node:
                yield "." + ".".join(path + [owner_label])
        if len(path) >= 3 and path[-3:] == ["template", "metadata", "labels"]:
            if owner_label in node:
                yield "." + ".".join(path + [owner_label])
        for key, value in node.items():
            yield from leak_paths(value, path + [str(key)])
    elif isinstance(node, list):
        for index, value in enumerate(node):
            yield from leak_paths(value, path + [str(index)])


for path in manifest_files:
    owner = expected_owner(path)
    try:
        docs = list(yaml.safe_load_all(path.read_text()))
    except Exception as exc:
        errors.append(f"{path}: YAML parse failed: {exc}")
        continue

    for doc in docs:
        if not isinstance(doc, dict):
            continue
        metadata = doc.get("metadata")
        if doc.get("apiVersion") is None or doc.get("kind") is None or not isinstance(metadata, dict):
            continue
        if metadata.get("name") is None:
            rel = path.relative_to(manifest_dir)
            errors.append(f"{rel}: {doc.get('kind', '<missing-kind>')} is missing metadata.name")
            continue

        labels = metadata.get("labels") if isinstance(metadata.get("labels"), dict) else {}
        actual_owner = labels.get(owner_label)
        rel = path.relative_to(manifest_dir)
        oid = object_id(doc)

        if actual_owner is None:
            errors.append(f"{rel}: {oid} is missing {owner_label}")
        elif actual_owner != owner:
            errors.append(f"{rel}: {oid} has owner {actual_owner}, expected {owner}")

        for leak_path in leak_paths(doc, []):
            errors.append(f"{rel}: {oid} carries {owner_label} in {leak_path}")

        if doc.get("kind") == "Deployment" and metadata.get("name") == "nvca-operator":
            pod_spec = (
                doc.get("spec", {})
                .get("template", {})
                .get("spec", {})
            )
            containers = pod_spec.get("containers", [])
            for container_name in ("nvca-operator", "nvca-mirror"):
                matches = [
                    container
                    for container in containers
                    if isinstance(container, dict) and container.get("name") == container_name
                ]
                if len(matches) != 1:
                    errors.append(f"{rel}: {oid} missing container {container_name}")
                    continue
                env = matches[0].get("env", [])
                values = [
                    item.get("value")
                    for item in env
                    if isinstance(item, dict) and item.get("name") == "NVCF_CONTROL_PLANE_OWNER"
                ]
                if values != [owner]:
                    errors.append(
                        f"{rel}: {oid} container {container_name} expected "
                        f"NVCF_CONTROL_PLANE_OWNER={owner}, got {values}"
                    )

if errors:
    for error in errors:
        print(error, file=sys.stderr)
    sys.exit(1)
PY

echo "verify-control-plane-owner-label-placement: all checks passed"
