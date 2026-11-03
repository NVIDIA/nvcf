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

"""Layout probe: what an nvstor KV spec gets from vLLM, and whether the
KV cache memory can be registered for RDMA.

An OffloadingSpec that stores and serves nothing (every lookup misses,
every store is declined). When vLLM hands a GPU worker its canonical KV
caches, the probe records, per worker:

- the offloading config: groups, tokens per block, layers, KV bytes per
  block, blocks per chunk, parallel layout;
- every KV tensor: shape, dtype, page size, address and size;
- per group, the layers' tensor references and page sizes, so the bytes
  of one block are known;
- each distinct CUDA allocation behind the tensors, and the page-aligned
  span the KV tensors themselves cover (the range a KV spec would register; for
  VMM memory the allocation range can include unmapped space): whether
  the driver calls it GPUDirect RDMA capable, whether it exports as a
  dma-buf, and whether every RDMA device in the container registers that
  dma-buf (ibv_reg_dmabuf_mr), with the time taken.

The report is one JSON file per worker in `report_dir` (extra config,
default /tmp/nvstor-kv-probe), named after the engine and rank, and a
one-line summary in vLLM's log. Load it with

    "kv_connector_extra_config": {"spec_name": "NvstorProbeSpec",
        "spec_module_path": "nvstor_kv.probe", "report_dir": "/tmp/probe"}

Written against vLLM v0.32.0 (vllm/v1/kv_offload/base.py).
"""

from __future__ import annotations

import ctypes
import json
import logging
import os
import time
from collections.abc import Collection
from typing import Any

from vllm.v1.kv_offload.base import (
    CanonicalKVCaches,
    GPULoadStoreSpec,
    LoadStoreSpec,
    LookupResult,
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

logger = logging.getLogger(__name__)

# cuda.h
_CU_POINTER_ATTRIBUTE_RANGE_START_ADDR = 11
_CU_POINTER_ATTRIBUTE_RANGE_SIZE = 12
_CU_POINTER_ATTRIBUTE_IS_GPU_DIRECT_RDMA_CAPABLE = 15
_CU_MEM_RANGE_HANDLE_TYPE_DMA_BUF_FD = 1
# verbs.h: IBV_ACCESS_LOCAL_WRITE | IBV_ACCESS_REMOTE_WRITE | IBV_ACCESS_REMOTE_READ
_IBV_ACCESS = 1 | 2 | 4


class _Cuda:
    """The few driver calls the probe needs, through libcuda directly."""

    def __init__(self) -> None:
        self.lib = ctypes.CDLL("libcuda.so.1")
        self.lib.cuPointerGetAttribute.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_uint64]
        self.lib.cuMemGetHandleForAddressRange.argtypes = [
            ctypes.c_void_p, ctypes.c_uint64, ctypes.c_size_t, ctypes.c_int, ctypes.c_ulonglong]
        self.lib.cuGetErrorName.argtypes = [ctypes.c_int, ctypes.POINTER(ctypes.c_char_p)]

    def _name(self, rc: int) -> str:
        s = ctypes.c_char_p()
        if self.lib.cuGetErrorName(rc, ctypes.byref(s)) == 0 and s.value:
            return s.value.decode()
        return f"CUresult {rc}"

    def attr(self, ptr: int, attribute: int) -> int:
        out = ctypes.c_uint64(0)
        rc = self.lib.cuPointerGetAttribute(ctypes.byref(out), attribute, ptr)
        if rc != 0:
            raise OSError(f"cuPointerGetAttribute({attribute}): {self._name(rc)}")
        return out.value

    def dmabuf_fd(self, ptr: int, size: int) -> int:
        fd = ctypes.c_int(-1)
        rc = self.lib.cuMemGetHandleForAddressRange(
            ctypes.byref(fd), ptr, size, _CU_MEM_RANGE_HANDLE_TYPE_DMA_BUF_FD, 0)
        if rc != 0:
            raise OSError(f"cuMemGetHandleForAddressRange: {self._name(rc)}")
        return fd.value


