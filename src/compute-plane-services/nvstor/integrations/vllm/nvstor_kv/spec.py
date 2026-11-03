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

"""NvstorSpec: a vLLM offloading spec whose medium is an nvstor KV server.
Experimental.

Loaded by vLLM's OffloadingConnector without patching vLLM:

    --kv-transfer-config '{"kv_connector": "OffloadingConnector", "kv_role": "kv_both",
      "kv_connector_extra_config": {"spec_name": "NvstorSpec",
        "spec_module_path": "nvstor_kv.spec",
        "nvstor_host": "10.0.0.5", "nvstor_port": 9900, "nvstor_rails": 8,
        "nvstor_namespace": "<32 hex digits>"}}'

Needs vLLM with failed-load support in the offloading connector (main after
v0.32.0) and `"kv_load_failure_policy": "recompute"` in --kv-transfer-config
(checked at startup): a load that fails is reported failed and vLLM
recomputes it, where its default would fail the request. Loads must stay asynchronous (`sync_load_budget_bytes`
unset): vLLM stops the engine when a sync load fails, and an nvstor load
can fail in normal operation (a block evicted after its lookup).

Data path, per GPU worker (one per engine: tensor-parallel engines are
refused until keys carry the rank). A key belongs to one KV-cache group
(its last 4 bytes) and holds that group's pages of one chunk; each group
has its own landing pool in GPU memory and its own connections, whose
landing layout is the group's pages.
- **load**: blocks are fetched by RDMA straight into free landing slots
  (`libnvstor_kv`), then one batched device-to-device copy
  (`cuMemcpyBatchAsync` through vLLM's `swap_blocks_batch`) moves them
  into vLLM's KV pages on this worker's own CUDA stream.
- **store**: one batched copy moves the pages into landing slots and into
  a pinned host staging row; once it completes, a background thread
  computes each block's check over the staging row and PUTs the blocks
  (the server reads them from the landing slots by RDMA).
A job larger than the landing pool runs in parts of at most
`nvstor_part_keys` keys, so any prefix fits; `wait` returns once the
job's KV pages are no longer read or written.

Scheduler side: a key seen for the first time answers RETRY while one
batched HEAD per round resolves every pending key in the background; after
that the last answer is used and refreshed in the background once it is
older than its TTL, so a request is deferred at most once per key. A
completed load refreshes its keys, so a block the server evicted turns into
a miss. Stored blocks are announced as REMOTE storage events, and removed
ones when a refresh finds them gone.

Namespaces: the blocks of a namespace are shared by every
engine that names it. The namespace sent to the server is derived from the
launch's `nvstor_namespace` and the model's KV identity (model, dtype,
groups, layout, parallelism, bytes per block), so engines that could not
read each other's KV never share blocks. Weight quantization and the
model's revision are not in that identity: a contract must name them.
Without `nvstor_namespace`, the engine's id stands in: the engine shares
only with itself.

With blocks_per_chunk > 1, stores that do not cover whole chunks (windowed
or recurrent groups) are refused and counted; such blocks are recomputed.
"""

from __future__ import annotations

import hashlib
import inspect
import json
import logging
import threading
import time
from collections import OrderedDict, deque
from collections.abc import Collection, Iterable, Sequence
from concurrent.futures import Future, ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import Any

import numpy as np
from vllm.v1.kv_offload.base import (
    CanonicalKVCaches,
    GPULoadStoreSpec,
    LoadStoreSpec,
    Locality,
    LookupResult,
    Medium,
    OffloadingEvent,
    OffloadingManager,
    OffloadingSpec,
    OffloadingWorker,
    OffloadKey,
    PrepareStoreOutput,
    ReqContext,
    RequestOffloadingContext,
    TransferResult,
)
from vllm.v1.kv_offload.config import OffloadingConfig

from . import client as kv

logger = logging.getLogger(__name__)

# PUT statuses that mean the block is stored, or being stored by another
# writer (first PUT wins): not a failure of this store.
_STORED = (kv.STATUS_OK, kv.STATUS_EXISTS, kv.STATUS_PENDING)
# The bytes of an OffloadKey that name its KV-cache group (vllm make_offload_key).
_GROUP_BYTES = 4


class NvstorLoadStoreSpec(LoadStoreSpec):
    """Where offloaded blocks are: their keys on the nvstor server."""

    def __init__(self, keys: list[OffloadKey]):
        self.keys = list(keys)

    def __repr__(self) -> str:
        return f"NvstorLoadStoreSpec({len(self.keys)} keys)"


