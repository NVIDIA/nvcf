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

"""Python binding of the nvstor KV client (`libnvstor_kv.so`, the C ABI in
crates/nvstor-kv/src/ffi.rs).

A `KvClient` connects to every rail of one nvstor KV server and registers a
landing pool: GPU memory the caller allocates and keeps, laid out per tensor
(tensor t's `slots` pages back to back, tensors one after another), the
layout `landing_bytes` sizes. GET writes blocks into landing slots; PUT
stores blocks from them. A batch runs on all connections at once.

After any call returns, every landing slot it named may be reused: a
connection that failed was dropped, destroying its QPs, before the call
returned. A block that failed reads STATUS_ERROR; a refused request's
blocks read the refusal's status. A batch may not name a slot twice.

The landing pool must cover whole host pages (a dma-buf range does):
`allocate_landing` returns a torch buffer and a pointer that satisfy that.
Calls may come from several threads at once; `close` waits for calls in
flight and later calls raise.

Credentials come from the environment (NVSTOR_KV_SECRET and
NVSTOR_KV_ACCESS_KEY), never from arguments. The library is found through
NVSTOR_KV_LIB (default: libnvstor_kv.so on the loader path). Calls release
the GIL while they run.
"""

from __future__ import annotations

import ctypes
import functools
import mmap
import os
import threading
from collections.abc import Sequence

# kv_proto statuses (crates/nvstor-rdma/src/kv_proto.rs).
STATUS_OK = 0
STATUS_MISS = 1
STATUS_EXISTS = 2
STATUS_PENDING = 3
STATUS_EXPIRED = 4
STATUS_CHECK_MISMATCH = 5
STATUS_NO_SPACE = 6
STATUS_BAD_LAYOUT = 7
STATUS_UNAUTHORIZED = 8
STATUS_ERROR = 9
STATUS_NOT_STORED = 10

NAMESPACE_LEN = 16
_ERR_LEN = 1024


class KvError(RuntimeError):
    """A call failed as a whole (no usable connection, bad arguments)."""


def landing_bytes(pages: Sequence[int], slots: int) -> int:
    """Bytes of a landing pool for these per-tensor page sizes and slots."""
    return sum(pages) * slots


def landing_offsets(pages: Sequence[int], slots: int) -> list[int]:
    """Byte offset of each tensor's region in the landing pool."""
    out, at = [], 0
    for p in pages:
        out.append(at)
        at += p * slots
    return out


