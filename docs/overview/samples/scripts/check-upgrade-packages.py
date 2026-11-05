#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Check extracted stack stamps against independently selected release versions."""

import os
from pathlib import Path
import sys


def main():
    required = ("CONTROL_STACK", "COMPUTE_STACK", "TARGET_BUNDLE",
                "EXPECTED_CONTROL_STACK_VERSION", "EXPECTED_COMPUTE_STACK_VERSION",
                "EXPECTED_OBSERVABILITY_STACK_VERSION")
    for name in required:
        if not os.environ.get(name):
            raise ValueError(f"Set {name} from the selected release before running this script")
    stacks = ((Path(os.environ["CONTROL_STACK"]), "CONTROL"),
              (Path(os.environ["COMPUTE_STACK"]), "COMPUTE"),
              (Path(os.environ["TARGET_BUNDLE"]) / "deploy/stacks/observability", "OBSERVABILITY"))
    for stack, plane in stacks:
        actual = (stack / "VERSION").read_text(encoding="utf-8").rstrip("\n")
        expected = os.environ[f"EXPECTED_{plane}_STACK_VERSION"]
        if actual != expected:
            raise ValueError(f"{stack}/VERSION does not match selected version {expected}")
    print("Package versions match the selected release")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        print(f"Package check failed: {error}", file=sys.stderr)
        sys.exit(1)