@dataclass
class _Settings:
    host: str
    port: int
    rails: int
    namespace: bytes
    devices: str = "netns"
    connections: int = 8
    lookup_connections: int = 4
    landing_slots: int = 256
    part_keys: int = 64
    budget_ms: int = 1000
    hit_ttl_s: float = 5.0
    miss_ttl_s: float = 0.5
    max_cached_keys: int = 1_000_000
    verify_loads: bool = False

    @staticmethod
    def parse(extra: dict[str, Any], identity: bytes, engine_id: str) -> _Settings:
        """Settings from kv_connector_extra_config; the namespace is derived
        from `nvstor_namespace` (or `engine_id`) and the KV `identity`."""
        try:
            host, port, rails = extra["nvstor_host"], int(extra["nvstor_port"]), int(extra["nvstor_rails"])
        except KeyError as e:
            raise ValueError(f"NvstorSpec needs kv_connector_extra_config {e.args[0]}") from None
        ns_hex = extra.get("nvstor_namespace")
        contract = None
        if ns_hex is not None:
            contract = bytes.fromhex(str(ns_hex))
            if len(contract) != kv.NAMESPACE_LEN:
                raise ValueError(f"nvstor_namespace must be {2 * kv.NAMESPACE_LEN} hex digits")
        s = _Settings(host=str(host), port=port, rails=rails,
                      namespace=derive_namespace(contract, identity, engine_id))
        for name in ("devices",):
            if f"nvstor_{name}" in extra:
                setattr(s, name, str(extra[f"nvstor_{name}"]))
        for name in ("connections", "lookup_connections", "landing_slots", "part_keys", "budget_ms",
                     "max_cached_keys"):
            if f"nvstor_{name}" in extra:
                setattr(s, name, int(extra[f"nvstor_{name}"]))
        for name in ("hit_ttl_s", "miss_ttl_s"):
            if f"nvstor_{name}" in extra:
                setattr(s, name, float(extra[f"nvstor_{name}"]))
        if "nvstor_verify_loads" in extra:
            s.verify_loads = _flag(extra["nvstor_verify_loads"])
        if min(s.connections, s.lookup_connections, s.landing_slots, s.part_keys, s.budget_ms) <= 0:
            raise ValueError("NvstorSpec: connections, lookup_connections, landing_slots, part_keys and budget_ms "
                             "must be positive")
        s.part_keys = min(s.part_keys, s.landing_slots)
        return s


def kv_identity(config: OffloadingConfig) -> bytes:
    """What two engines must agree on to read each other's KV blocks, from
    the config both the scheduler and the worker see."""
    p = config.parallel
    ident = {
        "model": [config.model.name, config.model.dtype],
        "groups": [[g.tokens_per_block, list(g.layer_names), g.sliding_window_size_in_chunks] for g in config.groups],
        "bytes_per_block": config.worker_kv_bytes_per_block,
        "cache": [config.cache.tokens_per_hash, config.cache.blocks_per_chunk],
        "layout": [getattr(config, "kv_cache_layout", None), getattr(config, "canonical_layout", False),
                   getattr(config, "replicated_layout", False)],
        "parallel": [p.tp_size, p.pp_size, p.pcp_size, p.dcp_size],
    }
    return json.dumps(ident, sort_keys=True).encode()


def derive_namespace(contract: bytes | None, identity: bytes, engine_id: str) -> bytes:
    """The server namespace: the launch contract's (or, without one, the
    engine's own) and the KV identity, hashed to 16 bytes."""
    base = b"engine\0" + engine_id.encode() if contract is None else b"contract\0" + contract
    return hashlib.sha256(b"nvstor-kv/vllm/1\0" + base + b"\0" + identity).digest()[:kv.NAMESPACE_LEN]


def _flag(v: Any) -> bool:
    if isinstance(v, str):
        if v.strip().lower() not in ("true", "false", "1", "0", "yes", "no"):
            raise ValueError(f"NvstorSpec: {v!r} is not a boolean")
        return v.strip().lower() in ("true", "1", "yes")
    return bool(v)


def _key_ok(key: bytes) -> bool:
    # sha256 block hashes and the group: xxhash keys differ between engines.
    return 32 + _GROUP_BYTES <= len(key) <= 64


def _key_group(key: bytes) -> int:
    return int.from_bytes(key[-_GROUP_BYTES:], "big")