def allocate_landing(torch, device: int, pages: Sequence[int], slots: int):
    """A landing pool on GPU `device`: (buffer, pointer). The pointer and the
    pool's length are whole host pages; keep `buffer` alive until the client
    using the pool is closed."""
    page = mmap.PAGESIZE
    need = -(-landing_bytes(pages, slots) // page) * page
    buf = torch.empty(need + page, dtype=torch.uint8, device=f"cuda:{device}")
    ptr = -(-buf.data_ptr() // page) * page
    return buf, ptr


@functools.cache
def _load(path: str | None) -> ctypes.CDLL:
    lib = ctypes.CDLL(path or os.environ.get("NVSTOR_KV_LIB", "libnvstor_kv.so"))
    c_char_p, u8p, u32p, u64p = ctypes.c_char_p, ctypes.POINTER(ctypes.c_uint8), ctypes.POINTER(ctypes.c_uint32), \
        ctypes.POINTER(ctypes.c_uint64)
    lib.bkv_open.argtypes = [c_char_p, ctypes.c_uint16, ctypes.c_uint16, c_char_p, ctypes.c_uint32, ctypes.c_uint32,
                             ctypes.c_uint64, u32p, ctypes.c_uint32, ctypes.c_uint32, ctypes.POINTER(ctypes.c_void_p),
                             ctypes.c_char_p, ctypes.c_size_t]
    lib.bkv_open.restype = ctypes.c_int32
    lib.bkv_get.argtypes = [ctypes.c_void_p, u8p, u8p, ctypes.c_uint32, u32p, ctypes.c_uint32, ctypes.c_uint32, u8p, u64p,
                            ctypes.c_char_p, ctypes.c_size_t]
    lib.bkv_get.restype = ctypes.c_int32
    lib.bkv_put.argtypes = [ctypes.c_void_p, u8p, u8p, ctypes.c_uint32, u32p, u64p, ctypes.c_uint32, ctypes.c_uint32, u8p,
                            ctypes.c_char_p, ctypes.c_size_t]
    lib.bkv_put.restype = ctypes.c_int32
    lib.bkv_head.argtypes = [ctypes.c_void_p, u8p, u8p, ctypes.c_uint32, ctypes.c_uint32, u8p, ctypes.c_char_p,
                             ctypes.c_size_t]
    lib.bkv_head.restype = ctypes.c_int32
    lib.bkv_open_lookup.argtypes = [c_char_p, ctypes.c_uint16, ctypes.c_uint16, c_char_p, ctypes.c_uint32,
                                    ctypes.POINTER(ctypes.c_void_p), ctypes.c_char_p, ctypes.c_size_t]
    lib.bkv_open_lookup.restype = ctypes.c_int32
    lib.bkv_block_check.argtypes = [u8p, u8p, ctypes.c_uint32, ctypes.c_void_p, ctypes.c_size_t, u64p, ctypes.c_char_p,
                                    ctypes.c_size_t]
    lib.bkv_block_check.restype = ctypes.c_int32
    lib.bkv_live_connections.argtypes = [ctypes.c_void_p]
    lib.bkv_live_connections.restype = ctypes.c_uint32
    lib.bkv_close.argtypes = [ctypes.c_void_p]
    lib.bkv_close.restype = None
    return lib


def _u8(data: bytes):
    return (ctypes.c_uint8 * len(data)).from_buffer_copy(data) if data else (ctypes.c_uint8 * 1)()


def block_check(namespace: bytes, key: bytes, data_ptr: int, length: int, *, lib_path: str | None = None) -> int:
    """The 64-bit block check: XXH3-64 over
    namespace || key || the `length` bytes of host memory at `data_ptr`,
    computed by the library (the GIL is released meanwhile)."""
    return _check_with(_load(lib_path), namespace, key, data_ptr, length)


def _check_with(lib: ctypes.CDLL, namespace: bytes, key: bytes, data_ptr: int, length: int) -> int:
    out, err = ctypes.c_uint64(), ctypes.create_string_buffer(_ERR_LEN)
    if lib.bkv_block_check(_namespace(namespace), _u8(key), len(key), data_ptr or None, length, ctypes.byref(out), err,
                           _ERR_LEN) != 0:
        raise KvError(err.value.decode(errors="replace"))
    return int(out.value)


def _namespace(ns: bytes):
    if len(ns) != NAMESPACE_LEN:
        raise ValueError(f"a namespace is {NAMESPACE_LEN} bytes, not {len(ns)}")
    return _u8(ns)


def _keys(keys: Sequence[bytes]) -> tuple[ctypes.Array, int]:
    if not keys:
        raise ValueError("no keys")
    key_len = len(keys[0])
    if any(len(k) != key_len for k in keys):
        raise ValueError("keys of one call must have one length")
    return _u8(b"".join(keys)), key_len


class KvClient:
    def __init__(self, host: str, port: int, rails: int, *, gpu: int, landing_ptr: int, pages: Sequence[int],
                 slots: int, connections: int = 8, devices: str = "netns", lib_path: str | None = None):
        """Connect and register the landing pool at device address
        `landing_ptr` on GPU `gpu` (allocated by the caller, kept until
        `close`). The pool is registered rounded up to whole host pages, so
        the allocation must cover `landing_bytes(pages, slots)` rounded up
        to a host page: `allocate_landing` does."""
        self._h = None  # set first: close() and __del__ run even if this fails
        self._cv = threading.Condition()
        self._in_flight = 0
        if not (0 < port < 65536 and 0 < rails < 65536 and port + rails <= 65536):
            raise ValueError(f"port {port} with {rails} rails")
        if not (0 < connections < 2**32 and 0 < slots < 2**32):
            raise ValueError("connections and slots must be positive")
        if landing_ptr % mmap.PAGESIZE:
            raise ValueError(f"landing_ptr {landing_ptr:#x} is not on a {mmap.PAGESIZE}-byte page (use allocate_landing)")
        self._lib = _load(lib_path)
        self.slots = slots
        self.pages = list(pages)
        err = ctypes.create_string_buffer(_ERR_LEN)
        handle = ctypes.c_void_p()
        page_arr = (ctypes.c_uint32 * len(self.pages))(*self.pages)
        rc = self._lib.bkv_open(host.encode(), port, rails, devices.encode(), connections, gpu, landing_ptr, page_arr,
                                len(self.pages), slots, ctypes.byref(handle), err, _ERR_LEN)
        if rc != 0:
            raise KvError(err.value.decode(errors="replace"))
        self._h = handle

    @classmethod
    def open_lookup(cls, host: str, port: int, rails: int, *, connections: int = 4, devices: str = "netns",
                    lib_path: str | None = None) -> KvClient:
        """A client for HEAD only, with no landing pool (an engine scheduler's)."""
        self = cls.__new__(cls)
        self._h = None
        self._cv = threading.Condition()
        self._in_flight = 0
        self.slots, self.pages = 0, []
        if not (0 < port < 65536 and 0 < rails < 65536 and port + rails <= 65536):
            raise ValueError(f"port {port} with {rails} rails")
        self._lib = _load(lib_path)
        err = ctypes.create_string_buffer(_ERR_LEN)
        handle = ctypes.c_void_p()
        rc = self._lib.bkv_open_lookup(host.encode(), port, rails, devices.encode(), connections, ctypes.byref(handle),
                                       err, _ERR_LEN)
        if rc != 0:
            raise KvError(err.value.decode(errors="replace"))
        self._h = handle
        return self

    def _check(self, rc: int, err) -> None:
        if rc != 0:
            raise KvError(err.value.decode(errors="replace"))

    def _call(self, fn, *args):
        """Run one library call with the handle, counted in flight."""
        with self._cv:
            if not self._h:
                raise KvError("the client is closed")
            self._in_flight += 1
            h = self._h
        try:
            err = ctypes.create_string_buffer(_ERR_LEN)
            self._check(fn(h, *args, err, _ERR_LEN), err)
        finally:
            with self._cv:
                self._in_flight -= 1
                self._cv.notify_all()

    def get(self, namespace: bytes, keys: Sequence[bytes], slots: Sequence[int], budget_ms: int) -> list[tuple[int, int]]:
        """GET keys[i] into landing slot slots[i]: (status, check) per key."""
        if len(keys) != len(slots):
            raise ValueError("one slot per key")
        raw, key_len = _keys(keys)
        n = len(keys)
        slot_arr, st, ck = (ctypes.c_uint32 * n)(*slots), (ctypes.c_uint8 * n)(), (ctypes.c_uint64 * n)()
        self._call(self._lib.bkv_get, _namespace(namespace), raw, key_len, slot_arr, n, budget_ms, st, ck)
        return list(zip(st, ck))

    def put(self, namespace: bytes, keys: Sequence[bytes], slots: Sequence[int], checks: Sequence[int],
            budget_ms: int) -> list[int]:
        """PUT landing slot slots[i] under keys[i] with block check checks[i]: a status per key."""
        if not len(keys) == len(slots) == len(checks):
            raise ValueError("one slot and one check per key")
        raw, key_len = _keys(keys)
        n = len(keys)
        slot_arr, ck, st = (ctypes.c_uint32 * n)(*slots), (ctypes.c_uint64 * n)(*checks), (ctypes.c_uint8 * n)()
        self._call(self._lib.bkv_put, _namespace(namespace), raw, key_len, slot_arr, ck, n, budget_ms, st)
        return list(st)

    def head(self, namespace: bytes, keys: Sequence[bytes]) -> list[int]:
        """Which keys are stored: a status per key."""
        raw, key_len = _keys(keys)
        n = len(keys)
        st = (ctypes.c_uint8 * n)()
        self._call(self._lib.bkv_head, _namespace(namespace), raw, key_len, n, st)
        return list(st)

    def block_check(self, namespace: bytes, key: bytes, data_ptr: int, length: int) -> int:
        """`block_check` with this client's library."""
        return _check_with(self._lib, namespace, key, data_ptr, length)

    def live_connections(self) -> int:
        with self._cv:
            return int(self._lib.bkv_live_connections(self._h)) if self._h else 0

    def close(self) -> None:
        """Wait for calls in flight, then close the connections and the
        landing registrations; the landing memory may be freed after this
        returns. Idempotent and safe from any thread."""
        with self._cv:
            h, self._h = self._h, None
            while h and self._in_flight:
                self._cv.wait()
        if h:
            self._lib.bkv_close(h)

    def __del__(self):
        self.close()
