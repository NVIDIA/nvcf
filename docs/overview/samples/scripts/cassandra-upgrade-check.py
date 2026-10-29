#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read-only checks for the standard three-member 0.6.0 to 0.6.1 upgrade."""

import argparse
import base64
import gzip
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tarfile


class CheckError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise CheckError(message)


def read(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def digest(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def bundle(directory, version):
    # These two immutable release archives are the procedure's inputs.
    expected = {
        "0.6.0": "64940c360ce4516509fadbb8a640d007501d4697ecff8eaf67452ada452605d8",
        "0.6.1": "644cad630b40ad564fb1406322372279fc7a8e5e0c534f94e0bbf9cf580576ca",
    }
    root = Path(directory)
    archive = root.parent / f"nvcf-self-managed-stack_v{version}" / f"nvcf-self-managed-stack-{version}.tar.gz"
    require(digest(archive) == expected[version], "Unexpected release archive; verify its provenance")
    with tarfile.open(archive) as source:
        files = {m.name[2:] if m.name.startswith("./") else m.name: m for m in source if m.isfile()}
        protected = ["Makefile", "global.yaml.gotmpl", "environments/base.yaml"]
        protected += sorted(n for n in files if n.startswith("helmfile.d/"))
        require(set(p.name for p in (root / "helmfile.d").iterdir()) ==
                {PurePosixPath(n).name for n in protected if n.startswith("helmfile.d/")},
                "Unexpected Helmfile stage files")
        for name in protected:
            require((root / name).read_bytes() == source.extractfile(files[name]).read(),
                    f"Bundle file differs from {version}: {name}; use site overrides")
    return {"bundle": version}


def cql_rows(text):
    """Parse cqlsh SELECT JSON output, including its row-count trailer."""
    rows = [json.loads(line.strip()) for line in text.splitlines() if line.strip().startswith("{")]
    counts = re.findall(r"^\s*\((\d+) rows?\)\s*$", text, re.M)
    require(len(counts) == 1 and int(counts[0]) == len(rows), "Incomplete CQL result or missing row count")
    require(all(isinstance(row, dict) for row in rows), "Invalid CQL JSON row")
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


def ledger(directory, baseline=None):
    root = Path(directory)
    tables = read(root / "migration-tables.json")
    require(tables and all(set(t) == {"table_name"} for t in tables), "Missing migration table inventory")
    names = [t["table_name"] for t in tables]
    require(len(names) == len(set(names)), "Duplicate migration table")
    result = {}
    for name in names:
        require(re.fullmatch(r"[a-z][a-z0-9_]*", name), "Unsupported migration table identifier")
        rows = read(root / f"migration-{name}.json")
        require(len(rows) == 1 and rows[0].get("dirty") is False and
                type(rows[0].get("version")) is int and rows[0]["version"] >= 0,
                f"Missing, dirty or invalid migration record: {name}")
        result[name] = rows[0]["version"]
    if baseline:
        old = read(baseline)
        require(old and all(k in result and result[k] >= v for k, v in old.items()),
                "Migration table disappeared or version regressed")
    return result


def topology(directory, baseline=None, unchanged=False, image=None):
    root = Path(directory)
    sts, pods, pvcs = (read(root / n) for n in ("statefulset.json", "pods.json", "pvcs.json"))
    spec, status = sts["spec"], sts["status"]
    uid = sts["metadata"]["uid"]
    require(sts["metadata"]["name"] == "cassandra" and spec["replicas"] == 3 and
            spec.get("ordinals", {}).get("start", 0) == 0, "Only the standard three-member layout is supported")
    require(status.get("observedGeneration", 0) >= sts["metadata"]["generation"] and
            status.get("replicas") == status.get("readyReplicas") == 3, "StatefulSet is not fully ready")
    policy = spec.get("persistentVolumeClaimRetentionPolicy", {})
    require(all(policy.get(k, "Retain") == "Retain" for k in ("whenDeleted", "whenScaled")),
            "PVC retention must be Retain")
    names = {f"cassandra-{i}" for i in range(3)}
    members = {}
    for pod in pods["items"]:
        controllers = [r for r in pod["metadata"].get("ownerReferences", []) if r.get("controller")]
        owned = len(controllers) == 1 and all(controllers[0].get(k) == v for k, v in
                                             {"uid": uid, "kind": "StatefulSet", "name": "cassandra"}.items())
        name = pod["metadata"]["name"]
        if not owned and not re.fullmatch(r"cassandra-\d+", name):
            continue
        require(owned and name in names and name not in members, f"Unexpected pod ownership: {name}")
        require(not pod["metadata"].get("deletionTimestamp") and pod["status"]["phase"] == "Running" and
                any(c["type"] == "Ready" and c["status"] == "True" for c in pod["status"].get("conditions", [])),
                f"Pod is not Ready: {name}")
        containers = {c["name"]: c for c in pod["spec"]["containers"]}
        states = {c["name"]: c for c in pod["status"].get("containerStatuses", [])}
        require(states.keys() == containers.keys() and "cassandra" in states and
                all(c.get("ready") and c.get("state", {}).get("running") is not None and c.get("imageID") for c in states.values()),
                f"Incomplete container health: {name}")
        claims = [v["persistentVolumeClaim"]["claimName"] for v in pod["spec"]["volumes"] if "persistentVolumeClaim" in v]
        require(claims == [f"data-{name}"], f"Unsupported data/commitlog volumes: {name}")
        matches = [p for p in pvcs["items"] if p["metadata"]["name"] == claims[0]]
        require(len(matches) == 1, f"Missing PVC: {name}")
        pvc = matches[0]
        require(pvc["status"]["phase"] == "Bound" and pvc["spec"].get("volumeName") and
                not pvc["metadata"].get("deletionTimestamp") and not pvc["metadata"].get("ownerReferences"),
                f"PVC is unbound, deleting or has an owner: {name}")
        if image:
            require(containers["cassandra"]["image"] == image and
                    pod["metadata"].get("labels", {}).get("controller-revision-hash") == status.get("updateRevision") and
                    status.get("updatedReplicas") == 3, f"Target runtime/revision is not current: {name}")
        members[name] = {"uid": pod["metadata"]["uid"], "ip": pod["status"]["podIP"],
                         "pvc_uid": pvc["metadata"]["uid"], "pv": pvc["spec"]["volumeName"],
                         "image": containers["cassandra"]["image"], "image_id": states["cassandra"]["imageID"]}
    require(set(members) == names and len({p["ip"] for p in members.values()}) == 3, "Missing or duplicate Cassandra member")
    rings = []
    for name in sorted(names):
        ring = {}
        for line in (root / f"{name}-ring.txt").read_text(encoding="utf-8").splitlines():
            if not re.match(r"^[UD][NLJM]\s", line):
                continue
            fields = line.split()
            ids = re.findall(r"\b[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\b", line)
            require(fields[0] == "UN" and len(ids) == 1 and fields[1] not in ring, f"Unhealthy ring from {name}")
            ring[fields[1]] = ids[0]
        require(set(ring) == {p["ip"] for p in members.values()} and len(set(ring.values())) == 3,
                f"Ring membership differs from owned pods: {name}")
        rings.append(ring)
    require(all(r == rings[0] for r in rings), "Nodes disagree on ring membership")
    for member in members.values():
        member["host_id"] = rings[0][member["ip"]]
    result = {"statefulset_uid": uid, "members": members}
    if baseline:
        old = read(baseline)
        require(set(old["members"]) == names, "Incomplete topology baseline")
        for name in names:
            require(all(members[name][k] == old["members"][name][k] for k in ("pvc_uid", "pv", "host_id")),
                    f"PVC or host identity changed: {name}")
        if unchanged:
            require(result == old, "Source changed since preflight; stop and re-establish the baseline")
        else:
            require(uid != old["statefulset_uid"] and all(members[n]["uid"] != old["members"][n]["uid"] for n in names),
                    "Not all source pods were replaced by the new StatefulSet")
    return result


def json_documents(text):
    decoder = json.JSONDecoder()
    objects = []
    while text.strip():
        item, end = decoder.raw_decode(text.lstrip())
        objects.extend(item["items"] if item.get("kind") == "List" else [item])
        text = text.lstrip()[end:]
    return objects


def target(render, source_values, secret, source_sts, source_directory):
    objects = json_documents(Path(render).read_text(encoding="utf-8"))
    values = read(source_values)
    source = values["cassandra"]
    password = base64.b64decode(read(secret)["data"]["cassandra-password"], validate=True).decode()
    require(password and source["dbUser"]["password"] == password and source.get("serviceRolePassword"),
            "Source Helm credentials disagree with the live Secret; reconcile before upgrading")
    jobs = {o["metadata"]["name"]: o for o in objects if o.get("kind") == "Job"}
    for name in ("cassandra-initialize-cluster", "cassandra-migrations"):
        env = {e["name"]: e.get("value") for e in jobs[name]["spec"]["template"]["spec"]["containers"][0]["env"]}
        require(env.get("CASSANDRA_USER") == source["dbUser"]["user"] and env.get("CASSANDRA_PASSWORD") == password,
                "Target hook does not preserve superuser credentials")
        if name == "cassandra-migrations":
            require(env.get("SERVICE_ROLE_PASSWORD") == source["serviceRolePassword"], "Target service credential differs")
    source_passwords = [e.get("value") for e in values["openbao"]["migrations"]["env"] if e["name"] == "DEFAULT_CASSANDRA_PASSWORD"]
    target_passwords = [e.get("value") for e in jobs["openbao-server-migrations"]["spec"]["template"]["spec"]["containers"][0]["env"]
                        if e["name"] == "DEFAULT_CASSANDRA_PASSWORD"]
    require(source_passwords == target_passwords == [source["serviceRolePassword"]],
            "OpenBao and Cassandra service credentials must remain equal")
    sts = next(o for o in objects if o.get("kind") == "StatefulSet" and o["metadata"]["name"] == "cassandra")
    container = next(c for c in sts["spec"]["template"]["spec"]["containers"] if c["name"] == "cassandra")
    old_container = next(c for c in read(source_sts)["spec"]["template"]["spec"]["containers"] if c["name"] == "cassandra")
    old_env, new_env = ({e["name"]: e.get("value") for e in c["env"]} for c in (old_container, container))
    require(old_env.get("CASSANDRA_USER") == source["dbUser"]["user"], "Source username differs")
    require(old_env.get("CASSANDRA_CLUSTER_NAME") and old_env["CASSANDRA_CLUSTER_NAME"] == new_env.get("CASSANDRA_CLUSTER_NAME") and
            old_env.get("CASSANDRA_DATACENTER") and old_env["CASSANDRA_DATACENTER"] == new_env.get("CASSANDRA_DC"),
            "Target cluster name or datacenter differs")
    require(sts["spec"]["replicas"] == 3 and any(v["name"] == "data" and v.get("subPath") == "data" and
            v["mountPath"] == "/var/lib/cassandra" for v in container["volumeMounts"]), "Target replica count or subPath is wrong")
    require(sts["spec"]["template"]["spec"]["securityContext"]["fsGroup"] == 999 and
            [v["metadata"]["name"] for v in sts["spec"]["volumeClaimTemplates"]] == ["data"],
            "Target volume ownership or claim template differs")
    config = next(o for o in objects if o.get("kind") == "ConfigMap" and o["metadata"]["name"] == "cassandra-conf")
    compatibility = {"uuid_sstable_identifiers_enabled": "true", "storage_compatibility_mode": "NONE",
                     "authenticator": "PasswordAuthenticator", "authorizer": "CassandraAuthorizer"}
    configurations = [config["data"]["cassandra-overrides.yaml"]]
    configurations += [(Path(source_directory) / f"cassandra-{i}-cassandra.yaml").read_text(encoding="utf-8") for i in range(3)]
    for configuration in configurations:
        for key, value in compatibility.items():
            # Support the shipped scalar and class_name forms, without accepting custom YAML.
            lines = [line.split("#", 1)[0].rstrip() for line in configuration.splitlines()]
            starts = [i for i, line in enumerate(lines) if line.startswith(key + ":")]
            require(len(starts) == 1, f"Missing or duplicate Cassandra setting: {key}")
            index = starts[0]
            actual = lines[index].split(":", 1)[1].strip().strip("\"'")
            if not actual and key in ("authenticator", "authorizer"):
                children = []
                for line in lines[index + 1:]:
                    if line and not line[0].isspace():
                        break
                    if line:
                        children.append(line)
                require(len(children) == 1 and children[0].startswith("  class_name:"),
                        f"Unsupported Cassandra authentication configuration: {key}")
                actual = children[0].split(":", 1)[1].strip().strip("\"'")
            require(actual == value, f"Unsupported or incompatible Cassandra setting: {key}")
    require(container["image"].endswith("/cassandra:5.0.8-nv-2.0.1"), "Unexpected target Cassandra image")
    migration_image = jobs["cassandra-migrations"]["spec"]["template"]["spec"]["containers"][0]["image"]
    require(migration_image.endswith("/nvcf-cassandra-migrations:0.10.3"), "Unexpected migration image")
    return {"image": container["image"], "migrations_image": migration_image}


def archive(path, tag):
    with gzip.open(path, "rb") as stream:
        while stream.read(1024 * 1024):
            pass
    with tarfile.open(path, "r:gz") as source:
        entries = source.getmembers()
        members = {m.name: m for m in entries}
        require(len(members) == len(entries), "Duplicate archive entry")
        require(members and all(not PurePosixPath(n).is_absolute() and ".." not in PurePosixPath(n).parts for n in members),
                "Unsafe or empty snapshot archive")
        manifests = [m for m in members.values() if m.isfile() and m.name.endswith(f"/snapshots/{tag}/manifest.json")]
        require(manifests, "No snapshot manifests for the requested tag")
        keyspaces = {PurePosixPath(m.name).parts[1] for m in manifests}
        require({"nvcf_api", "schema_migrations", "system_auth"} <= keyspaces, "Missing application, migration or authentication snapshots")
        data_files = 0
        for manifest in manifests:
            contents = json.load(source.extractfile(manifest))
            for filename in contents["files"]:
                require(PurePosixPath(filename).name == filename, "Invalid manifest filename")
                name = str(PurePosixPath(manifest.name).parent / filename)
                require(name in members, "Snapshot manifest references a missing SSTable")
                require(filename.endswith("Data.db"), "Unexpected SSTable manifest filename")
                prefix = name[:-len("Data.db")]
                # TOC is optional in a snapshot; require the format's data-bearing files.
                required = {"Data.db", "Statistics.db"}
                if prefix.endswith("-bti-"):
                    required.update(("Partitions.db", "Rows.db"))
                elif prefix.endswith("-big-"):
                    required.add("Index.db")
                else:
                    raise CheckError("Unsupported SSTable format")
                require(all(prefix + c in members for c in required) and
                        any(prefix + c in members for c in ("CompressionInfo.db", "CRC.db")),
                        "SSTable data, metadata or primary index is missing")
                toc = prefix + "TOC.txt"
                require(toc in members or not any(n.startswith(prefix + "SAI+") for n in members),
                        "SAI components require a table of contents for completeness checks")
                components = (source.extractfile(members[toc]).read().decode().splitlines()
                              if toc in members else sorted(required))
                require(components, "SSTable table of contents is empty")
                for component in components:
                    require(PurePosixPath(component).name == component, "Invalid SSTable component name")
                    if prefix + component in members:
                        continue
                    # Empty SAI column indexes contain only their completion marker.
                    parts = component.split("+")
                    require(len(parts) == 4 and parts[0] == "SAI", "SSTable component is missing")
                    column = prefix + "+".join(parts[:3]) + "+"
                    marker = column + "ColumnComplete.db"
                    # The released aa marker contains header, empty=1 and CRC32C footer.
                    empty_marker = bytes.fromhex("3fd76c1702616101c02893e800000000000000005b1f9c3c")
                    require(parts[1] == "aa" and marker in members and
                            source.extractfile(members[marker]).read() == empty_marker and
                            {n for n in members if n.startswith(column)} == {marker},
                            "Nonempty or incomplete SAI column index is missing a component")
                data_files += 1
        require(data_files > 0, "Snapshot has no SSTable data")
        for member in members.values():
            require(not member.issym(), "Unexpected snapshot symlink")
            if member.isfile() or member.islnk():
                with source.extractfile(member) as stream:
                    while stream.read(1024 * 1024):
                        pass
    return {"sha256": digest(path), "archive": Path(path).name, "tag": tag}


def space(directory):
    root = Path(directory)
    total = 0
    for ordinal in range(3):
        lines = (root / f"cassandra-{ordinal}-space.txt").read_text(encoding="utf-8").splitlines()
        used, available = int(lines[0].split()[0]), int(lines[-1].split()[3])
        require(used > 0 and available >= used, "Reserve at least one data footprint of free node space before flushing")
        total += used * 1024
    require(shutil.disk_usage(root).free >= total * 1.1, "Backup destination needs room for all nodes plus 10 percent")
    return {"required_backup_bytes": int(total * 1.1)}


def releases(expected, installed):
    require(all(type(r["enabled"]) is bool and type(r["installed"]) is bool for r in expected),
            "Invalid enabled-release inventory")
    desired = [r for r in expected if r["enabled"] is True and r["installed"] is True]
    require(desired, "No enabled target releases")
    require(len({(r["namespace"], r["name"]) for r in desired}) == len(desired), "Duplicate desired release")
    current = {(r["namespace"], r["name"]): r for r in installed}
    require(len(current) == len(installed), "Duplicate Helm release identity")
    for r in desired:
        key = (r["namespace"], r["name"])
        chart = r["chart"].rsplit("/", 1)[-1] + "-" + r["version"]
        require(key in current and current[key]["status"] == "deployed" and current[key]["chart"] == chart,
                f"Release is absent, failed or has the wrong chart: {key}; expected {chart}")
    return {"matched_releases": len(desired)}


def jobs(current, previous):
    old = {j["metadata"]["name"]: j["metadata"]["uid"] for j in previous["items"]}
    for name in ("cassandra-initialize-cluster", "cassandra-migrations"):
        found = [j for j in current["items"] if j["metadata"]["name"] == name]
        require(len(found) == 1 and found[0]["metadata"]["uid"] != old.get(name), f"Missing or stale Job: {name}")
        require(any(c["type"] == "Complete" and c["status"] == "True" for c in found[0].get("status", {}).get("conditions", [])),
                f"Job did not complete: {name}")
    return {"new_jobs_complete": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for name, arguments in {
        "bundle": ["directory", "version"], "cql-json": ["file"],
        "ledger": ["directory"], "topology": ["directory"], "space": ["directory"],
        "target": ["render", "source_values", "secret", "source_sts", "source_directory"],
        "archive": ["file", "tag"], "releases": ["expected", "installed"],
        "jobs": ["current", "previous"],
    }.items():
        command = sub.add_parser(name)
        for argument in arguments:
            command.add_argument(argument)
        if name in ("ledger", "topology"):
            command.add_argument("--baseline")
        if name == "topology":
            command.add_argument("--unchanged", action="store_true")
            command.add_argument("--image")
    args = vars(parser.parse_args())
    command = args.pop("command")
    if command == "cql-json":
        result = cql_rows(Path(args["file"]).read_text(encoding="utf-8"))
    elif command in ("releases", "jobs"):
        result = globals()[command](*(read(p) for p in args.values()))
    elif command == "archive":
        result = archive(args["file"], args["tag"])
    else:
        result = globals()[command](**args)
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except CheckError as error:
        sys.exit(f"Check failed: {error}")
    except (ValueError, KeyError, TypeError, IndexError, StopIteration, OSError, tarfile.TarError, EOFError):
        # Some inputs contain credentials; never print their contents on failure.
        sys.exit("Check failed: incomplete or inconsistent evidence; inspect the private inputs before continuing")