class _Lookups:
    """Scheduler-side presence answers. A key never seen answers RETRY
    while a batched HEAD resolves it in the background; after that its last
    answer stands, refreshed in the background once older than its TTL."""

    def __init__(self, s: _Settings, head):
        self._s, self._head = s, head
        self._lock = threading.Lock()
        # key -> (present, fresh until)
        self._known: OrderedDict[bytes, tuple[bool, float]] = OrderedDict()
        self._pending: set[bytes] = set()
        self._queued: list[bytes] = []
        self._wake = threading.Condition(self._lock)
        self._stop = False
        self._gone: list[bytes] = []
        self.errors = 0
        self._thread = threading.Thread(target=self._run, name="nvstor-kv-lookup", daemon=True)
        self._thread.start()

    def _queue(self, key: bytes) -> None:
        # Under the lock.
        if key not in self._pending:
            self._pending.add(key)
            self._queued.append(key)
            self._wake.notify()

    def lookup(self, key: bytes) -> LookupResult:
        with self._lock:
            hit = self._known.get(key)
            if hit is None:
                self._queue(key)
                return LookupResult.RETRY
            self._known.move_to_end(key)
            if hit[1] <= time.monotonic():
                self._queue(key)
            return LookupResult.HIT if hit[0] else LookupResult.MISS

    def known(self, key: bytes) -> bool | None:
        """The last answer for `key`, if any; never queues a HEAD."""
        with self._lock:
            hit = self._known.get(key)
            return None if hit is None else hit[0]

    def refresh(self, keys: Iterable[bytes]) -> None:
        """Ask the server again about `keys`, in the background."""
        with self._lock:
            for k in keys:
                self._queue(k)

    def mark(self, keys: Iterable[bytes], present: bool) -> None:
        ttl = self._s.hit_ttl_s if present else self._s.miss_ttl_s
        with self._lock:
            until = time.monotonic() + ttl
            for k in keys:
                self._remember(k, present, until)

    def pending(self) -> bool:
        with self._lock:
            return bool(self._pending)

    def take_gone(self) -> list[bytes]:
        """Keys a refresh found missing after they were known present."""
        with self._lock:
            gone, self._gone = self._gone, []
            return gone

    def clear(self) -> None:
        with self._lock:
            self._known.clear()

    def _remember(self, k: bytes, present: bool, until: float) -> None:
        # Under the lock.
        old = self._known.get(k)
        if old is not None and old[0] and not present:
            self._gone.append(k)
        self._known[k] = (present, until)
        self._known.move_to_end(k)
        while len(self._known) > self._s.max_cached_keys:
            self._known.popitem(last=False)

    def _run(self) -> None:
        while True:
            with self._lock:
                while not self._queued and not self._stop:
                    self._wake.wait()
                if self._stop:
                    return
                batch, self._queued = self._queued, []
            try:
                statuses = self._head(batch)
                now = time.monotonic()
                with self._lock:
                    for k, st in zip(batch, statuses):
                        present = st == kv.STATUS_OK
                        self._remember(k, present, now + (self._s.hit_ttl_s if present else self._s.miss_ttl_s))
            except Exception:
                self.errors += 1
                logger.exception("NvstorSpec: lookup failed; %d keys treated as misses", len(batch))
                with self._lock:
                    until = time.monotonic() + self._s.miss_ttl_s
                    for k in batch:
                        self._remember(k, False, until)
            finally:
                with self._lock:
                    self._pending.difference_update(batch)

    def shutdown(self) -> None:
        with self._lock:
            self._stop = True
            self._wake.notify()
        self._thread.join()


