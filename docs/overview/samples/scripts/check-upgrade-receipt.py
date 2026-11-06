#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Read the receipt and compare it with the independently selected release."""

import os
import subprocess
import sys


def main():
    for name in ("CONTROL_KUBECONFIG", "CONTROL_CONTEXT", "EXPECTED_CONTROL_STACK_VERSION"):
        if not os.environ.get(name):
            raise ValueError(f"Set {name} before checking the receipt")
    kc = ["kubectl", "--kubeconfig", os.environ["CONTROL_KUBECONFIG"],
          "--context", os.environ["CONTROL_CONTEXT"], "--request-timeout=30s"]
    result = subprocess.run(kc + ["-n", "nvcf", "get", "configmap", "nvcf-upgrade-receipt",
                                  "-o", "jsonpath={.data.installed_stack_version}"],
                            text=True, capture_output=True, check=True, timeout=60)
    expected = os.environ["EXPECTED_CONTROL_STACK_VERSION"]
    if result.stdout.rstrip("\n") != expected:
        raise ValueError(f"Receipt does not match selected Self-Managed {expected}")
    print(f"Receipt matches selected Self-Managed {expected}")


if __name__ == "__main__":
    try:
        main()
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        print("Receipt read failed or timed out; stop before continuing", file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError) as error:
        print(f"Receipt check failed: {error}", file=sys.stderr)
        sys.exit(1)
