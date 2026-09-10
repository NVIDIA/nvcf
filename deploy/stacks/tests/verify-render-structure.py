#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import pathlib
import re
import sys

import yaml


DNS_LABEL_RE = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
GLUED_SEPARATOR_RE = re.compile(
    r"[^ \t\r\n-]---[ \t]*$|^[ \t]*---[^ \t\r\n#-]",
    re.MULTILINE,
)
MAX_METADATA_NAME_LENGTH = 253
MAX_NAMESPACE_LENGTH = 63
MAX_KYVERNO_RULE_NAME_LENGTH = 63


def fail(message):
    print(f"verify-render-structure: {message}", file=sys.stderr)
    sys.exit(1)


def object_id(doc):
    metadata = doc.get("metadata") if isinstance(doc.get("metadata"), dict) else {}
    return f"{doc.get('kind', '<missing-kind>')}/{metadata.get('name', '<missing-name>')}"


def format_path(path):
    out = "$"
    for item in path:
        if isinstance(item, int):
            out += f"[{item}]"
        else:
            out += f".{item}"
    return out


def walk(node, path):
    if isinstance(node, dict):
        for key, value in node.items():
            child_path = path + [str(key)]
            yield child_path, str(key), value
            yield from walk(value, child_path)
    elif isinstance(node, list):
        for index, value in enumerate(node):
            yield from walk(value, path + [index])


def check_namespace_value(value, where, errors):
    if not isinstance(value, str):
        return
    if len(value) > MAX_NAMESPACE_LENGTH:
        errors.append(f"{where} exceeds {MAX_NAMESPACE_LENGTH} characters: {value}")
    if not DNS_LABEL_RE.match(value):
        errors.append(f"{where} is not a valid Kubernetes namespace name: {value}")


def check_document(path, doc_index, doc, errors):
    location = f"{path}: document {doc_index}"
    if doc is None:
        return 0
    if not isinstance(doc, dict):
        errors.append(f"{location} is not a Kubernetes object map")
        return 0

    metadata = doc.get("metadata")
    if not isinstance(metadata, dict):
        metadata = {}

    missing = []
    if doc.get("apiVersion") is None:
        missing.append("apiVersion")
    if doc.get("kind") is None:
        missing.append("kind")
    if metadata.get("name") is None:
        missing.append("metadata.name")
    if missing:
        errors.append(f"{location} missing {', '.join(missing)} in {object_id(doc)}")

    metadata_name = metadata.get("name")
    if isinstance(metadata_name, str) and len(metadata_name) > MAX_METADATA_NAME_LENGTH:
        errors.append(
            f"{location} {object_id(doc)} metadata.name exceeds "
            f"{MAX_METADATA_NAME_LENGTH} characters"
        )

    if doc.get("kind") == "Namespace":
        check_namespace_value(metadata_name, f"{location} Namespace/{metadata_name} metadata.name", errors)

    metadata_namespace = metadata.get("namespace")
    check_namespace_value(
        metadata_namespace,
        f"{location} {object_id(doc)} metadata.namespace",
        errors,
    )

    for field_path, key, value in walk(doc, []):
        if key == "namespace":
            check_namespace_value(
                value,
                f"{location} {object_id(doc)} {format_path(field_path)}",
                errors,
            )

    if doc.get("kind") in {"Policy", "ClusterPolicy"}:
        for index, rule in enumerate(((doc.get("spec") or {}).get("rules") or [])):
            if not isinstance(rule, dict):
                continue
            rule_name = rule.get("name")
            if isinstance(rule_name, str) and len(rule_name) > MAX_KYVERNO_RULE_NAME_LENGTH:
                errors.append(
                    f"{location} {object_id(doc)} spec.rules[{index}].name exceeds "
                    f"{MAX_KYVERNO_RULE_NAME_LENGTH} characters: {rule_name}"
                )

    return 1


def is_manifest_file(path):
    if path.suffix not in {".yaml", ".yml"}:
        return False
    return not path.name.endswith(("-values.yaml", "-values.yml"))


def main():
    if len(sys.argv) != 2:
        fail("usage: verify-render-structure.py <rendered manifest directory>")

    manifest_dir = pathlib.Path(sys.argv[1])
    if not manifest_dir.is_dir():
        fail(f"{manifest_dir} does not exist")

    manifest_files = sorted(
        path
        for path in manifest_dir.rglob("*")
        if path.is_file() and is_manifest_file(path)
    )
    if not manifest_files:
        fail(f"no YAML manifests found under {manifest_dir}")

    errors = []
    object_count = 0
    for path in manifest_files:
        rel = path.relative_to(manifest_dir).as_posix()
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError as exc:
            errors.append(f"{rel}: cannot read as UTF-8: {exc}")
            continue

        if GLUED_SEPARATOR_RE.search(text):
            errors.append(f"{rel}: rendered output contains glued YAML separators")

        try:
            docs = list(yaml.safe_load_all(text))
        except Exception as exc:
            errors.append(f"{rel}: YAML parse failed: {exc}")
            continue

        for doc_index, doc in enumerate(docs, start=1):
            object_count += check_document(rel, doc_index, doc, errors)

    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        fail("rendered manifests failed structural checks")

    print(f"verify-render-structure: all checks passed ({object_count} objects)")


if __name__ == "__main__":
    main()
