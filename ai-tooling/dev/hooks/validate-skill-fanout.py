#!/usr/bin/env python3
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import json
import os
from pathlib import Path


ROOTS = [".cursor", ".codex", ".claude"]
FANOUTS = {
    "skills": {
        "required_file": "SKILL.md",
    },
    "hooks": {
        "required_file": None,
    },
}


def source_roots(repo: Path, kind: str) -> list[Path]:
    return sorted(path.resolve() for path in repo.glob(f"*/dev/{kind}") if path.is_dir())


def is_under(path: Path, roots: list[Path]) -> bool:
    return any(path == root or root in path.parents for root in roots)


def main() -> int:
    # Anchor on the script's resolved location so the hook works no matter
    # which subdirectory the invoking agent is running from. The script lives
    # at <repo>/ai-tooling/dev/hooks/validate-skill-fanout.py.
    repo = Path(__file__).resolve().parents[3]
    problems = []

    for kind, config in FANOUTS.items():
        targets_by_name = {}
        allowed_sources = source_roots(repo, kind)

        for root in ROOTS:
            fanout_dir = repo / root / kind
            if not fanout_dir.exists():
                problems.append(f"`{root}/{kind}/` is missing")
                continue
            if not fanout_dir.is_dir():
                problems.append(f"`{root}/{kind}/` is not a directory")
                continue

            for entry in sorted(fanout_dir.iterdir(), key=lambda path: path.name):
                rel = f"{root}/{kind}/{entry.name}"
                if not entry.is_symlink():
                    problems.append(
                        f"`{rel}` is source content, but root {kind} fanouts must contain only symlinks"
                    )
                    continue

                target = os.readlink(entry)
                target_path = (entry.parent / target).resolve()
                if not is_under(target_path, allowed_sources):
                    problems.append(
                        f"`{rel}` points outside the discovered `dev/{kind}` source trees"
                    )
                    continue

                if not target_path.exists():
                    problems.append(f"`{rel}` points to a missing source target")
                elif config["required_file"] and not (
                    target_path / config["required_file"]
                ).exists():
                    problems.append(
                        f"`{rel}` target is missing `{config['required_file']}`"
                    )

                targets_by_name.setdefault(entry.name, {})[root] = target

        for name, roots in sorted(targets_by_name.items()):
            missing = [root for root in ROOTS if root not in roots]
            if missing:
                problems.append(
                    f"`{kind}/{name}` is exposed in only {sorted(roots)}, missing {missing}"
                )
                continue
            if len(set(roots.values())) != 1:
                problems.append(f"`{kind}/{name}` fanout targets differ across tools")

    if not problems:
        print(json.dumps({}))
        return 0

    message = "\n".join(f"- {problem}" for problem in problems)
    response = (
        "Fix the NVCF root fanouts before finishing. "
        "Root skill and hook fanout entries under `.cursor`, `.codex`, "
        "and `.claude` must be matching symlinks to their source trees "
        "under a discovered `dev/skills` or `dev/hooks` source tree.\n\n"
        f"{message}"
    )
    print(
        json.dumps(
            {
                "followup_message": response,
                "decision": "block",
                "reason": response,
            }
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