class _Verbs:
    """Device enumeration and dma-buf registration through libibverbs."""

    def __init__(self) -> None:
        self.lib = ctypes.CDLL("libibverbs.so.1", use_errno=True)
        L = self.lib
        L.ibv_get_device_list.restype = ctypes.POINTER(ctypes.c_void_p)
        L.ibv_get_device_list.argtypes = [ctypes.POINTER(ctypes.c_int)]
        L.ibv_free_device_list.argtypes = [ctypes.POINTER(ctypes.c_void_p)]
        L.ibv_get_device_name.restype = ctypes.c_char_p
        L.ibv_get_device_name.argtypes = [ctypes.c_void_p]
        L.ibv_open_device.restype = ctypes.c_void_p
        L.ibv_open_device.argtypes = [ctypes.c_void_p]
        L.ibv_close_device.argtypes = [ctypes.c_void_p]
        L.ibv_alloc_pd.restype = ctypes.c_void_p
        L.ibv_alloc_pd.argtypes = [ctypes.c_void_p]
        L.ibv_dealloc_pd.argtypes = [ctypes.c_void_p]
        L.ibv_reg_dmabuf_mr.restype = ctypes.c_void_p
        L.ibv_reg_dmabuf_mr.argtypes = [ctypes.c_void_p, ctypes.c_uint64, ctypes.c_size_t,
                                        ctypes.c_uint64, ctypes.c_int, ctypes.c_int]
        L.ibv_dereg_mr.argtypes = [ctypes.c_void_p]

    def register_everywhere(self, fd: int, ptr: int, size: int) -> list[dict[str, Any]]:
        """Register the dma-buf on every device, then release it again."""
        n = ctypes.c_int(0)
        devs = self.lib.ibv_get_device_list(ctypes.byref(n))
        if not devs:
            return [{"error": f"ibv_get_device_list: errno {ctypes.get_errno()}"}]
        out: list[dict[str, Any]] = []
        try:
            for i in range(n.value):
                out.append(self._register_one(devs[i], fd, ptr, size))
        finally:
            self.lib.ibv_free_device_list(devs)
        return out

    def _register_one(self, dev: int, fd: int, ptr: int, size: int) -> dict[str, Any]:
        raw = self.lib.ibv_get_device_name(dev)
        rec: dict[str, Any] = {"device": raw.decode() if raw else "?"}
        ctx = pd = mr = None
        try:
            ctx = self.lib.ibv_open_device(dev)
            if not ctx:
                rec["error"] = f"ibv_open_device: errno {ctypes.get_errno()}"
                return rec
            pd = self.lib.ibv_alloc_pd(ctx)
            if not pd:
                rec["error"] = f"ibv_alloc_pd: errno {ctypes.get_errno()}"
                return rec
            t0 = time.perf_counter()
            mr = self.lib.ibv_reg_dmabuf_mr(pd, 0, size, ptr, fd, _IBV_ACCESS)
            rec["reg_s"] = round(time.perf_counter() - t0, 4)
            rec["registered"] = bool(mr)
            if not mr:
                err = ctypes.get_errno()
                rec["error"] = f"ibv_reg_dmabuf_mr: {os.strerror(err)} ({err})"
            return rec
        finally:
            # Release in reverse order and record any failure: a leaked MR
            # would stay attached to the engine's KV memory.
            if mr:
                t1 = time.perf_counter()
                rc = self.lib.ibv_dereg_mr(mr)
                rec["dereg_s"] = round(time.perf_counter() - t1, 4)
                if rc != 0:
                    rec["release_error"] = f"ibv_dereg_mr: {os.strerror(rc)} ({rc})"
            if pd and self.lib.ibv_dealloc_pd(pd) != 0:
                rec.setdefault("release_error", "ibv_dealloc_pd failed")
            if ctx and self.lib.ibv_close_device(ctx) != 0:
                rec.setdefault("release_error", "ibv_close_device failed")


def _extent(t: Any, page_size_bytes: int) -> int:
    """Bytes from a (num_blocks, ...) tensor's first byte to its last page's
    end; the leading stride may exceed the page (packed layouts)."""
    n = t.shape[0]
    return (n - 1) * t.stride(0) * t.element_size() + page_size_bytes if n else 0


