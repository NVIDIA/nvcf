#!/usr/bin/env python3
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

"""Generate NOTICE from the source tree and the Rust dependency graph.

    scripts/gen-notice.py            write NOTICE
    scripts/gen-notice.py --check    exit 1 if NOTICE is out of date

The dependency list is every third-party crate built for Linux
(x86_64 and aarch64) with all features, as `cargo metadata` resolves it
from Cargo.lock.
"""
import json
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TARGETS = ["x86_64-unknown-linux-gnu", "aarch64-unknown-linux-gnu"]

HEAD = """nvstor
Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.

This product is licensed under the Apache License, Version 2.0 (see
LICENSE). It includes or depends on the third-party software listed
below, each under its own license.

Third-party source in this tree
-------------------------------

bins/nvstor-csi/proto/csi.proto
    Container Storage Interface (CSI) specification
    https://github.com/container-storage-interface/spec
    License: Apache-2.0

crates/nvstor-core/src/xxh3.rs
    XXH3 implemented from the xxHash reference
    https://github.com/Cyan4973/xxHash
    License: BSD-2-Clause

    xxHash Library
    Copyright (c) 2012-2021 Yann Collet
    All rights reserved.

    BSD 2-Clause License (https://www.opensource.org/licenses/bsd-license.php)

    Redistribution and use in source and binary forms, with or without modification,
    are permitted provided that the following conditions are met:

    * Redistributions of source code must retain the above copyright notice, this
      list of conditions and the following disclaimer.

    * Redistributions in binary form must reproduce the above copyright notice, this
      list of conditions and the following disclaimer in the documentation and/or
      other materials provided with the distribution.

    THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
    ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
    WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
    DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR
    ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
    (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
    LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON
    ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
    (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
    SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Rust crates (built from crates.io; license as each crate declares it)
---------------------------------------------------------------------
"""


def crates():
    seen = {}
    for target in TARGETS:
        out = subprocess.run(
            ["cargo", "metadata", "--format-version", "1", "--all-features",
             "--locked", "--filter-platform", target],
            cwd=ROOT, capture_output=True, text=True, check=True).stdout
        meta = json.loads(out)
        reachable = {n["id"] for n in meta["resolve"]["nodes"]}
        for p in meta["packages"]:
            if p["id"] not in reachable or p.get("source") is None:
                continue  # workspace members and path crates are ours
            key = (p["name"], p["version"])
            seen[key] = (p.get("license") or "see crate", p.get("repository") or "")
    return sorted(seen.items())


def render():
    lines = [HEAD]
    for (name, version), (lic, repo) in crates():
        lines.append(f"{name} {version}\n    License: {lic}\n")
        if repo:
            lines.append(f"    {repo}\n")
    return "".join(lines)


def main():
    text = render()
    path = os.path.join(ROOT, "NOTICE")
    if "--check" in sys.argv[1:]:
        try:
            current = open(path, encoding="utf-8").read()
        except FileNotFoundError:
            current = ""
        if current != text:
            print("NOTICE is out of date: run scripts/gen-notice.py", file=sys.stderr)
            return 1
        return 0
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