class NvstorManager(OffloadingManager):
    def __init__(self, s: _Settings, lookups: _Lookups, enable_events: bool, on_shutdown=None):
        self._s = s
        self._lookups = lookups
        self._events_on = enable_events
        self._events: list[OffloadingEvent] = []
        self._storing: set[bytes] = set()
        self._on_shutdown = on_shutdown
        self._bad_keys = 0

    def _usable(self, key: bytes) -> bool:
        if _key_ok(key):
            return True
        self._bad_keys += 1
        if self._bad_keys == 1:
            logger.error("NvstorSpec: a key of %d bytes: nvstor needs sha256 block hashes "
                         "(--prefix-caching-hash-algo sha256); every block is a miss and nothing is stored", len(key))
        return False

    def lookup(self, key: OffloadKey, req_context: ReqContext) -> LookupResult:
        if not self._usable(key):
            return LookupResult.MISS
        return self._lookups.lookup(key)

    def prepare_load(self, keys: Collection[OffloadKey], req_context: ReqContext) -> LoadStoreSpec:
        return NvstorLoadStoreSpec(list(keys))

    def complete_load(self, keys: Collection[OffloadKey], req_context: ReqContext):
        # Called whether or not the load succeeded: ask again, so a block
        # the server no longer has stops answering HIT.
        self._lookups.refresh(keys)

    def prepare_store(self, keys: Collection[OffloadKey], req_context: ReqContext) -> PrepareStoreOutput | None:
        to_store = [k for k in keys
                    if self._usable(k) and k not in self._storing and self._lookups.known(k) is not True]
        self._storing.update(to_store)
        return PrepareStoreOutput(keys_to_store=to_store, store_spec=NvstorLoadStoreSpec(to_store), evicted_keys=[])

    def complete_store(self, keys: Collection[OffloadKey], req_context: ReqContext, success: bool = True):
        keys = list(keys)
        self._storing.difference_update(keys)
        if success:
            self._lookups.mark(keys, True)
            if self._events_on and keys:
                self._events.append(OffloadingEvent(keys=keys, medium=Medium.STORAGE, removed=False,
                                                    locality=Locality.REMOTE))

    def on_new_request(self, req_context: ReqContext) -> RequestOffloadingContext:
        return RequestOffloadingContext()

    def take_events(self) -> Iterable[OffloadingEvent]:
        gone = self._lookups.take_gone()
        if self._events_on and gone:
            self._events.append(OffloadingEvent(keys=gone, medium=Medium.STORAGE, removed=True,
                                                locality=Locality.REMOTE))
        events, self._events = self._events, []
        return events

    def has_pending_work(self) -> bool:
        # A deferred request needs another step once its lookups resolve.
        return self._lookups.pending()

    def reset_cache(self) -> None:
        self._lookups.clear()

    def shutdown(self) -> None:
        self._lookups.shutdown()
        if self._on_shutdown is not None:
            self._on_shutdown()


# ---------------------------------------------------------------- worker

@dataclass
class _Group:
    """One KV-cache group: its connections and landing pool, its pinned
    staging rows (one payload per slot), and per ref (layer tensor of the
    group) the addresses its copies use."""
    client: Any
    gpu_base: np.ndarray     # per ref: the KV tensor's address
    gpu_stride: np.ndarray   # per ref: bytes from one GPU block to the next
    page: np.ndarray         # per ref: bytes of one GPU block's page
    landing: np.ndarray      # per ref: its landing region (slots pages of page * bpc)
    payload_off: np.ndarray  # per ref: its offset in a block's payload
    payload: int             # bytes of one key's block
    staging: int             # address of the pinned staging rows
    free: list[int] = field(default_factory=list)
    waiting: deque = field(default_factory=deque)


@dataclass
class _Part:
    """At most `part_keys` keys of one group, the GPU blocks of their
    sub-blocks, and each sub-block's position (key index * blocks_per_chunk
    + sub-block)."""
    job: _Job
    group: int
    keys: list[bytes]
    gpu_blocks: list[int]
    positions: list[int]
    slots: list[int] | None = None
    fetch: Future | None = None
    event: Any = None           # the copy's CUDA event
    put: Future | None = None
    done: bool = False


@dataclass
class _Job:
    job_id: int
    store: bool
    started: float = 0.0
    parts: list[_Part] = field(default_factory=list)
    ok: bool = True
    remaining: int = 0
    after: Any = None   # the model's stream at submission (a CUDA event)


