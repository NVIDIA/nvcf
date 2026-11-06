#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Check captured mount metadata without changing OpenBao or reading keys."""

import json
from pathlib import Path
import re
import sys


PLUGIN = "vault-plugin-secrets-jwt"


def read_object(path):
    value = json.loads(Path(path).read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("Expected a JSON object")
    return value


def valid_sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value)


def mounts(inventory):
    result = {}
    for path, mount in inventory.items():
        if not isinstance(mount, dict) or not isinstance(mount.get("type"), str):
            raise ValueError("Invalid mount inventory")
        names = [mount.get("plugin_name")]
        for field in ("options", "config"):
            nested = mount.get(field)
            if nested is not None and not isinstance(nested, dict):
                raise ValueError("Invalid mount metadata")
            names.append((nested or {}).get("plugin_name"))
        if mount["type"] == "plugin" and not any(names):
            raise ValueError("Unidentified external plugin mount")
        if mount["type"] != PLUGIN and PLUGIN not in names:
            continue
        configured = mount.get("plugin_version", "")
        running = mount.get("running_plugin_version", "")
        configured = "" if configured is None else configured
        running = "" if running is None else running
        if (not isinstance(configured, str) or configured != running
                or not valid_sha(mount.get("running_sha256"))):
            raise ValueError("Missing runtime metadata or unfinished plugin transition")
        accessor = mount.get("accessor")
        if not isinstance(accessor, str) or not accessor:
            raise ValueError("Missing JWT mount identity")
        result[path] = (accessor, configured, mount["running_sha256"])
    if not result:
        raise ValueError("No JWT mounts found")
    return result


def check(before, after=None, catalog=None):
    baseline = mounts(before)
    if after is None:
        return
    current = mounts(after)
    version = catalog.get("version")
    if (not isinstance(version, str) or not version
            or catalog.get("command") != f"{PLUGIN}-{version}"
            or catalog.get("builtin") is not False or not valid_sha(catalog.get("sha256"))):
        raise ValueError("Invalid target plugin catalog entry")
    for path, identity in baseline.items():
        if current.get(path) != identity:
            raise ValueError("An existing JWT mount is missing or changed; inspect retained metadata")
    for path, (_, configured, sha) in current.items():
        if path not in baseline and (configured != version or sha != catalog["sha256"]):
            raise ValueError("A new JWT mount is not running the selected target plugin")


def main():
    if len(sys.argv) not in (2, 4):
        raise ValueError("Usage: check-upgrade-jwt-mounts.py BEFORE.json [AFTER.json TARGET-CATALOG.json]")
    check(*(read_object(path) for path in sys.argv[1:]))
    print("JWT mount check passed")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError):
        # Mount paths and file contents may contain customer identifiers.
        print("JWT mount check failed; inspect the private capture files and stop before continuing",
              file=sys.stderr)
        sys.exit(1)
