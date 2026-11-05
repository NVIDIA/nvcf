#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Wait for rolling workloads; leave OnDelete replacement to the upgrade guide."""

import json
import subprocess
import sys


def main():
    if len(sys.argv) != 4 or not all(sys.argv[1:]):
        raise ValueError("Usage: python3 wait-upgrade-workloads.py KUBECONFIG CONTEXT NAMESPACE")
    kubeconfig, context, namespace = sys.argv[1:]
    kc = ["kubectl", "--kubeconfig", kubeconfig, "--context", context, "-n", namespace]
    result = subprocess.run(kc + ["--request-timeout=30s", "get", "deployments,statefulsets,daemonsets", "-o", "json"],
                            text=True, capture_output=True, check=True, timeout=60)
    workloads = json.loads(result.stdout)["items"]
    if not workloads:
        raise ValueError("No workloads returned; check the context and namespace")
    for workload in workloads:
        name = f'{workload["kind"]}/{workload["metadata"]["name"]}'
        if workload["spec"].get("updateStrategy", {}).get("type") == "OnDelete":
            print(f"{name}: follow the guide's explicit pod-replacement checks", flush=True)
            continue
        subprocess.run(kc + ["rollout", "status", name, "--timeout=15m"], check=True, timeout=930)
    subprocess.run(kc + ["--request-timeout=30s", "get", "pods"], check=True, timeout=60)


if __name__ == "__main__":
    try:
        main()
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        print("Workload check failed or timed out; stop before the next stage", file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"Workload check failed: {error}", file=sys.stderr)
        sys.exit(1)
