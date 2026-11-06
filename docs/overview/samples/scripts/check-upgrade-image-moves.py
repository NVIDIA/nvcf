#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Record running images, then fail if a rendered upgrade moves one to an older version.

  check-upgrade-image-moves.py before KUBECONFIG OUTPUT_FILE
  check-upgrade-image-moves.py compare BEFORE_FILE RENDER_DIR
"""

import json
from pathlib import Path
import re
import subprocess
import sys

IMAGE = re.compile(r"""image:\s*["']?([^\s"']+)""")


def split_image(reference):
    """Return (name, tag) for a reference, or None when it has no comparable tag."""
    reference = reference.split("@", 1)[0]
    name, _, tag = reference.rpartition(":")
    if not name or "/" in tag:
        return None
    return name.rsplit("/", 1)[-1], tag


def version_key(tag):
    numbers = re.findall(r"\d+", tag)
    return tuple(int(number) for number in numbers) if numbers else None


def newest(tags):
    keyed = [(version_key(tag), tag) for tag in tags if version_key(tag) is not None]
    return max(keyed) if keyed else None


def collect(references):
    found = {}
    for reference in references:
        parts = split_image(reference)
        if parts:
            found.setdefault(parts[0], set()).add(parts[1])
    return found


def record(kubeconfig, output):
    result = subprocess.run(
        ["kubectl", "--kubeconfig", kubeconfig, "--request-timeout=60s", "get",
         "deploy,sts,ds", "-A", "-o", "json"],
        text=True, capture_output=True, check=True, timeout=120)
    lines = []
    for item in json.loads(result.stdout)["items"]:
        spec = item["spec"]["template"]["spec"]
        for container in spec.get("initContainers", []) + spec["containers"]:
            where = f'{item["metadata"]["namespace"]}/{item["metadata"]["name"]}/{container["name"]}'
            lines.append(f'{where}\t{container["image"]}')
    Path(output).write_text("\n".join(sorted(lines)) + "\n", encoding="utf-8")
    print(f"Recorded {len(lines)} container images in {output}")


def compare(before_file, render_dir):
    running = collect(line.split("\t", 1)[1]
                      for line in Path(before_file).read_text(encoding="utf-8").splitlines()
                      if "\t" in line)
    references = []
    for path in sorted(Path(render_dir).rglob("*")):
        if path.is_file() and path.suffix in {".yaml", ".yml"}:
            references += IMAGE.findall(path.read_text(encoding="utf-8", errors="replace"))
    target = collect(references)
    backward, compared = [], 0
    for name in sorted(running.keys() & target.keys()):
        current, planned = newest(running[name]), newest(target[name])
        if current is None or planned is None:
            continue
        compared += 1
        if planned[0] < current[0]:
            backward.append((name, current[1], planned[1]))
    for name, current, planned in backward:
        print(f"BACKWARD {name}: running {current}, the render pulls {planned}")
    if backward:
        print(f"{len(backward)} image(s) would move to an older version; stop and read "
              "'Releases not covered yet' and Step 3", file=sys.stderr)
        return 1
    print(f"No image moves to an older version ({compared} compared)")
    return 0


def main(argv):
    if len(argv) == 4 and argv[1] == "before":
        record(argv[2], argv[3])
        return 0
    if len(argv) == 4 and argv[1] == "compare":
        return compare(argv[2], argv[3])
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        print("kubectl failed or timed out; stop before continuing", file=sys.stderr)
        sys.exit(1)
    except (OSError, KeyError, ValueError) as error:
        print(f"Image check failed: {error}", file=sys.stderr)
        sys.exit(1)
