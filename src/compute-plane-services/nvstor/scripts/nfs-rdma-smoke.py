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

"""Byte-verifying stress for an existing Linux NFS/RDMA mount.

Requires a real proto=rdma mount. Direct reads bypass the client page cache.
Retain a write-phase dataset, restart the server, then run the verify phase
with the printed prefix to test recovery through the kernel client.
"""

import argparse
import concurrent.futures
import hashlib
import json
import mmap
import os
from pathlib import Path
import re
import subprocess
import time
import uuid


def mount_info(path):
    output = subprocess.check_output(
        ["findmnt", "--json", "--target", str(path), "--output", "TARGET,SOURCE,FSTYPE,OPTIONS"],
        text=True,
    )
    entry = json.loads(output)["filesystems"][0]
    if entry["fstype"] not in ("nfs", "nfs4") or "proto=rdma" not in entry["options"].split(","):
        raise RuntimeError(f"requires an NFS mount with proto=rdma, found {entry}")
    return entry


def payload(worker, index, generation, size):
    seed = f"nvstor-nfs-rdma-v1/{worker}/{index}/{generation}".encode()
    return hashlib.shake_256(seed).digest(size)


def file_size(index, maximum):
    return min([0, 1, 4095, 4096, 4097, 65537, 1048579, maximum][index % 8], maximum)


def direct_read(path, size):
    """Fail explicitly if direct I/O is unsupported; never test cached reads."""
    fd = os.open(path, os.O_RDONLY | os.O_DIRECT)
    try:
        if os.fstat(fd).st_size != size:
            raise RuntimeError(f"wrong file size: {path}")
        if not size:
            return b""
        length = (size + 4095) & ~4095
        with mmap.mmap(-1, length) as buffer:
            count = os.preadv(fd, [buffer], 0)
            if count != size:
                raise RuntimeError(f"short direct read: {path}: {count} != {size}")
            return buffer[:count]
    finally:
        os.close(fd)


def write_worker(root, worker, files, maximum):
    for index in range(files):
        path = root / f"{worker}-{index}"
        size = file_size(index, maximum)
        # Exercise growth and truncation, then commit the final generation.
        for generation, length in [(0, max(1, maximum - size)), (1, size)]:
            data = payload(worker, index, generation, length)
            with path.open("wb") as file:
                file.write(data)
                file.flush()
                os.fsync(file.fileno())
        if direct_read(path, size) != payload(worker, index, 1, size):
            raise RuntimeError(f"write/read mismatch: {path}")
        moved = path.with_suffix(".renamed")
        path.rename(moved)
        moved.rename(path)
        link = path.with_suffix(".link")
        os.link(path, link)
        if direct_read(link, size) != payload(worker, index, 1, size):
            raise RuntimeError(f"hardlink mismatch: {link}")
        link.unlink()


def verify_worker(root, worker, files, maximum):
    total = 0
    for index in range(files):
        size = file_size(index, maximum)
        path = root / f"{worker}-{index}"
        if direct_read(path, size) != payload(worker, index, 1, size):
            raise RuntimeError(f"retained data mismatch: {path}")
        total += size
    return total


def run_workers(function, root, workers, files, maximum):
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        futures = [pool.submit(function, root, worker, files, maximum) for worker in range(workers)]
        return [future.result() for future in futures]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mount", type=Path, help="existing proto=rdma mount, e.g. /mnt/nvstor")
    parser.add_argument("--phase", choices=["lifecycle", "write", "verify", "cleanup"], default="lifecycle")
    parser.add_argument("--prefix", help="dataset name; required for verify and cleanup")
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--files", type=int, default=16, help="files per worker")
    parser.add_argument("--max-size", type=int, default=4 * 1024 * 1024)
    args = parser.parse_args()
    if not 1 <= args.workers <= 128 or not 1 <= args.files <= 10000 or not 1 <= args.max_size <= 64 * 1024 * 1024:
        parser.error("workers: 1..128, files: 1..10000, max-size: 1..67108864")
    if args.phase in ("verify", "cleanup") and not args.prefix:
        parser.error("--prefix is required for a retained dataset")
    prefix = args.prefix or "nfs-rdma-" + uuid.uuid4().hex[:12]
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}", prefix):
        parser.error("prefix must be a simple directory name")
    mount = args.mount.resolve(strict=True)
    info = mount_info(mount)
    root = mount / prefix
    if root.is_symlink():
        raise RuntimeError("dataset must not be a symlink")
    marker = root / "manifest.json"
    started = time.monotonic()
    if args.phase in ("write", "lifecycle"):
        root.mkdir()  # Refuse to overwrite an existing dataset.
        run_workers(write_worker, root, args.workers, args.files, args.max_size)
        manifest = {"format": "nvstor-nfs-rdma-v1", "workers": args.workers, "files": args.files, "max_size": args.max_size}
        with marker.open("x") as file:
            json.dump(manifest, file)
            file.flush()
            os.fsync(file.fileno())
    else:
        with marker.open() as file:
            manifest = json.load(file)
        if manifest.get("format") != "nvstor-nfs-rdma-v1":
            raise RuntimeError("not an NFS/RDMA smoke dataset")
    workers, files, maximum = (manifest[key] for key in ("workers", "files", "max_size"))
    if not all(isinstance(value, int) for value in (workers, files, maximum)) or not (
        1 <= workers <= 128 and 1 <= files <= 10000 and 1 <= maximum <= 64 * 1024 * 1024
    ):
        raise RuntimeError("invalid dataset parameters")
    verified = 0
    if args.phase != "cleanup":
        verified = sum(run_workers(verify_worker, root, workers, files, maximum))
    if args.phase in ("cleanup", "lifecycle"):
        for worker in range(workers):
            for index in range(files):
                (root / f"{worker}-{index}").unlink()
        marker.unlink()
        root.rmdir()  # Unexpected files are never recursively removed.
    print(json.dumps({"phase": args.phase, "prefix": prefix, "files": workers * files,
        "verified_bytes": verified, "seconds": round(time.monotonic() - started, 3), "mount": info}, indent=2))


if __name__ == "__main__":
    main()