def _kv_span(kv_caches: CanonicalKVCaches) -> dict[str, Any]:
    """The page-aligned range covering every KV tensor."""
    page = os.sysconf("SC_PAGE_SIZE")
    lo = min(t.tensor.data_ptr() for t in kv_caches.tensors)
    hi = max(t.tensor.data_ptr() + _extent(t.tensor, t.page_size_bytes) for t in kv_caches.tensors)
    lo -= lo % page
    hi += -hi % page
    return {"start": hex(lo), "size": hi - lo, "tensors": list(range(len(kv_caches.tensors)))}


def _allocations(kv_caches: CanonicalKVCaches, cuda: _Cuda | None) -> list[dict[str, Any]]:
    """The distinct CUDA allocations behind the KV tensors."""
    ranges: dict[int, dict[str, Any]] = {}
    for i, t in enumerate(kv_caches.tensors):
        ptr = t.tensor.data_ptr()
        entry: dict[str, Any]
        if cuda is None:
            ranges.setdefault(-1, {"error": "libcuda not loaded", "tensors": []})["tensors"].append(i)
            continue
        try:
            start = cuda.attr(ptr, _CU_POINTER_ATTRIBUTE_RANGE_START_ADDR)
            size = cuda.attr(ptr, _CU_POINTER_ATTRIBUTE_RANGE_SIZE)
        except OSError as e:
            ranges.setdefault(-2 - i, {"error": str(e), "tensors": []})["tensors"].append(i)
            continue
        entry = ranges.setdefault(start, {"start": hex(start), "size": size, "tensors": []})
        entry["tensors"].append(i)
        if not (start <= ptr and ptr + _extent(t.tensor, t.page_size_bytes) <= start + size):
            entry.setdefault("tensors_outside_range", []).append(i)
    return list(ranges.values())


def _probe_allocation(entry: dict[str, Any], cuda: _Cuda, verbs: _Verbs | None) -> None:
    start, size = int(entry["start"], 16), entry["size"]
    try:
        entry["gpudirect_rdma_capable"] = bool(
            cuda.attr(start, _CU_POINTER_ATTRIBUTE_IS_GPU_DIRECT_RDMA_CAPABLE))
    except OSError as e:
        entry["gpudirect_rdma_capable"] = str(e)
    try:
        fd = cuda.dmabuf_fd(start, size)
    except OSError as e:
        entry["dmabuf"] = str(e)
        return
    entry["dmabuf"] = "exported"
    try:
        entry["rdma"] = (verbs.register_everywhere(fd, start, size) if verbs
                         else [{"error": "libibverbs not loaded"}])
    finally:
        os.close(fd)


def _report(config: OffloadingConfig, kv_caches: CanonicalKVCaches) -> dict[str, Any]:
    rep: dict[str, Any] = {
        "engine_id": config.engine_id,
        "model": {"name": config.model.name, "dtype": config.model.dtype},
        "parallel": {k: getattr(config.parallel, k) for k in (
            "rank", "world_size", "tp_size", "pp_size", "pcp_size", "dcp_size",
            "data_parallel_index", "data_parallel_size", "is_parallelism_agnostic")},
        "worker_kv_bytes_per_block": config.worker_kv_bytes_per_block,
        "tokens_per_hash": config.cache.tokens_per_hash,
        "blocks_per_chunk": config.cache.blocks_per_chunk,
        "groups": [{"group_id": g.group_id, "tokens_per_block": g.tokens_per_block,
                    "layers": len(g.layer_names)} for g in config.groups],
        "pytorch_cuda_alloc_conf": os.environ.get("PYTORCH_CUDA_ALLOC_CONF", ""),
        "tensors": [{"shape": list(t.tensor.shape), "dtype": str(t.tensor.dtype),
                     "page_size_bytes": t.page_size_bytes, "data_ptr": hex(t.tensor.data_ptr()),
                     "extent_bytes": _extent(t.tensor, t.page_size_bytes),
                     "contiguous": t.tensor.is_contiguous(), "device": str(t.tensor.device)}
                    for t in kv_caches.tensors],
        "group_refs": [{"layers": len(refs),
                        "distinct_tensors": len({r.tensor_idx for r in refs}),
                        "page_bytes_per_block": sum(r.page_size_bytes for r in refs),
                        "page_sizes": sorted({r.page_size_bytes for r in refs}),
                        "canonical_mapping": any(r.mapping is not None for r in refs)}
                       for refs in kv_caches.group_data_refs],
    }
    cuda: _Cuda | None = None
    verbs: _Verbs | None = None
    try:
        cuda = _Cuda()
    except OSError as e:
        rep["cuda_error"] = str(e)
    try:
        verbs = _Verbs()
    except (OSError, AttributeError) as e:
        rep["verbs_error"] = str(e)
    allocs = _allocations(kv_caches, cuda)
    span = _kv_span(kv_caches)
    if cuda is not None:
        for entry in allocs:
            if "start" in entry:
                _probe_allocation(entry, cuda, verbs)
        _probe_allocation(span, cuda, verbs)
    rep["allocations"] = allocs
    rep["kv_span"] = span
    return rep


