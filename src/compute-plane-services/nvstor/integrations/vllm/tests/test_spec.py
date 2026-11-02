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

"""Tests of NvstorSpec with stub vLLM types, a simulated address space
(KV tensors, landing pools and staging rows are ranges of one bytearray;
copies are memmoves of the real copy descriptors) and a fake server that
keeps payloads in the server's layout and verifies PUT checks. No vLLM,
GPU or NIC needed.

    python3 -I integrations/vllm/tests/test_spec.py integrations/vllm
"""

import hashlib
import sys
import threading
import time
import types
from dataclasses import dataclass
from enum import Enum, auto

import numpy as np


def install_stub_vllm():
    base = types.ModuleType("vllm.v1.kv_offload.base")

    class LookupResult(Enum):
        MISS = auto()
        HIT = auto()
        HIT_PENDING = auto()
        RETRY = auto()

    class Medium(Enum):
        CPU = "CPU"
        STORAGE = "STORAGE"

    class Locality(Enum):
        LOCAL = "LOCAL"
        REMOTE = "REMOTE"

    @dataclass
    class OffloadingEvent:
        keys: list
        medium: Medium
        removed: bool
        locality: Locality | None = None

    @dataclass
    class PrepareStoreOutput:
        keys_to_store: list
        store_spec: object
        evicted_keys: list

    @dataclass
    class TransferResult:
        job_id: int
        success: bool
        transfer_size: int | None = None
        transfer_time: float | None = None

    class LoadStoreSpec:
        pass

    class GPULoadStoreSpec(LoadStoreSpec):
        def __init__(self, block_ids, group_sizes, block_indices):
            assert sum(group_sizes) == len(block_ids) and len(block_indices) == len(group_sizes)
            self.block_ids = np.asarray(block_ids, dtype=np.int64)
            self.group_sizes = group_sizes
            self.block_indices = block_indices

    @dataclass
    class RequestOffloadingContext:
        policy: object = None

    for name, obj in dict(LookupResult=LookupResult, Medium=Medium, Locality=Locality, OffloadingEvent=OffloadingEvent,
                          PrepareStoreOutput=PrepareStoreOutput, TransferResult=TransferResult,
                          LoadStoreSpec=LoadStoreSpec, GPULoadStoreSpec=GPULoadStoreSpec,
                          RequestOffloadingContext=RequestOffloadingContext, OffloadingManager=object,
                          OffloadingWorker=object, OffloadingSpec=object, ReqContext=object, OffloadKey=bytes,
                          CanonicalKVCaches=object).items():
        setattr(base, name, obj)
    config = types.ModuleType("vllm.v1.kv_offload.config")
    config.OffloadingConfig = object
    for mod in ("vllm", "vllm.v1", "vllm.v1.kv_offload"):
        sys.modules[mod] = types.ModuleType(mod)
    sys.modules["vllm.v1.kv_offload.base"] = base
    sys.modules["vllm.v1.kv_offload.config"] = config
    return base


sys.path.insert(0, sys.argv[1])
base = install_stub_vllm()
from nvstor_kv import client as kv
from nvstor_kv import spec

NS = bytes(16)
R = base.LookupResult


def key(n, group=0):
    return hashlib.sha256(bytes([n])).digest() + group.to_bytes(4, "big")


