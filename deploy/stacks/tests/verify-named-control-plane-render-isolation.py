#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import dataclasses
import pathlib
import re
import sys
from collections import defaultdict

import yaml


OWNER_LABEL = "nvcf.nvidia.com/control-plane-owner"
SHARED_OWNER = "shared"
RESERVED_OWNERS = {"default", SHARED_OWNER}
OWNER_RE = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
DNS_LABEL = r"[a-z0-9]([-a-z0-9]*[a-z0-9])?"
SERVICE_DNS_RE = re.compile(
    rf"(?P<service>{DNS_LABEL})\.(?P<namespace>{DNS_LABEL})\.svc"
    r"(\.cluster\.local)?(:[0-9]+)?"
)
LEGACY_PLANE_OBJECT_NAME_PATTERNS = {
    name: re.compile(
        rf"(?<![a-z0-9-])(?P<name>{re.escape(name)}(?:-[a-z0-9]+)*)(?![a-z0-9-])"
    )
    for name in (
        "admin-token-issuer-proxy",
        "openbao-server",
    )
}

LEGACY_PLANE_NAMESPACES = {
    "api-keys",
    "cassandra-system",
    "ess",
    "nats-system",
    "ncp",
    "nvca-modelcache-init",
    "nvca-operator",
    "nvca-system",
    "nvcf",
    "nvcf-backend",
    "nvcf-ui",
    "sis",
    "vault-system",
}
GATEWAY_ROUTE_KINDS = {
    "GRPCRoute",
    "HTTPRoute",
    "TCPRoute",
    "TLSRoute",
    "UDPRoute",
}
CLUSTER_SCOPED_KINDS = {
    "APIService",
    "CertificateSigningRequest",
    "ClusterIssuer",
    "ClusterRole",
    "ClusterRoleBinding",
    "ClusterTrustBundle",
    "CustomResourceDefinition",
    "MutatingWebhookConfiguration",
    "Namespace",
    "Node",
    "PersistentVolume",
    "PodSecurityPolicy",
    "PriorityClass",
    "RuntimeClass",
    "StorageClass",
    "ValidatingWebhookConfiguration",
    "VolumeAttachment",
}

@dataclasses.dataclass(frozen=True)
class RenderedObject:
    owner: str
    path: str
    doc_index: int
    doc: dict
    kind: str
    namespace: str
    name: str

    @property
    def is_cluster_scoped(self):
        return self.kind in CLUSTER_SCOPED_KINDS

    @property
    def display_namespace(self):
        if self.namespace:
            return self.namespace
        if self.is_cluster_scoped:
            return "<cluster>"
        return "<release-namespace>"

    @property
    def identity(self):
        return f"{self.kind}/{self.display_namespace}/{self.name}"

    @property
    def comparable_identity(self):
        if self.namespace or self.is_cluster_scoped:
            return self.identity
        return ""

    @property
    def location(self):
        return f"{self.path}: document {self.doc_index} {self.identity}"


def fail(message):
    print(f"verify-named-control-plane-render-isolation: {message}", file=sys.stderr)
    sys.exit(1)


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


def is_manifest_file(path):
    if path.suffix not in {".yaml", ".yml"}:
        return False
    return not path.name.endswith(("-values.yaml", "-values.yml"))


def parse_render_spec(spec):
    owner, sep, raw_path = spec.partition("=")
    if sep != "=" or not owner or not raw_path:
        fail(f"--render must use OWNER=DIR, got {spec!r}")
    if owner in RESERVED_OWNERS:
        fail(f"--render owner must be a named control plane, got {owner!r}")
    if not OWNER_RE.match(owner):
        fail(f"--render owner is not a valid control-plane ID: {owner!r}")

    render_dir = pathlib.Path(raw_path)
    if not render_dir.is_dir():
        fail(f"{render_dir} does not exist")
    return owner, render_dir


def load_allowed_shared_identities(inline_identities, identity_files):
    identities = set(inline_identities)
    for raw_path in identity_files:
        path = pathlib.Path(raw_path)
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except Exception as exc:
            fail(f"cannot read --allow-shared-identity-file {path}: {exc}")
        for line_number, line in enumerate(lines, start=1):
            identity = line.partition("#")[0].strip()
            if not identity:
                continue
            if identity.count("/") != 2:
                fail(
                    f"{path}:{line_number}: shared identity must use "
                    f"KIND/NAMESPACE/NAME, got {identity!r}"
                )
            identities.add(identity)
    return identities


def owner_label(obj):
    metadata = obj.doc.get("metadata") if isinstance(obj.doc.get("metadata"), dict) else {}
    labels = metadata.get("labels") if isinstance(metadata.get("labels"), dict) else {}
    label = labels.get(OWNER_LABEL)
    return label if isinstance(label, str) else ""