def split_job(keys: Sequence[bytes], spec: GPULoadStoreSpec, bpc: int, part_keys: int, store: bool,
              job: _Job, group_ids: Sequence[int]) -> list[_Part]:
    """The parts of a job: per group (in the GPU spec's order), the group's
    keys and the GPU block each of their sub-blocks maps to, cut into parts
    of at most `part_keys` keys. Raises ValueError when keys and blocks
    disagree (a key's group id must be `group_ids[i]` for the spec's i-th
    group), or when a store would leave part of a block unwritten."""
    blocks = list(spec.block_ids)
    sizes = list(spec.group_sizes)
    indices = list(spec.block_indices)
    parts: list[_Part] = []
    k_at = b_at = 0
    for g, (size, first) in enumerate(zip(sizes, indices)):
        if size == 0:
            continue
        skip = first % bpc
        n_keys = -(-(size + skip) // bpc)
        if store and (skip or (size + skip) % bpc):
            raise ValueError(f"group {g}: a store of {size} blocks from index {first} does not fill whole chunks")
        g_keys = list(keys[k_at:k_at + n_keys])
        if len(g_keys) != n_keys or any(_key_group(k) != group_ids[g] for k in g_keys):
            raise ValueError(f"group {g}: the keys do not match the GPU blocks")
        g_blocks = blocks[b_at:b_at + size]
        k_at, b_at = k_at + n_keys, b_at + size
        for p0 in range(0, n_keys, part_keys):
            p_keys = g_keys[p0:p0 + part_keys]
            # Sub-block i of the group is at position skip + i of its keys.
            lo = max(0, p0 * bpc - skip)
            hi = min(size, (p0 + len(p_keys)) * bpc - skip)
            parts.append(_Part(job, g, p_keys, g_blocks[lo:hi], [skip + i - p0 * bpc for i in range(lo, hi)]))
    if k_at != len(keys) or b_at != len(blocks):
        raise ValueError(f"{len(keys)} keys and {len(blocks)} blocks do not match the groups")
    return parts


def _sub_blocks(part: _Part, bpc: int):
    """(GPU blocks, slots, sub-block indices) of a part's sub-blocks, as
    (m, 1) arrays for broadcasting against the per-ref arrays."""
    assert part.slots is not None
    pos = np.asarray(part.positions, dtype=np.int64)
    slots = np.asarray(part.slots, dtype=np.int64)[pos // bpc]
    return (np.asarray(part.gpu_blocks, dtype=np.int64)[:, None], slots[:, None], (pos % bpc)[:, None])


def pages_and_landing(g: _Group, part: _Part, bpc: int):
    """(KV page addresses, landing addresses, sizes) of every (sub-block,
    ref) of a part."""
    blocks, slots, sub = _sub_blocks(part, bpc)
    pages = g.gpu_base + blocks * g.gpu_stride
    landing = g.landing + (slots * bpc + sub) * g.page
    return pages, landing, np.broadcast_to(g.page, pages.shape)


def pages_and_staging(g: _Group, part: _Part, bpc: int):
    """(KV page addresses, staging addresses, sizes): each sub-block at its
    place in its slot's staging payload (ref by ref, each ref's chunk
    sub-blocks back to back), the server's payload order."""
    blocks, slots, sub = _sub_blocks(part, bpc)
    pages = g.gpu_base + blocks * g.gpu_stride
    staging = g.staging + slots * g.payload + g.payload_off + sub * g.page
    return pages, staging, np.broadcast_to(g.page, pages.shape)


def landing_and_staging(g: _Group, part: _Part, bpc: int):
    """(landing addresses, staging addresses, sizes) of whole slots."""
    assert part.slots is not None
    slots = np.asarray(part.slots, dtype=np.int64)[:, None]
    landing = g.landing + slots * g.page * bpc
    staging = g.staging + slots * g.payload + g.payload_off
    return landing, staging, np.broadcast_to(g.page * bpc, landing.shape)


class NvstorWorker(OffloadingWorker):
    """Loads: GET into landing slots, then one batched copy into the KV
    pages. Stores: one batched copy out of the KV pages, then checks and
    PUT. Parts wait for free slots of their group. `group_ids[i]` is the
    KV-cache group id (the one in the keys) of the GPU specs' i-th group."""

    def __init__(self, s: _Settings, groups: list[_Group], bpc: int, copier, group_ids: Sequence[int]):
        if len(group_ids) != len(groups):
            raise ValueError(f"{len(groups)} KV groups with {len(group_ids)} group ids")
        self._s = s
        self._groups = groups
        self._group_ids = list(group_ids)
        self._bpc = bpc
        self._copier = copier
        for g in groups:
            g.free = list(range(s.landing_slots))
        self._jobs: dict[int, _Job] = {}
        self._copying: deque[_Part] = deque()      # load copies in flight, in issue order
        self._storing: deque[_Part] = deque()      # store copies in flight, in issue order
        # Fetches and PUTs on separate threads: a load never queues behind stores.
        threads = max(2, s.connections * len(groups))
        self._fetch_pool = ThreadPoolExecutor(max_workers=threads, thread_name_prefix="nvstor-kv-get")
        self._put_pool = ThreadPoolExecutor(max_workers=threads, thread_name_prefix="nvstor-kv-put")
        self._results: list[TransferResult] = []
        self.refused_stores = 0

    # -- submission (the worker's thread)

    def submit_load(self, job_id: int, src_spec: LoadStoreSpec, dst_spec: GPULoadStoreSpec,
                    expedited: bool = False) -> bool:
        assert isinstance(src_spec, NvstorLoadStoreSpec)
        return self._submit(job_id, src_spec.keys, dst_spec, store=False, front=expedited)

    def submit_store(self, job_id: int, src_spec: GPULoadStoreSpec, dst_spec: LoadStoreSpec) -> bool:
        assert isinstance(dst_spec, NvstorLoadStoreSpec)
        return self._submit(job_id, dst_spec.keys, src_spec, store=True, front=False)

    def _submit(self, job_id: int, keys: list[bytes], spec: GPULoadStoreSpec, store: bool, front: bool) -> bool:
        job = _Job(job_id, store, started=time.monotonic())
        try:
            job.parts = split_job(keys, spec, self._bpc, self._s.part_keys, store, job, self._group_ids)
        except ValueError as e:
            self._refused(job_id, store, e)
            return True
        if not job.parts:
            self._results.append(TransferResult(job_id=job_id, success=True, transfer_size=0, transfer_time=0.0))
            return True
        # Copies of this job start after what the model has issued so far
        # (its writes of the pages a store reads; zeroing of the pages a
        # load writes), not after what it issues later.
        job.after = self._copier.mark()
        job.remaining = len(job.parts)
        self._jobs[job_id] = job
        for part in (reversed(job.parts) if front else job.parts):
            q = self._groups[part.group].waiting
            if front:
                q.appendleft(part)
            else:
                q.append(part)
        self._start_waiting()
        return True

    def _refused(self, job_id: int, store: bool, why: Exception) -> None:
        if store:
            # Stores of partial chunks (bpc > 1 with windowed or recurrent
            # groups) are refused by design: counted, logged now and then.
            self.refused_stores += 1
            if self.refused_stores & (self.refused_stores - 1) == 0:
                logger.warning("NvstorSpec: %d stores refused so far; latest, job %d: %s", self.refused_stores,
                               job_id, why)
        else:
            logger.error("NvstorSpec: load job %d refused: %s", job_id, why)
        self._results.append(TransferResult(job_id=job_id, success=False))

    def _start_waiting(self) -> None:
        for g in self._groups:
            while g.waiting and len(g.waiting[0].keys) <= len(g.free):
                part = g.waiting.popleft()
                n = len(part.keys)
                part.slots, g.free = g.free[:n], g.free[n:]
                if part.job.store:
                    self._start_store(g, part)
                else:
                    part.fetch = self._fetch_pool.submit(self._fetch, g, part)

    def _issue(self, part: _Part, batches, what: str) -> bool:
        """Issue a part's copy; on failure, wait out whatever of it was
        queued (its slots are then free to reuse) and fail the part."""
        try:
            part.event = self._copier.copy(batches, after=part.job.after)
            return True
        except Exception:
            logger.exception("NvstorSpec: job %d: %s failed", part.job.job_id, what)
            self._copier.synchronize()
            self._finish(part, False)
            return False

    def _start_store(self, g: _Group, part: _Part) -> None:
        # KV pages -> landing slots and -> staging rows; the PUT starts once
        # the copy has completed (_progress).
        if self._issue(part, [pages_and_landing(g, part, self._bpc), pages_and_staging(g, part, self._bpc)],
                       "copy out of the KV pages"):
            self._storing.append(part)

    # -- network (pool threads)

    def _fetch(self, g: _Group, part: _Part) -> bool:
        res = g.client.get(self._s.namespace, part.keys, part.slots, self._s.budget_ms)
        if not all(st == kv.STATUS_OK for st, _ in res):
            return False
        if self._s.verify_loads:
            # Landing slots -> staging rows; each block's check against
            # the one the server returned.
            self._copier.copy([landing_and_staging(g, part, self._bpc)], after=None).synchronize()
            assert part.slots is not None
            for key, slot, (_st, check) in zip(part.keys, part.slots, res):
                if g.client.block_check(self._s.namespace, key, g.staging + slot * g.payload, g.payload) != check:
                    logger.error("NvstorSpec: load job %d: block check mismatch", part.job.job_id)
                    return False
        return True

    def _put(self, g: _Group, part: _Part) -> bool:
        assert part.slots is not None
        checks = [g.client.block_check(self._s.namespace, k, g.staging + slot * g.payload, g.payload)
                  for k, slot in zip(part.keys, part.slots)]
        statuses = g.client.put(self._s.namespace, part.keys, part.slots, checks, self._s.budget_ms)
        return all(x in _STORED for x in statuses)

    # -- completion (the worker's thread)

    def _finish(self, part: _Part, ok: bool) -> None:
        g = self._groups[part.group]
        if part.slots is not None:
            g.free.extend(part.slots)
            part.slots = None
        part.done = True
        job = part.job
        job.ok = job.ok and ok
        job.remaining -= 1
        if job.remaining == 0:
            del self._jobs[job.job_id]
            if job.ok:
                size = sum(len(p.keys) * self._groups[p.group].payload for p in job.parts)
                self._results.append(TransferResult(job_id=job.job_id, success=True, transfer_size=size,
                                                    transfer_time=time.monotonic() - job.started))
            else:
                self._results.append(TransferResult(job_id=job.job_id, success=False))

    @staticmethod
    def _outcome(fut: Future, what: str, job_id: int) -> bool:
        try:
            return bool(fut.result())
        except Exception:
            logger.exception("NvstorSpec: %s job %d failed", what, job_id)
            return False

    def _progress(self) -> None:
        for job in list(self._jobs.values()):
            for part in job.parts:
                if part.done:
                    continue
                if part.fetch is not None and part.event is None and part.fetch.done():
                    if not self._outcome(part.fetch, "load", job.job_id):
                        self._finish(part, False)
                        continue
                    pages, landing, sizes = pages_and_landing(self._groups[part.group], part, self._bpc)
                    if self._issue(part, [(landing, pages, sizes)], "copy into the KV pages"):
                        self._copying.append(part)
                elif part.put is not None and part.put.done():
                    self._finish(part, self._outcome(part.put, "store", job.job_id))
        while self._copying and self._copying[0].event.query():
            self._finish(self._copying.popleft(), True)
        while self._storing and self._storing[0].event.query():
            part = self._storing.popleft()
            part.put = self._put_pool.submit(self._put, self._groups[part.group], part)
        self._start_waiting()

    def get_finished(self) -> list[TransferResult]:
        self._progress()
        out, self._results = self._results, []
        return out

    def _pages_free(self, job_id: int) -> bool:
        """Whether a job no longer reads or writes KV pages: a load once it
        is complete, a store once every part's copy out has completed."""
        job = self._jobs.get(job_id)
        if job is None:
            return True
        if not job.store:
            return False
        return all(p.done or (p.event is not None and p.event.query()) for p in job.parts)

    def wait(self, job_ids: set[int]) -> None:
        # Results stay for get_finished.
        while not all(self._pages_free(j) for j in job_ids):
            self._progress()
            time.sleep(0.0002)

    def shutdown(self) -> None:
        self._fetch_pool.shutdown(wait=True)
        self._copier.synchronize()
        self._progress()  # PUTs of completed copies
        self._put_pool.shutdown(wait=True)
        for g in self._groups:
            g.client.close()


class _Copier:
    """Batched pointer copies on this worker's own CUDA stream, in issue
    order, through vLLM's `swap_blocks_batch` (cuMemcpyBatchAsync on CUDA
    12.8+, any direction)."""

    def __init__(self, torch, swap_blocks_batch):
        self._torch = torch
        self._swap = swap_blocks_batch
        self._order_arg = "is_src_access_order_any" in inspect.signature(swap_blocks_batch).parameters
        self._stream = torch.cuda.Stream()
        self._lock = threading.Lock()

    def mark(self):
        """An event after the work issued so far on the caller's stream."""
        ev = self._torch.cuda.Event()
        ev.record(self._torch.cuda.current_stream())
        return ev

    def copy(self, batches, after):
        """Issue every (src, dst, sizes) batch of address arrays, after the
        event `after` (if any); returns a CUDA event that completes after
        them."""
        torch = self._torch
        # np.array copies: broadcast views are read-only, torch wants writable.
        descs = [[torch.from_numpy(np.array(a, dtype=np.int64).reshape(-1)) for a in b] for b in batches]
        with self._lock:
            if after is not None:
                self._stream.wait_event(after)
            with torch.cuda.stream(self._stream):
                for src, dst, sizes in descs:
                    if src.numel() == 0:
                        continue
                    if self._order_arg:
                        self._swap(src, dst, sizes, is_src_access_order_any=False)
                    else:
                        self._swap(src, dst, sizes)
                ev = torch.cuda.Event()
                ev.record(self._stream)
            return ev

    def synchronize(self) -> None:
        self._stream.synchronize()


def _current_vllm_config():
    try:
        from vllm.config import get_current_vllm_config_or_none
    except ImportError:
        return None
    return get_current_vllm_config_or_none()


def check_load_failure_policy(vllm_config) -> None:
    """An nvstor load can fail in normal operation (a block evicted after
    its lookup); vLLM must then recompute, not fail the request (its
    default, kv_load_failure_policy "fail")."""
    ktc = getattr(vllm_config, "kv_transfer_config", None)
    policy = getattr(ktc, "kv_load_failure_policy", None)
    if policy is None:
        # vLLM builds the spec outside its config context, so the policy
        # cannot be checked here: say what it must be.
        logger.warning("NvstorSpec: cannot verify kv_load_failure_policy here; it must be \"recompute\" in "
                       "--kv-transfer-config, or a failed load fails its request")
    elif policy != "recompute":
        raise ValueError(f"NvstorSpec: kv_load_failure_policy is {policy!r}: set it to \"recompute\" in "
                         "--kv-transfer-config (a failed load must be recomputed, not fail its request)")


class NvstorSpec(OffloadingSpec):
    def __init__(self, config: OffloadingConfig):
        super().__init__(config)
        if config.parallel.world_size != 1:
            raise ValueError("NvstorSpec supports one GPU worker per engine (data parallel); "
                             f"this engine has {config.parallel.world_size}")
        extra = dict(self.extra_config)
        check_load_failure_policy(_current_vllm_config())
        if int(extra.get("sync_load_budget_bytes", 0) or 0) > 0:
            # vLLM stops the engine when a sync load fails, and an nvstor load
            # can fail in normal operation (an eviction after the lookup).
            raise ValueError("NvstorSpec: loads must stay asynchronous; unset sync_load_budget_bytes")
        self._s = _Settings.parse(extra, kv_identity(config), config.engine_id)
        if extra.get("nvstor_namespace") is None:
            logger.warning("NvstorSpec: no nvstor_namespace; engine %s shares blocks only with itself",
                           config.engine_id)
        self._manager: NvstorManager | None = None
        self._worker: NvstorWorker | None = None

    def get_manager(self) -> OffloadingManager:
        if self._manager is None:
            lookup_client = kv.KvClient.open_lookup(self._s.host, self._s.port, self._s.rails,
                                                    connections=self._s.lookup_connections, devices=self._s.devices)
            lookups = _Lookups(self._s, lambda keys: lookup_client.head(self._s.namespace, keys))
            self._manager = NvstorManager(self._s, lookups, self.kv_events_config.enable_kv_cache_events,
                                           on_shutdown=lookup_client.close)
            logger.info("NvstorSpec: lookups at %s:%d, namespace %s", self._s.host, self._s.port,
                        self._s.namespace.hex())
        return self._manager

    def get_worker(self, kv_caches: CanonicalKVCaches) -> OffloadingWorker:
        if self._worker is None:
            self._worker = self._create_worker(kv_caches)
        return self._worker

    def _create_worker(self, kv_caches: CanonicalKVCaches) -> NvstorWorker:
        import torch
        from vllm import _custom_ops as ops

        bpc = self.blocks_per_chunk
        slots = self._s.landing_slots
        device = torch.cuda.current_device()
        tensors = [t.tensor.view(torch.int8).view((-1, t.page_size_bytes)) for t in kv_caches.tensors]
        # The GPU specs list the offloaded groups in config.groups' order;
        # keys carry each group's KV-cache group id.
        group_ids = [g.group_id for g in self.config.groups]
        groups, memory = [], []
        for g_idx, refs in enumerate(kv_caches.group_data_refs):
            pages = [r.page_size_bytes * bpc for r in refs]
            buf, ptr = kv.allocate_landing(torch, device, pages, slots)
            payload = sum(pages)
            staging = torch.empty(slots * payload, dtype=torch.uint8, pin_memory=True)
            memory += [buf, staging]
            client = kv.KvClient(self._s.host, self._s.port, self._s.rails, gpu=device, landing_ptr=ptr, pages=pages,
                                 slots=slots, connections=self._s.connections, devices=self._s.devices)
            arr = lambda xs: np.asarray(xs, dtype=np.int64)
            groups.append(_Group(
                client=client,
                gpu_base=arr([tensors[r.tensor_idx].data_ptr() for r in refs]),
                gpu_stride=arr([tensors[r.tensor_idx].stride(0) for r in refs]),
                page=arr([r.page_size_bytes for r in refs]),
                landing=arr([ptr + off for off in kv.landing_offsets(pages, slots)]),
                payload_off=arr(np.cumsum([0] + pages[:-1])),
                payload=payload,
                staging=staging.data_ptr()))
            logger.info("NvstorSpec: group %d (id %s): %d landing slots of %d bytes (%d pages) on GPU %d", g_idx,
                        group_ids[g_idx] if g_idx < len(group_ids) else "?", slots, payload, len(pages), device)
        worker = NvstorWorker(self._s, groups, bpc, _Copier(torch, ops.swap_blocks_batch), group_ids)
        # The landing and staging memory lives as long as the worker; its
        # clients are closed in shutdown, before it can be freed.
        worker._memory = memory
        logger.info("NvstorSpec: %s:%d, %d rails, namespace %s", self._s.host, self._s.port, self._s.rails,
                    self._s.namespace.hex())
        return worker
