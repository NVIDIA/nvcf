/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * nvsnap gpushare — checkpoint support for GPU memory shared between processes.
 *
 * cuda-checkpoint cannot restore a process that maps GPU memory imported
 * from another process: cuMem imports (cuMemImportFromShareableHandle,
 * NCCL P2P by default) fail with CUDA_ERROR_NOT_SUPPORTED, legacy CUDA IPC
 * (cuIpcOpenMemHandle, vLLM custom all-reduce) fails too, and a restored
 * allocation can no longer be IPC-shared at all.
 *
 * libnvsnap_gpushare.so (LD_PRELOAD) tracks every imported mapping, and runs
 * CUDA IPC on cuMem: cuMemAlloc >= 2 MiB is backed by an exportable cuMem
 * allocation, and cuIpc*MemHandle use our own handles for it. Each
 * process that exports or imports runs a control thread on an abstract
 * unix socket; nvsnap-gpu-suspend drives it:
 *
 *   quiesce  (before lock, all pids)  hold kernel/graph launches in a
 *            gate, drain GPU work, check everything below can be released.
 *   release  (all pids quiesced)  unmap + release every imported mapping
 *            (the VA range stays reserved), unmap, unbind and drop
 *            multicast objects (NCCL NVLS), unregister page-locked host
 *            memory (the driver restores none of these on Grace); save
 *            the cuMem allocations backing cuMemAlloc to host memory and
 *            free them, keeping the VA (on Grace the driver's checkpoint of
 *            a TP worker holding many fails with OUT_OF_MEMORY).
 *   remap    (after restore + unlock of all pids)  re-create the saved
 *            allocations at the same VA (or earlier, on a peer's export
 *            request); re-register host memory;
 *            re-import each allocation from its exporter (fresh export,
 *            requested over the exporter's control socket) and map it at
 *            the same VA with the same access, so pointers held by the app
 *            and by captured CUDA graphs stay valid; re-create or re-import
 *            multicast objects and rejoin this process's devices.
 *   resume   (after remap of all pids)  bind multicast memory (blocks until
 *            every device rejoined) and map it; reopen the launch gate.
 *
 * Shared memory is tracked whether it is passed as a POSIX fd or, with an
 * IMEX channel, as a fabric handle (identified by "whosefab", hex). An
 * export made before the memory is mapped (NCCL symmetric buffers) is
 * registered when mapped; importers look the exporter up again at
 * "quiesce". Exporters are found among the processes on this node; memory
 * shared across nodes (multi-node NVLink) is not tracked, and its
 * checkpoint is refused.
 *
 * A restored process may not use fabric handles (driver 610: NOT_PERMITTED),
 * so memory and multicast objects created for fabric handles also allow
 * POSIX fds, and are re-imported as fds. Driver 610 still refuses to export
 * fabric-capable memory after restore: pods with an IMEX channel do not
 * restore yet.
 *
 * Multicast (NVLS) needs a driver that lets a restored process create and
 * join multicast objects: 610.57.04 on GB300 does, 580.173.02 does not.
 *
 * Messages are SOCK_SEQPACKET text, replies start with "ok" or "err".
 */
#ifndef NVSNAP_GPUSHARE_H
#define NVSNAP_GPUSHARE_H

/* Abstract socket name (without the leading NUL), per pid. */
#define GPUSHARE_SOCK_FMT "nvsnap-gpushare.%d"
/* Control thread name (/proc/<pid>/task/<tid>/comm): must not be frozen. */
#define GPUSHARE_THREAD "nvsnap-gpushare"

#endif