def expected_namespace(owner, namespace):
    return f"{owner}-{namespace}"


def check_legacy_namespace_reference(obj, field_path, value, errors):
    if not isinstance(value, str) or value not in LEGACY_PLANE_NAMESPACES:
        return
    errors.append(
        f"{obj.location} {format_path(field_path)} references legacy "
        f"plane-owned namespace {value}; expected {expected_namespace(obj.owner, value)}"
    )


def check_service_dns(obj, field_path, value, errors):
    if not isinstance(value, str):
        return
    for match in SERVICE_DNS_RE.finditer(value):
        service = match.group("service")
        namespace = match.group("namespace")
        if namespace in LEGACY_PLANE_NAMESPACES:
            errors.append(
                f"{obj.location} {format_path(field_path)} contains service DNS "
                f"{match.group(0)} in legacy plane-owned namespace {namespace}; "
                f"expected namespace {expected_namespace(obj.owner, namespace)}"
            )
            continue
        if namespace.startswith(f"{obj.owner}-"):
            for legacy_name, pattern in LEGACY_PLANE_OBJECT_NAME_PATTERNS.items():
                if pattern.fullmatch(service):
                    errors.append(
                        f"{obj.location} {format_path(field_path)} contains service DNS "
                        f"{match.group(0)} with legacy plane-owned service name "
                        f"{service}; expected the named control-plane identity "
                        f"instead of {legacy_name}"
                    )


def check_known_legacy_object_reference(obj, field_path, value, errors):
    if not isinstance(value, str):
        return
    reference_keys = {
        "configMapName",
        "rootTokenSecretName",
        "secretName",
        "serviceAccountName",
        "serviceName",
    }
    reference_parent_keys = {
        "backendRefs",
        "parentRefs",
        "roleRef",
        "service",
        "subjects",
    }
    reference_path = field_path == ["metadata", "name"]
    if field_path:
        reference_path = (
            reference_path
            or field_path[-1] in reference_keys
            or (field_path[-1] == "name" and any(key in reference_parent_keys for key in field_path[:-1]))
        )
    if not reference_path:
        return
    for legacy_name, pattern in LEGACY_PLANE_OBJECT_NAME_PATTERNS.items():
        for match in pattern.finditer(value):
            errors.append(
                f"{obj.location} {format_path(field_path)} contains legacy "
                f"plane-owned object reference {match.group('name')}; "
                f"expected the named control-plane identity instead of {legacy_name}"
            )


def check_owner_label(obj, errors):
    label = owner_label(obj)
    if label and label not in {obj.owner, SHARED_OWNER}:
        errors.append(
            f"{obj.location} has {OWNER_LABEL}={label}; expected {obj.owner} or {SHARED_OWNER}"
        )


def check_namespace_identity(obj, errors):
    if obj.kind == "Namespace" and obj.name in LEGACY_PLANE_NAMESPACES:
        errors.append(
            f"{obj.location} creates legacy plane-owned namespace {obj.name}; "
            f"expected {expected_namespace(obj.owner, obj.name)}"
        )


def check_gateway_route_identity(obj, allowed_shared_identities, errors):
    if obj.kind not in GATEWAY_ROUTE_KINDS:
        return
    if obj.namespace.startswith(f"{obj.owner}-"):
        return
    if obj.name.startswith(f"{obj.owner}-"):
        return
    if owner_label(obj) == SHARED_OWNER:
        if obj.identity in allowed_shared_identities:
            return
        errors.append(
            f"{obj.location} has shared owner but gateway routes in unprefixed "
            f"namespaces must be listed in --allow-shared-identity"
        )
        return
    errors.append(
        f"{obj.location} is a gateway route in unprefixed namespace "
        f"{obj.display_namespace}; "
        f"metadata.name must use owner prefix {obj.owner}-"
    )


def check_cluster_scoped_identity(obj, allowed_shared_identities, errors):
    if not obj.is_cluster_scoped:
        return

    label = owner_label(obj)
    if obj.identity in allowed_shared_identities:
        return

    if label == SHARED_OWNER:
        errors.append(
            f"{obj.location} has shared owner but is not listed in "
            f"--allow-shared-identity"
        )
        return

    if not obj.name.startswith(f"{obj.owner}-"):
        errors.append(
            f"{obj.location} is a cluster-scoped object in a named render; "
            f"metadata.name must use owner prefix {obj.owner}- or the object must "
            f"be explicitly allowed as shared"
        )
        return

    if obj.kind != "ClusterRoleBinding":
        return
    role_ref = obj.doc.get("roleRef") if isinstance(obj.doc.get("roleRef"), dict) else {}
    if role_ref.get("kind") != "ClusterRole":
        return
    role_ref_name = role_ref.get("name")
    if not isinstance(role_ref_name, str) or role_ref_name.startswith(f"{obj.owner}-"):
        return
    role_identity = f"ClusterRole/<cluster>/{role_ref_name}"
    if role_identity in allowed_shared_identities:
        return
    errors.append(
        f"{obj.location} roleRef.name references unprefixed ClusterRole "
        f"{role_ref_name}; expected owner-prefixed name or --allow-shared-identity "
        f"{role_identity}"
    )