class Memory:
    """One address space: allocations are ranges of a bytearray."""

    def __init__(self, size=1 << 22):
        self.buf = bytearray(size)
        self.top = 4096

    def alloc(self, n):
        at = self.top
        self.top += -(-n // 64) * 64
        assert self.top <= len(self.buf)
        return at

    def read(self, at, n):
        return bytes(self.buf[at:at + n])

    def write(self, at, data):
        self.buf[at:at + len(data)] = data


class Event:
    def __init__(self, copier):
        self.copier, self.done = copier, not copier.hold

    def query(self):
        return self.done

    def synchronize(self):
        while not self.done:
            time.sleep(0.001)


class Copier:
    """Copies run at issue (in order); with `hold`, their events complete
    only on `release`."""

    def __init__(self, mem):
        self.mem, self.hold, self.events, self.issued = mem, False, [], 0

    def mark(self):
        return "mark"

    def copy(self, batches, after):
        assert after in ("mark", None)
        for src, dst, sizes in batches:
            for s, d, n in zip(np.ravel(src), np.ravel(dst), np.ravel(sizes)):
                self.mem.write(int(d), self.mem.read(int(s), int(n)))
        self.issued += 1
        ev = Event(self)
        self.events.append(ev)
        return ev

    def release(self):
        self.hold = False
        for ev in self.events:
            ev.done = True

    def synchronize(self):
        pass


def check_of(mem, ns, k, at, n):
    return int.from_bytes(hashlib.blake2b(ns + k + mem.read(at, n), digest_size=8).digest(), "big")


class Server:
    """Payloads by (namespace, key), with their checks."""

    def __init__(self):
        self.blocks, self.down, self.corrupt, self.pending = {}, False, False, set()


class Client:
    """A group's client: the server's landing layout (per ref, slots pages
    of page * bpc back to back) over the simulated memory."""

    def __init__(self, server, mem, landing, pages):
        self.server, self.mem, self.landing, self.pages = server, mem, landing, pages
        self.closed = False

    def _pieces(self, slot):
        at = 0
        for lb, p in zip(self.landing, self.pages):
            yield lb + slot * p, p, at
            at += p

    def get(self, ns, keys, slots, budget):
        if self.server.down:
            raise kv.KvError("lane failed")
        out = []
        for k, s in zip(keys, slots):
            hit = self.server.blocks.get((ns, k))
            if hit is None:
                out.append((kv.STATUS_MISS, 0))
                continue
            payload, check = hit
            if self.server.corrupt:
                payload = bytes([payload[0] ^ 1]) + payload[1:]
            for at, p, off in self._pieces(s):
                self.mem.write(at, payload[off:off + p])
            out.append((kv.STATUS_OK, check))
        return out

    def put(self, ns, keys, slots, checks, budget):
        if self.server.down:
            raise kv.KvError("lane failed")
        out = []
        for k, s, c in zip(keys, slots, checks):
            payload = b"".join(self.mem.read(at, p) for at, p, _ in self._pieces(s))
            if int.from_bytes(hashlib.blake2b(ns + k + payload, digest_size=8).digest(), "big") != c:
                out.append(kv.STATUS_CHECK_MISMATCH)
            elif k in self.server.pending:
                out.append(kv.STATUS_PENDING)
            elif (ns, k) in self.server.blocks:
                out.append(kv.STATUS_EXISTS)
            else:
                self.server.blocks[(ns, k)] = (payload, c)
                out.append(kv.STATUS_OK)
        return out

    def block_check(self, ns, k, at, n):
        return check_of(self.mem, ns, k, at, n)

    def close(self):
        self.closed = True


class Rig:
    """KV tensors (num_blocks rows of `row` bytes) and a worker over them.
    `groups` lists, per KV group, its refs as (tensor index, page bytes)."""

    def __init__(self, tensors, groups, bpc=1, slots=8, part_keys=64, verify=False, server=None, group_ids=None):
        self.mem = Memory()
        self.server = server or Server()
        self.tensors = [(self.mem.alloc(n * row), n, row) for n, row in tensors]
        rng = np.random.default_rng(len(tensors) * 7 + bpc)
        for at, n, row in self.tensors:
            self.mem.write(at, rng.integers(0, 256, n * row, dtype=np.uint8).tobytes())
        self.s = spec._Settings(host="h", port=9900, rails=1, namespace=NS)
        self.s.landing_slots, self.s.part_keys, self.s.verify_loads = slots, min(part_keys, slots), verify
        self.bpc = bpc
        self.groups = []
        for refs in groups:
            pages = [p * bpc for _, p in refs]
            landing = []
            for p in pages:
                landing.append(self.mem.alloc(p * slots))
            payload = sum(pages)
            staging = self.mem.alloc(payload * slots)
            arr = lambda xs: np.asarray(xs, dtype=np.int64)
            self.groups.append(spec._Group(
                client=Client(self.server, self.mem, landing, pages),
                gpu_base=arr([self.tensors[t][0] for t, _ in refs]),
                gpu_stride=arr([self.tensors[t][2] for t, _ in refs]),
                page=arr([p for _, p in refs]),
                landing=arr(landing),
                payload_off=arr(np.cumsum([0] + pages[:-1])),
                payload=payload, staging=staging))
        self.refs = groups
        self.copier = Copier(self.mem)
        self.w = spec.NvstorWorker(self.s, self.groups, bpc, self.copier, group_ids or list(range(len(groups))))

    def page(self, group, block):
        """The bytes of `block` in every ref of `group`."""
        return [self.mem.read(self.tensors[t][0] + block * self.tensors[t][2], p) for t, p in self.refs[group]]

    def drain(self, jobs, timeout=5.0):
        out, end = {}, time.monotonic() + timeout
        while set(jobs) - set(out) and time.monotonic() < end:
            for r in self.w.get_finished():
                out[r.job_id] = r.success
            time.sleep(0.001)
        return out


def gspec(blocks, sizes=None, indices=None):
    sizes = sizes or [len(blocks)]
    return base.GPULoadStoreSpec(blocks, sizes, indices or [0] * len(sizes))


def keys_spec(keys):
    return spec.NvstorLoadStoreSpec(keys)


def test_store_then_load_moves_every_page_byte_for_byte():
    rig = Rig([(32, 256), (32, 96)], [[(0, 200), (1, 96)]])  # 200 of 256: a padded page
    before = {b: rig.page(0, b) for b in (3, 5)}
    rig.w.submit_store(1, gspec([3, 5]), keys_spec([key(1), key(2)]))
    assert rig.drain([1]) == {1: True}
    assert len(rig.server.blocks) == 2, "the server verified both checks"
    rig.w.submit_load(2, keys_spec([key(2), key(1)]), gspec([7, 9]))
    assert rig.drain([2]) == {2: True}
    assert rig.page(0, 7) == before[5] and rig.page(0, 9) == before[3]
    assert sorted(rig.groups[0].free) == list(range(8)), "slots returned"


def test_chunks_of_several_blocks_and_a_partial_first_chunk():
    rig = Rig([(32, 64)], [[(0, 64)]], bpc=2)
    orig = {b: rig.page(0, b) for b in range(4)}
    rig.w.submit_store(1, gspec([0, 1, 2, 3]), keys_spec([key(1), key(2)]))
    assert rig.drain([1]) == {1: True}
    # A load starting at logical block 1: the second half of chunk 1, then chunk 2.
    rig.w.submit_load(2, keys_spec([key(1), key(2)]), gspec([10, 11, 12], indices=[1]))
    assert rig.drain([2]) == {2: True}
    assert [rig.page(0, b) for b in (10, 11, 12)] == [orig[1], orig[2], orig[3]]
    rig.w.submit_store(3, gspec([5, 6, 7], indices=[1]), keys_spec([key(3), key(4)]))
    assert rig.drain([3]) == {3: False}, "a store that does not fill whole chunks is refused"


def test_two_groups_each_with_their_own_pages():
    rig = Rig([(16, 128), (16, 48)], [[(0, 128)], [(1, 48), (0, 64)]])
    a, b = rig.page(0, 2), rig.page(1, 4)
    rig.w.submit_store(1, gspec([2, 4], sizes=[1, 1], indices=[0, 0]), keys_spec([key(1, 0), key(1, 1)]))
    assert rig.drain([1]) == {1: True}
    sizes = {k[1]: len(v[0]) for k, v in rig.server.blocks.items()}
    assert sizes == {key(1, 0): 128, key(1, 1): 112}, "a key holds its group's pages only"
    rig.w.submit_load(2, keys_spec([key(1, 0), key(1, 1)]), gspec([8, 9], sizes=[1, 1], indices=[0, 0]))
    assert rig.drain([2]) == {2: True}
    assert rig.page(0, 8) == a and rig.page(1, 9) == b
    rig.w.submit_load(3, keys_spec([key(1, 1)]), gspec([8], sizes=[1, 0], indices=[0, 0]))
    assert rig.drain([3]) == {3: False}, "a key of another group is refused"


def test_groups_are_matched_by_their_kv_cache_group_id():
    # Offloaded groups 0 and 2 (group 1 is not offloaded): keys carry the ids.
    rig = Rig([(16, 64), (16, 32)], [[(0, 64)], [(1, 32)]], group_ids=[0, 2])
    a, b = rig.page(0, 1), rig.page(1, 2)
    rig.w.submit_store(1, gspec([1, 2], sizes=[1, 1], indices=[0, 0]), keys_spec([key(5, 0), key(5, 2)]))
    assert rig.drain([1]) == {1: True}
    rig.w.submit_load(2, keys_spec([key(5, 0), key(5, 2)]), gspec([6, 7], sizes=[1, 1], indices=[0, 0]))
    assert rig.drain([2]) == {2: True}
    assert rig.page(0, 6) == a and rig.page(1, 7) == b
    rig.w.submit_store(3, gspec([3], sizes=[0, 1], indices=[0, 0]), keys_spec([key(6, 1)]))
    assert rig.drain([3]) == {3: False}, "group position 1 is id 2, not 1"


def test_a_block_another_writer_is_storing_is_not_a_failure():
    rig = Rig([(16, 32)], [[(0, 32)]])
    rig.server.pending.add(key(1))
    rig.w.submit_store(1, gspec([0, 1]), keys_spec([key(1), key(2)]))
    assert rig.drain([1]) == {1: True}


def test_jobs_larger_than_the_landing_pool_run_in_parts():
    rig = Rig([(64, 32)], [[(0, 32)]], slots=4, part_keys=3)
    orig = {b: rig.page(0, b) for b in range(10)}
    ks = [key(i) for i in range(10)]
    rig.w.submit_store(1, gspec(list(range(10))), keys_spec(ks))
    rig.w.submit_load(2, keys_spec(ks[:2]), gspec([40, 41]))
    assert rig.drain([1, 2]) == {1: True, 2: True}, "a waiting load does not block the store, or hang"
    rig.w.submit_load(3, keys_spec(ks), gspec(list(range(20, 30))))
    assert rig.drain([3]) == {3: True}
    assert [rig.page(0, 20 + i) for i in range(10)] == [orig[i] for i in range(10)]


def test_wait_returns_only_once_the_pages_are_read():
    rig = Rig([(64, 32)], [[(0, 32)]], slots=2, part_keys=2)
    rig.copier.hold = True
    rig.w.submit_store(1, gspec([0, 1, 2, 3]), keys_spec([key(i) for i in range(4)]))
    assert rig.copier.issued == 1, "the second part waits for slots"
    done = threading.Event()
    t = threading.Thread(target=lambda: (rig.w.wait({1}), done.set()))
    t.start()
    time.sleep(0.05)
    assert not done.is_set(), "wait does not return while copies are pending or parts wait"
    rig.copier.release()
    t.join(5)
    assert done.is_set() and rig.copier.issued == 2
    assert rig.drain([1]) == {1: True}


def test_failures_fail_their_job_and_return_the_slots():
    rig = Rig([(32, 32)], [[(0, 32)]])
    rig.w.submit_load(1, keys_spec([key(9)]), gspec([0]))
    assert rig.drain([1]) == {1: False}, "a miss fails the load (vLLM recomputes it)"
    rig.server.down = True
    rig.w.submit_store(2, gspec([0]), keys_spec([key(3)]))
    rig.w.submit_load(3, keys_spec([key(3)]), gspec([1]))
    assert rig.drain([2, 3]) == {2: False, 3: False}
    assert sorted(rig.groups[0].free) == list(range(8))
    rig.w.submit_store(4, gspec([]), keys_spec([]))
    assert rig.drain([4]) == {4: True}, "an empty store completes"


def test_verified_loads_refuse_corrupt_blocks():
    rig = Rig([(32, 32)], [[(0, 32)]], verify=True)
    rig.w.submit_store(1, gspec([0]), keys_spec([key(1)]))
    assert rig.drain([1]) == {1: True}
    rig.w.submit_load(2, keys_spec([key(1)]), gspec([5]))
    assert rig.drain([2]) == {2: True}
    rig.server.corrupt = True
    before = rig.page(0, 6)
    rig.w.submit_load(3, keys_spec([key(1)]), gspec([6]))
    assert rig.drain([3]) == {3: False} and rig.page(0, 6) == before, "never copied into the KV pages"


def test_an_expedited_load_goes_first():
    rig = Rig([(32, 32)], [[(0, 32)]], slots=1, part_keys=1)
    rig.server.blocks[(NS, key(1))] = (b"\1" * 32, 0)
    rig.server.blocks[(NS, key(2))] = (b"\2" * 32, 0)
    rig.w.submit_load(1, keys_spec([key(1)]), gspec([0]))
    rig.w.submit_load(2, keys_spec([key(1)]), gspec([1]))
    rig.w.submit_load(3, keys_spec([key(2)]), gspec([2]), expedited=True)
    assert [p.job.job_id for p in rig.groups[0].waiting] == [3, 2]


def test_lookups_resolve_once_then_serve_and_refresh():
    present = {key(1)}
    calls = []

    def head(keys):
        calls.append(list(keys))
        return [kv.STATUS_OK if k in present else kv.STATUS_MISS for k in keys]

    s = spec._Settings(host="h", port=1, rails=1, namespace=NS)
    s.miss_ttl_s = s.hit_ttl_s = 0.0
    lk = spec._Lookups(s, head)
    assert lk.lookup(key(1)) == R.RETRY and lk.lookup(key(2)) == R.RETRY
    end = time.monotonic() + 2
    while lk.pending() and time.monotonic() < end:
        time.sleep(0.001)
    assert lk.lookup(key(1)) == R.HIT and lk.lookup(key(2)) == R.MISS, "expired answers still answer"
    present.clear()
    end = time.monotonic() + 2
    while lk.lookup(key(1)) == R.HIT and time.monotonic() < end:
        time.sleep(0.001)
    assert lk.lookup(key(1)) == R.MISS and lk.take_gone() == [key(1)], "a refresh found it gone"
    assert all(len(set(c)) == len(c) for c in calls), "a key is queued once at a time"
    lk.shutdown()

    def broken(keys):
        raise kv.KvError("down")

    lk2 = spec._Lookups(s, broken)
    lk2.lookup(key(5))
    end = time.monotonic() + 2
    while lk2.pending() and time.monotonic() < end:
        time.sleep(0.001)
    assert lk2.lookup(key(5)) == R.MISS and lk2.errors >= 1
    lk2.shutdown()


def test_the_manager_dedups_stores_refreshes_loads_and_announces():
    present = set()
    s = spec._Settings(host="h", port=1, rails=1, namespace=NS)
    lk = spec._Lookups(s, lambda keys: [kv.STATUS_OK if k in present else kv.STATUS_MISS for k in keys])
    closed = []
    m = spec.NvstorManager(s, lk, enable_events=True, on_shutdown=lambda: closed.append(1))
    assert m.prepare_store([key(1), key(2)], None).keys_to_store == [key(1), key(2)]
    assert m.prepare_store([key(1), key(3)], None).keys_to_store == [key(3)], "in flight"
    m.complete_store([key(1), key(2)], None, success=True)
    present.update([key(1), key(2)])
    ev = list(m.take_events())
    assert len(ev) == 1 and ev[0].locality == base.Locality.REMOTE and not ev[0].removed
    assert m.prepare_store([key(1)], None).keys_to_store == [], "known stored"
    assert m.lookup(key(1), None) == R.HIT
    m.complete_store([key(4)], None, success=False)
    assert list(m.take_events()) == [], "a failed store announces nothing"
    present.discard(key(1))  # evicted on the server
    m.complete_load([key(1)], None)
    end = time.monotonic() + 2
    while m.has_pending_work() and time.monotonic() < end:
        time.sleep(0.001)
    assert m.lookup(key(1), None) == R.MISS
    ev = list(m.take_events())
    assert len(ev) == 1 and ev[0].removed and ev[0].keys == [key(1)]
    assert m.lookup(b"x" * 24, None) == R.MISS, "an xxhash-sized key is a miss"
    assert m.prepare_store([b"x" * 24], None).keys_to_store == [], "and is never stored"
    m.shutdown()
    assert closed == [1], "the lookup client is closed"


def test_failed_loads_must_be_recomputed():
    def cfg(policy):
        return types.SimpleNamespace(kv_transfer_config=types.SimpleNamespace(kv_load_failure_policy=policy))

    spec.check_load_failure_policy(cfg("recompute"))
    spec.check_load_failure_policy(None)  # unknown: a warning
    try:
        spec.check_load_failure_policy(cfg("fail"))
    except ValueError as e:
        assert "recompute" in str(e)
    else:
        raise AssertionError("the fail policy was accepted")


def test_settings_and_namespaces():
    ident = b'{"model": "m"}'
    s = spec._Settings.parse({"nvstor_host": "a", "nvstor_port": 9900, "nvstor_rails": 8,
                              "nvstor_namespace": "00" * 16, "nvstor_landing_slots": 64}, ident, "e1")
    assert (s.host, s.port, s.rails, s.landing_slots, s.part_keys) == ("a", 9900, 8, 64, 64)
    same = spec._Settings.parse({"nvstor_host": "b", "nvstor_port": 1, "nvstor_rails": 1,
                                 "nvstor_namespace": "00" * 16}, ident, "e2")
    assert s.namespace == same.namespace, "one contract, one KV identity: one namespace across engines"
    other = spec._Settings.parse({"nvstor_host": "b", "nvstor_port": 1, "nvstor_rails": 1,
                                  "nvstor_namespace": "00" * 16}, b'{"model": "n"}', "e1")
    assert other.namespace != s.namespace, "another model never shares"
    p1 = spec._Settings.parse({"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1}, ident, "e1")
    p2 = spec._Settings.parse({"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1}, ident, "e1")
    p3 = spec._Settings.parse({"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1}, ident, "e2")
    assert p1.namespace == p2.namespace != p3.namespace, "scheduler and worker agree; engines do not share"
    assert p1.namespace != s.namespace
    off = spec._Settings.parse({"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1,
                                "nvstor_verify_loads": "false"}, ident, "e1")
    assert off.verify_loads is False, "the string false is false"
    for bad in ({}, {"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1, "nvstor_namespace": "00"},
                {"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1, "nvstor_landing_slots": 0},
                {"nvstor_host": "a", "nvstor_port": 1, "nvstor_rails": 1, "nvstor_verify_loads": "maybe"}):
        try:
            spec._Settings.parse(bad, ident, "e")
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted {bad}")


if __name__ == "__main__":
    n = 0
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
            n += 1
    print(f"{n} spec tests ok")
