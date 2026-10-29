#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Validate captured state for the standard three-member 0.6.2 upgrade.

Reads JSON/text files only. Does not contact Kubernetes or change any state.
"""
import argparse
import json
import re
import sys
from pathlib import Path

PODS = [f"openbao-server-{i}" for i in range(3)]
CONTAINERS = {"openbao", "auto-unseal-sidecar"}
PLUGIN = "vault-plugin-secrets-jwt"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read(root, name):
    return json.loads((Path(root) / name).read_text(encoding="utf-8"))


def digest(value):
    match = re.search(r"(?:^|@)(sha256:[0-9a-f]{64})$", value)
    require(match is not None, "missing or unsupported imageID digest")
    return match.group(1)


def image_map(spec):
    containers = spec["containers"]
    require(len(containers) == 2 and {c["name"] for c in containers} == CONTAINERS,
            "expected exactly openbao and auto-unseal-sidecar containers")
    result = {c["name"]: c["image"] for c in containers}
    require(len(set(result.values())) == 1, "server and auto-unseal images differ")
    return result


def health(root):
    root = Path(root)
    sts = read(root, "statefulset.json")
    spec = sts["spec"]
    require(sts["metadata"]["name"] == "openbao-server" and
            sts["metadata"]["namespace"] == "vault-system", "wrong StatefulSet")
    require(spec["replicas"] == 3 and spec.get("ordinals", {}).get("start", 0) == 0,
            "only three members with ordinals 0-2 are supported")
    require(spec["updateStrategy"]["type"] == "OnDelete", "expected OnDelete")
    retention = spec.get("persistentVolumeClaimRetentionPolicy", {})
    require(all(retention.get(key, "Retain") == "Retain" for key in ("whenDeleted", "whenScaled")),
            "data PVC retention must be Retain")
    require(sts["status"]["observedGeneration"] >= sts["metadata"]["generation"],
            "StatefulSet controller has not observed this generation")
    image_map(spec["template"]["spec"])
    pods = read(root, "pods.json")["items"]
    require(len(pods) == 3 and sorted(p["metadata"]["name"] for p in pods) == PODS,
            "expected exactly the three server pods")
    pvcs = {p["metadata"]["name"]: p for p in read(root, "pvcs.json")["items"]}
    nodes = {n["metadata"]["name"]: n for n in read(root, "nodes.json")["items"]}
    cluster_architectures = {n["metadata"]["labels"]["kubernetes.io/arch"] for n in nodes.values()}
    require(len(cluster_architectures) == 1 and cluster_architectures <= {"amd64", "arm64"},
            "every cluster node must use one supported architecture; refresh hooks are not pinned")
    members, architectures, leaders, leader_addresses = {}, set(), [], set()
    for pod in pods:
        meta, ps = pod["metadata"], pod["spec"]
        name = meta["name"]
        require(meta["namespace"] == "vault-system" and not meta.get("deletionTimestamp"),
                f"{name}: wrong namespace or terminating")
        owners = [o for o in meta.get("ownerReferences", []) if o.get("controller")]
        require(len(owners) == 1 and owners[0]["uid"] == sts["metadata"]["uid"],
                f"{name}: wrong controller owner")
        require(pod["status"]["phase"] == "Running" and any(
            c["type"] == "Ready" and c["status"] == "True"
            for c in pod["status"]["conditions"]), f"{name}: not Ready")
        images = image_map(ps)
        statuses = pod["status"]["containerStatuses"]
        require(len(statuses) == 2 and {c["name"] for c in statuses} == CONTAINERS,
                f"{name}: incomplete container status")
        require(all(c["ready"] and "running" in c["state"] for c in statuses),
                f"{name}: containers are not running and ready")
        ids = {c["name"]: digest(c["imageID"]) for c in statuses}
        require(len(set(ids.values())) == 1, f"{name}: server/sidecar digests differ")
        status = read(root, f"{name}-status.json")
        require(status["initialized"] is True and status["sealed"] is False and
                status["ha_enabled"] is True and isinstance(status.get("is_self", False), bool),
                f"{name}: not initialized, unsealed and HA")
        # The released CLI omits is_self on standbys. Only explicit true is active.
        address = (status["leader_address"], status["leader_cluster_address"])
        require(all(isinstance(a, str) and a for a in address), f"{name}: missing leader address")
        leader_addresses.add(address)
        require(status["type"] == "shamir" and status["storage_type"] == "raft",
                f"{name}: only Shamir seal and Raft storage are supported")
        require(status["version"] in ("2.5.4", "2.5.5"), f"{name}: unsupported version")
        node_id = (root / f"{name}-node-id.txt").read_text(encoding="utf-8").strip()
        require(bool(node_id), f"{name}: empty Raft node ID")
        sha = (root / f"{name}-plugin.txt").read_text(encoding="utf-8").split()[0]
        require(re.fullmatch(r"[0-9a-f]{64}", sha) is not None, f"{name}: invalid plugin hash")
        claims = [v["persistentVolumeClaim"]["claimName"] for v in ps["volumes"]
                  if "persistentVolumeClaim" in v]
        require(claims == [f"data-{name}"], f"{name}: nonstandard data volume")
        claim = pvcs[claims[0]]
        require(not claim["metadata"].get("deletionTimestamp") and
                not claim["metadata"].get("ownerReferences"),
                f"{name}: data PVC is deleting or has garbage-collection ownership")
        require(claim["status"]["phase"] == "Bound" and claim["spec"]["volumeName"],
                f"{name}: PVC is not Bound")
        mounts = next(c for c in ps["containers"] if c["name"] == "openbao")["volumeMounts"]
        require(any(m["name"] == "data" and m["mountPath"] == "/openbao/data" and
                    not m.get("subPath") for m in mounts), f"{name}: nonstandard data mount")
        arch = nodes[ps["nodeName"]]["metadata"]["labels"]["kubernetes.io/arch"]
        architectures.add(arch)
        members[name] = {"uid": meta["uid"], "node_id": node_id, "images": images,
                         "image_ids": ids, "plugin_sha256": sha, "version": status["version"],
                         "revision": meta["labels"]["controller-revision-hash"],
                         "pvc": [claim["metadata"]["uid"], claim["spec"]["volumeName"]]}
        if status.get("is_self") is True:
            leaders.append(name)
    require(len(architectures) == 1 and architectures <= {"amd64", "arm64"},
            "all servers must use the same supported CPU architecture")
    require(len(leaders) == 1, "expected exactly one active member")
    require(len(leader_addresses) == 1, "members disagree on leader addresses")
    active = leaders[0]
    ids = sorted(m["node_id"] for m in members.values())
    require(len(set(ids)) == 3, "duplicate Raft node IDs")
    peers = read(root, "peers.json")["data"]["config"]["servers"]
    require(sorted(p["node_id"] for p in peers) == ids and all(p["voter"] is True for p in peers),
            "Raft membership differs from the three voting server pods")
    require([p["node_id"] for p in peers if p["leader"]] == [members[active]["node_id"]],
            "Raft leader disagrees with local server status")
    autopilot = read(root, "autopilot.json")["data"]
    require(autopilot["healthy"] is True and autopilot["failure_tolerance"] >= 1,
            "Raft Autopilot is not healthy with one failure tolerated")
    require(sorted(autopilot["voters"]) == ids and not autopilot.get("non_voters") and
            sorted(autopilot["servers"]) == ids and
            all(s["healthy"] is True for s in autopilot["servers"].values()),
            "Autopilot membership or server health differs")
    require(autopilot["leader"] == members[active]["node_id"], "Autopilot leader differs")
    catalog = read(root, "catalog.json")["data"]
    require(catalog["command"] == PLUGIN and not catalog.get("args") and
            not catalog.get("version"), "custom/versioned plugin catalog is unsupported")
    require(re.fullmatch(r"[0-9a-f]{64}", catalog["sha256"]) is not None, "invalid catalog hash")
    return {"statefulset": sts, "members": members, "active": active,
            "architecture": next(iter(architectures)), "catalog": catalog}


def matches_render(actual, rendered, path=()):
    """Allow API default fields, but require every reviewed field to match."""
    if isinstance(rendered, dict):
        if not isinstance(actual, dict):
            return False
        for key, value in rendered.items():
            # Kubernetes omits the PodSpec hostNetwork field when it is false.
            default = False if path == ("template", "spec") and key == "hostNetwork" else None
            if not matches_render(actual.get(key, default), value, path + (key,)):
                return False
        return True
    if isinstance(rendered, list):
        return isinstance(actual, list) and len(actual) == len(rendered) and all(
            matches_render(a, b, path + (index,))
            for index, (a, b) in enumerate(zip(actual, rendered)))
    return actual == rendered


def compare_baseline(current, baseline):
    require(current["statefulset"]["metadata"]["uid"] == baseline["statefulset"]["metadata"]["uid"],
            "StatefulSet identity changed")
    require(current["architecture"] == baseline["architecture"], "server architecture changed")
    for name in PODS:
        for field in ("node_id", "pvc"):
            require(current["members"][name][field] == baseline["members"][name][field],
                    f"{name}: {field} changed")


def check(root, stage="source", baseline_root=None, target_root=None, leader=None, leader_uid=None):
    current = health(root)
    baseline = health(baseline_root) if baseline_root else current
    compare_baseline(current, baseline)
    if stage == "source":
        versions = {m["version"] for m in current["members"].values()}
        require(len(versions) == 1, "source has mixed versions; use saved baseline to resume")
        for name, member in current["members"].items():
            image = member["images"]["openbao"]
            tag = "2.5.4-nv-1.3.0" if member["version"] == "2.5.4" else "2.5.5-nv-1.3.1"
            require(image.endswith(":" + tag) or image.endswith(":" + tag + "-ea"),
                    f"{name}: unsupported source image")
            require(member["plugin_sha256"] == current["catalog"]["sha256"],
                    f"{name}: source catalog does not match its plugin; use saved transition evidence")
        return {"active": current["active"], "stale": [], "members": current["members"],
                "architecture": current["architecture"]}
    require(target_root is not None and baseline_root is not None, "target and baseline are required")
    target = read(target_root, "statefulset.json")
    rendered = read(target_root, "rendered-statefulset.json")
    require(matches_render(target["spec"], rendered["spec"]),
            "applied StatefulSet differs from the reviewed render")
    image = read(target_root, "image.json")
    target_images = image_map(target["spec"]["template"]["spec"])
    require(all(v == image["image"] for v in target_images.values()) and
            image["image"].endswith(":2.5.5-nv-1.3.1"), "target must use reviewed 2.5.5 image")
    require(current["statefulset"]["spec"] == target["spec"] and
            current["statefulset"]["metadata"]["uid"] == target["metadata"]["uid"] and
            current["statefulset"]["status"]["updateRevision"] == target["status"]["updateRevision"],
            "live StatefulSet differs from frozen reviewed target")
    require(current["catalog"]["sha256"] == image["plugin_sha256"], "catalog is not the target hash")
    stale = []
    for name, member in current["members"].items():
        is_target = (member["images"] == target_images and member["version"] == "2.5.5" and
                     member["revision"] == target["status"]["updateRevision"] and
                     set(member["image_ids"].values()) == {digest(image["image_id"])} and
                     member["plugin_sha256"] == image["plugin_sha256"])
        if not is_target:
            require(member == baseline["members"][name],
                    f"{name}: neither the unchanged source pod nor a healthy target replacement")
            stale.append(name)
    if leader:
        require(current["active"] == leader and current["members"][leader]["uid"] == leader_uid,
                "original active member changed; stop and inspect, never fail back")
    if stage == "complete":
        require(not stale, "stale server pods remain")
        require(current["statefulset"]["status"]["updatedReplicas"] == 3,
                "controller has not recorded three updated replicas")
    return {"active": current["active"], "stale": stale, "members": current["members"],
            "architecture": current["architecture"]}


def watch_events(path):
    text = Path(path).read_text(encoding="utf-8")
    decoder, offset = json.JSONDecoder(), 0
    while offset < len(text):
        if text[offset].isspace():
            offset += 1
            continue
        obj, offset = decoder.raw_decode(text, offset)
        yield obj["object"]


def check_hooks(path, old_uid, started):
    jobs = {}
    for obj in watch_events(path):
        if obj.get("kind") == "Job":
            jobs[obj["metadata"]["uid"]] = obj
    def completed(name):
        found = [j for j in jobs.values() if j["metadata"]["name"] == name and
                 j["metadata"]["creationTimestamp"] >= started and j["metadata"]["uid"] != old_uid and
                 any(c["type"] == "Complete" and c["status"] == "True"
                     for c in j.get("status", {}).get("conditions", []))]
        require(len(found) == 1, f"missing unique fresh completed hook: {name}")
        return found[0]
    refresh = completed("openbao-server-refresh-jwt-plugin-catalog")
    migrations = completed("openbao-server-migrations")
    require(migrations["metadata"]["creationTimestamp"] >= refresh["status"]["completionTime"],
            "migrations started before catalog refresh completed")
    return {"refresh_uid": refresh["metadata"]["uid"], "migration_uid": migrations["metadata"]["uid"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    state = sub.add_parser("state")
    state.add_argument("directory")
    state.add_argument("--stage", choices=("source", "transition", "complete"), default="source")
    state.add_argument("--baseline")
    state.add_argument("--target")
    state.add_argument("--leader")
    state.add_argument("--leader-uid")
    hooks = sub.add_parser("hooks")
    hooks.add_argument("events")
    hooks.add_argument("--old-uid", required=True)
    hooks.add_argument("--started", required=True)
    args = parser.parse_args()
    try:
        if args.command == "hooks":
            result = check_hooks(args.events, args.old_uid, args.started)
        else:
            result = check(args.directory, args.stage, args.baseline, args.target,
                           args.leader, args.leader_uid)
        print(json.dumps(result, sort_keys=True))
    except (ValueError, KeyError, TypeError, OSError, IndexError) as exc:
        print(f"Stop: OpenBao upgrade check failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
