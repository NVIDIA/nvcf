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

"""Tests of the ctypes binding against a built libnvstor_kv.so (no NIC, no GPU needed).

    cargo build -p nvstor-kv --lib
    python3 -I integrations/vllm/tests/test_client_binding.py integrations/vllm target/debug/libnvstor_kv.so
"""
import ctypes, sys
sys.path.insert(0, sys.argv[1])
from nvstor_kv import client as c
assert c.landing_bytes([100, 50], 4) == 600
assert c.landing_offsets([100, 50], 4) == [0, 400]
lib = c._load(sys.argv[2])
# The library loads and its functions are callable: a null client is an error, not a crash.
err = ctypes.create_string_buffer(256)
st = (ctypes.c_uint8 * 1)()
rc = lib.bkv_head(None, c._u8(bytes(16)), c._u8(bytes([1]) * 36), 36, 1, st, err, 256)
assert rc == -1 and b"null" in err.value, (rc, err.value)
# Opening without a secret fails cleanly with a message.
import os
os.environ.pop("NVSTOR_KV_SECRET", None)
try:
    c.KvClient("127.0.0.1", 9900, 1, gpu=0, landing_ptr=0, pages=[4096], slots=1, lib_path=sys.argv[2])
except c.KvError as e:
    assert "NVSTOR_KV_SECRET" in str(e), e
else:
    raise AssertionError("open without a secret succeeded")
try:
    c._keys([b"a" * 36, b"b" * 35])
except ValueError:
    pass
print("binding ok")
# namespace length, port range, alignment, close idempotence on a fake handle
try:
    c._namespace(b"short")
except ValueError:
    pass
else:
    raise AssertionError("short namespace accepted")
for bad in [dict(port=0, rails=1), dict(port=65535, rails=2)]:
    try:
        c.KvClient("h", bad["port"], bad["rails"], gpu=0, landing_ptr=0, pages=[4096], slots=1, lib_path=sys.argv[2])
    except ValueError:
        pass
    else:
        raise AssertionError(f"accepted {bad}")
import mmap
try:
    c.KvClient("h", 9900, 1, gpu=0, landing_ptr=mmap.PAGESIZE + 1, pages=[4096], slots=1, lib_path=sys.argv[2])
except ValueError:
    pass
else:
    raise AssertionError("unaligned landing accepted")
k = c.KvClient.__new__(c.KvClient)
k._h, k._cv, k._in_flight, k._lib = None, __import__("threading").Condition(), 0, lib
k.close(); k.close()
try:
    k.head(bytes(16), [b"a" * 36])
except c.KvError as e:
    assert "closed" in str(e)
print("binding checks ok")
# The block check: matches the library's XXH3 over ns || key || payload, null data only when empty.
ns, key = bytes(range(16)), bytes([7]) * 36
payload = (ctypes.c_uint8 * 1000)(*([9] * 1000))
a = c.block_check(ns, key, ctypes.addressof(payload), 1000, lib_path=sys.argv[2])
assert a == c.block_check(ns, key, ctypes.addressof(payload), 1000, lib_path=sys.argv[2]), "deterministic"
payload[999] = 8
assert c.block_check(ns, key, ctypes.addressof(payload), 1000, lib_path=sys.argv[2]) != a, "covers the payload"
assert c.block_check(bytes(16), key, ctypes.addressof(payload), 0, lib_path=sys.argv[2]) != \
    c.block_check(ns, key, ctypes.addressof(payload), 0, lib_path=sys.argv[2]), "covers the namespace"
try:
    c.block_check(ns, key, 0, 10, lib_path=sys.argv[2])
except c.KvError as e:
    assert "null" in str(e)
else:
    raise AssertionError("null data with a length accepted")
# A lookup client without a secret fails cleanly too.
try:
    c.KvClient.open_lookup("127.0.0.1", 9900, 1, lib_path=sys.argv[2])
except c.KvError as e:
    assert "NVSTOR_KV_SECRET" in str(e), e
else:
    raise AssertionError("lookup open without a secret succeeded")
print("block check and lookup ok")