class _ProbeManager(OffloadingManager):
    """Scheduler side: nothing is ever offloaded, so nothing is ever found."""

    def lookup(self, key: OffloadKey, req_context: ReqContext) -> LookupResult:
        return LookupResult.MISS

    def prepare_load(self, keys: Collection[OffloadKey], req_context: ReqContext) -> LoadStoreSpec:
        # Unreachable: vLLM loads only keys whose lookup was a hit.
        raise RuntimeError("the nvstor KV probe holds no blocks")

    def prepare_store(self, keys: Collection[OffloadKey],
                      req_context: ReqContext) -> PrepareStoreOutput | None:
        # Nothing to store, not "cannot store": None counts as an allocation
        # failure in vLLM's scheduler, which warns and retries every step.
        return PrepareStoreOutput(keys_to_store=[], store_spec=LoadStoreSpec(), evicted_keys=[])

    def on_new_request(self, req_context: ReqContext) -> RequestOffloadingContext:
        return RequestOffloadingContext()


class _ProbeWorker(OffloadingWorker):
    """Worker side: never given a job, since the manager stores and finds
    nothing. vLLM asserts on a False return, so a job is a clear error."""

    def submit_store(self, job_id: int, src_spec: GPULoadStoreSpec, dst_spec: LoadStoreSpec) -> bool:
        raise RuntimeError("the nvstor KV probe stores nothing")

    def submit_load(self, job_id: int, src_spec: LoadStoreSpec, dst_spec: GPULoadStoreSpec) -> bool:
        raise RuntimeError("the nvstor KV probe holds no blocks")

    def get_finished(self) -> list[TransferResult]:
        return []

    def wait(self, job_ids: set[int]) -> None:
        return


class NvstorProbeSpec(OffloadingSpec):
    def __init__(self, config: OffloadingConfig):
        super().__init__(config)
        self._worker: _ProbeWorker | None = None
        self._report_dir = str(self.extra_config.get("report_dir", "/tmp/nvstor-kv-probe"))

    def get_manager(self) -> OffloadingManager:
        return _ProbeManager()

    def get_worker(self, kv_caches: CanonicalKVCaches) -> OffloadingWorker:
        if self._worker is None:
            self._write(kv_caches)
            self._worker = _ProbeWorker()
        return self._worker

    def _write(self, kv_caches: CanonicalKVCaches) -> None:
        # The probe must never take the engine down: a failure is reported.
        try:
            rep = _report(self.config, kv_caches)
        except Exception as e:  # noqa: BLE001 - recorded, not swallowed
            rep = {"engine_id": self.config.engine_id, "probe_error": repr(e)}
        name = f"probe-{self.config.engine_id}-rank{self.config.parallel.rank}.json"
        path = os.path.join(self._report_dir, "".join(c if c.isalnum() or c in "-_." else "_" for c in name))
        try:
            os.makedirs(self._report_dir, exist_ok=True)
            with open(path, "w") as f:
                json.dump(rep, f, indent=1)
        except OSError as e:
            logger.error("nvstor_kv probe: cannot write %s: %s", path, e)
            return
        regs = [r for a in rep.get("allocations", []) + [rep.get("kv_span", {})] for r in a.get("rdma", [])]
        logger.info("nvstor_kv probe: %d tensors, %d allocations, %d/%d RDMA registrations; report %s",
                    len(rep.get("tensors", [])), len(rep.get("allocations", [])),
                    sum(1 for r in regs if r.get("registered")), len(regs), path)
