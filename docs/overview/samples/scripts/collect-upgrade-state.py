#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Capture release and registration identity in a new private evidence directory."""

import json
import os
from pathlib import Path
import subprocess
import sys


def run(args, input_text=None):
    return subprocess.run(args, input=input_text, text=True, capture_output=True,
                          check=True, timeout=120).stdout


def release_list(kubeconfig, context):
    releases, seen = [], set()
    for offset in range(0, 256000, 256):
        page = json.loads(run(["helm", "--kubeconfig", kubeconfig, "--kube-context", context,
                               "list", "--all", "-A", "--max", "256", "--offset", str(offset), "-o", "json"]))
        if not isinstance(page, list):
            raise ValueError("Invalid Helm release list")
        for release in page:
            identity = (release["namespace"], release["name"])
            if identity in seen:
                raise ValueError("Helm pagination repeated a release; retry collection")
            seen.add(identity)
        releases.extend(page)
        if len(page) < 256:
            return releases
    raise ValueError("Helm listing did not finish; do not use this capture")


def main():
    if len(sys.argv) != 2:
        raise SystemExit("Usage: python3 collect-upgrade-state.py NEW_OUTPUT_DIRECTORY")
    output = Path(sys.argv[1])
    if not output.is_absolute():
        raise SystemExit("Use an absolute output directory")
    required = ("CONTROL_KUBECONFIG", "CONTROL_CONTEXT", "COMPUTE_KUBECONFIG", "COMPUTE_CONTEXT")
    for name in required:
        if not os.environ.get(name):
            raise SystemExit(f"Set {name} before collecting state")
    os.umask(0o077)
    output.mkdir(mode=0o700)
    records, clusters = {}, {}
    for plane in ("control", "compute"):
        kubeconfig = os.environ[f"{plane.upper()}_KUBECONFIG"]
        context = os.environ[f"{plane.upper()}_CONTEXT"]
        kc = ["kubectl", "--kubeconfig", kubeconfig, "--context", context,
              "--request-timeout=30s"]
        if run(kc + ["config", "current-context"]).strip() != context:
            raise SystemExit(f"{plane} kubeconfig must select the recorded context; do not use this capture")
        uid = run(kc + ["get", "namespace", "kube-system", "-o", "jsonpath={.metadata.uid}"]).strip()
        if not uid:
            raise ValueError(f"Missing {plane} cluster identity")
        clusters[plane] = {"context": context, "kube_system_uid": uid}
        releases = release_list(kubeconfig, context)
        if not releases:
            raise ValueError(f"No {plane} releases were returned")
        records[f"{plane}-releases.json"] = releases
        if plane == "compute":
            configmap = json.loads(run(kc + ["-n", "nvca-operator", "get", "configmap",
                                             "nvcfbackend-self-managed", "-o", "json"]))
            identity = json.loads(run(["yq", "-o=json", "."], configmap["data"]["cluster-dto.yaml"]))
            identity = {key: identity.get(key) for key in ("clusterId", "clusterGroupId", "clusterName", "ncaID")}
            if not all(isinstance(value, str) and value for value in identity.values()):
                raise ValueError("Missing compute registration identity")
            records["identity.json"] = identity
    records["clusters.json"] = clusters
    for name, value in records.items():
        (output / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (output / "complete").touch(mode=0o600)
    print(f"Upgrade state captured in {output}")


if __name__ == "__main__":
    try:
        main()
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        print("State collection failed: a read command failed or timed out; do not use this capture", file=sys.stderr)
        sys.exit(1)
    except FileExistsError:
        print("Output directory already exists; preserve it and choose a new capture path", file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print(f"State collection failed ({type(error).__name__}); check inputs and use a new output directory", file=sys.stderr)
        sys.exit(1)