def check_object(obj, allowed_shared_identities, errors):
    check_owner_label(obj, errors)
    check_namespace_identity(obj, errors)
    check_gateway_route_identity(obj, allowed_shared_identities, errors)
    check_cluster_scoped_identity(obj, allowed_shared_identities, errors)

    for field_path, key, value in walk(obj.doc, []):
        if key == "namespace":
            check_legacy_namespace_reference(obj, field_path, value, errors)
        check_service_dns(obj, field_path, value, errors)
        check_known_legacy_object_reference(obj, field_path, value, errors)


def load_render(owner, render_dir, errors):
    objects = []
    manifest_files = sorted(
        path for path in render_dir.rglob("*") if path.is_file() and is_manifest_file(path)
    )
    if not manifest_files:
        errors.append(f"{render_dir}: no YAML manifests found")
        return objects

    for path in manifest_files:
        rel = path.relative_to(render_dir).as_posix()
        try:
            docs = list(yaml.safe_load_all(path.read_text(encoding="utf-8")))
        except Exception as exc:
            errors.append(f"{rel}: YAML parse failed: {exc}")
            continue

        for doc_index, doc in enumerate(docs, start=1):
            if doc is None:
                continue
            if not isinstance(doc, dict):
                errors.append(f"{rel}: document {doc_index} is not a Kubernetes object map")
                continue

            metadata = doc.get("metadata") if isinstance(doc.get("metadata"), dict) else {}
            kind = doc.get("kind")
            name = metadata.get("name")
            namespace = metadata.get("namespace", "")
            if not isinstance(kind, str) or not isinstance(name, str):
                errors.append(f"{rel}: document {doc_index} missing kind or metadata.name")
                continue
            if namespace is None:
                namespace = ""
            if not isinstance(namespace, str):
                errors.append(f"{rel}: document {doc_index} metadata.namespace is not a string")
                continue

            objects.append(
                RenderedObject(
                    owner=owner,
                    path=rel,
                    doc_index=doc_index,
                    doc=doc,
                    kind=kind,
                    namespace=namespace,
                    name=name,
                )
            )
    return objects


def check_duplicate_identities(objects, allowed_identities, errors):
    by_identity = defaultdict(list)
    for obj in objects:
        identity = obj.comparable_identity
        if identity:
            by_identity[identity].append(obj)

    for identity, identity_objects in sorted(by_identity.items()):
        owners = {obj.owner for obj in identity_objects}
        if len(owners) < 2 or identity in allowed_identities:
            continue

        owner_list = ", ".join(sorted(owners))
        locations = "; ".join(obj.location for obj in identity_objects)
        errors.append(
            f"{identity} appears in multiple named renders ({owner_list}) without an "
            f"explicit shared allowlist entry: {locations}"
        )


def main():
    parser = argparse.ArgumentParser(
        description="verify named control-plane renders do not reuse legacy object identities"
    )
    parser.add_argument(
        "--render",
        action="append",
        required=True,
        metavar="OWNER=DIR",
        help="rendered manifest directory for a named control plane",
    )
    parser.add_argument(
        "--allow-shared-identity",
        action="append",
        default=[],
        metavar="KIND/NAMESPACE/NAME",
        help="object identity allowed to appear in multiple named renders",
    )
    parser.add_argument(
        "--allow-shared-identity-file",
        action="append",
        default=[],
        metavar="PATH",
        help="file containing shared object identities, one KIND/NAMESPACE/NAME per line",
    )
    args = parser.parse_args()

    allowed_shared_identities = load_allowed_shared_identities(
        args.allow_shared_identity,
        args.allow_shared_identity_file,
    )
    errors = []
    objects = []
    for spec in args.render:
        owner, render_dir = parse_render_spec(spec)
        render_objects = load_render(owner, render_dir, errors)
        for obj in render_objects:
            check_object(obj, allowed_shared_identities, errors)
        objects.extend(render_objects)

    check_duplicate_identities(objects, allowed_shared_identities, errors)

    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        fail("named renders failed isolation checks")

    render_count = len(args.render)
    print(
        "verify-named-control-plane-render-isolation: "
        f"all checks passed ({len(objects)} objects across {render_count} render(s))"
    )


if __name__ == "__main__":
    main()
