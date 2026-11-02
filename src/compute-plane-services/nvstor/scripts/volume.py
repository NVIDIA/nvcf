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

"""Provision PM/NBD volumes or copy a quiesced legacy native volume to a new ID."""
import argparse
import socket
import struct
import uuid


def blob(value):
    return struct.pack("<Q", len(value)) + value


def receive(sock, length):
    result = bytearray()
    while len(result) < length:
        chunk = sock.recv(length - len(result))
        if not chunk:
            raise ConnectionError("server closed connection")
        result.extend(chunk)
    return bytes(result)


def request(server, operation, volume_id, payload=b""):
    host, port = server.rsplit(":", 1)
    body = struct.pack("<I", operation) + blob(uuid.UUID(str(volume_id)).bytes) + payload
    with socket.create_connection((host.strip("[]"), int(port)), timeout=30) as sock:
        sock.sendall(struct.pack("<I", len(body)) + body)
        length = struct.unpack("<I", receive(sock, 4))[0]
        if length > 256 * 1024 * 1024:
            raise ValueError("response too large")
        response = receive(sock, length)
    kind = struct.unpack_from("<I", response)[0]
    if kind == 1:
        code, size = struct.unpack_from("<HQ", response, 4)
        raise RuntimeError(f"server {code}: {response[14:14 + size].decode()}")
    return kind, response[4:]


def create(server, volume_id, size):
    if size <= 0:
        raise ValueError("size must be positive")
    kind, _ = request(server, 7, volume_id, blob(str(volume_id).encode()) + struct.pack("<QB", size, 1))
    if kind != 0:
        raise ValueError("unexpected create response")


def read(server, volume_id, offset, size):
    kind, body = request(server, 10, volume_id, struct.pack("<QI", offset, size))
    if kind != 7 or len(body) < 8:
        raise ValueError("unexpected read response")
    count = struct.unpack_from("<Q", body)[0]
    data = body[8:]
    if count != len(data) or count != size:
        raise ValueError("short volume read")
    return data


def write(server, volume_id, offset, data):
    kind, _ = request(server, 9, volume_id, struct.pack("<Q", offset) + blob(data))
    if kind != 0:
        raise ValueError("unexpected write response")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["create", "copy"])
    parser.add_argument("--server", default="127.0.0.1:9100", help="destination native endpoint")
    parser.add_argument("--id", type=uuid.UUID, help="new destination UUID; existing volumes are refused")
    parser.add_argument("--size", type=int, help="required for create, bytes")
    parser.add_argument("--source-server", help="copy source native endpoint")
    parser.add_argument("--source-id", type=uuid.UUID)
    args = parser.parse_args()
    target = args.id or uuid.uuid4()
    size = args.size
    if args.operation == "copy":
        if not args.source_server or not args.source_id:
            parser.error("copy needs --source-server and --source-id; quiesce source writers first")
        kind, body = request(args.source_server, 11, args.source_id)
        if kind != 8:
            raise ValueError("unexpected volume info response")
        size = struct.unpack_from("<Q", body)[0]
    if size is None:
        parser.error("create needs --size")
    create(args.server, target, size)
    if args.operation == "copy":
        for offset in range(0, size, 1024 * 1024):
            count = min(size - offset, 1024 * 1024)
            data = read(args.source_server, args.source_id, offset, count)
            write(args.server, target, offset, data)
            if read(args.server, target, offset, count) != data:
                raise IOError(f"verification failed at offset {offset}; original volume remains intact")
    print(target)


if __name__ == "__main__":
    main()
