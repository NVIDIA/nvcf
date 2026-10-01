# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Inspect the single-client RPC listener without opening another connection."""
import pathlib


def ready(proc=pathlib.Path('/proc'), port=50052):
    for entry in proc.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            command = (entry / 'cmdline').read_bytes().split(b'\0')[0].decode()
            if pathlib.Path(command).name != 'ggml-rpc-server':
                continue
            status = (entry / 'status').read_text()
            state = next(line.split()[1] for line in status.splitlines() if line.startswith('State:'))
            if state not in ('R', 'S', 'D'):
                continue
            for line in (entry / 'net/tcp').read_text().splitlines()[1:]:
                fields = line.split()
                if int(fields[1].rsplit(':', 1)[1], 16) == port and fields[3] == '0A':
                    return True
        except (FileNotFoundError, ProcessLookupError):
            continue
    return False


if __name__ == '__main__':
    raise SystemExit(0 if ready() else 1)
