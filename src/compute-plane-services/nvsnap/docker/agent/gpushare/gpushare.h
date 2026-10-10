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
 * allocation, and cuIpc*MemHandle use our own handles for it; smaller
 * (legacy) memory gets our handle too, and the driver export is made only
 * when a peer opens it (a legacy export crashes the driver checkpoint). Each
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
 *            a TP worker holding many fails with OUT_OF_MEMORY); likewise
 *            the cuMem allocations the app created and shares, mapped
 *            once from offset 0 (re-created by "load" with the same
 *            properties and access).
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
 * "quiesce". Exporters are found among the processes on this node.
 *
 * A driver restore drops the IMEX channel subscription of libcuda's
 * clients; "load" subscribes them again. Unless NVSNAP_GPUSHARE_FABRIC=1,
 * fabric support is hidden from the workload, which then shares memory as
 * POSIX fds. With it (multi-node NVLink), memory from other nodes is
 * imported by fabric handle, and fabric handles change across a restore:
 *   fabmap <file>  (after "load")  write the fabric handles this process
 *            exported again at "load", old -> new.
 *   remap <file>  re-import memory from other nodes by its new handle, from
 *            the lists of all nodes merged into file.
 * nvsnap-gpu-suspend --fabric-map DIR exchanges the lists and has the nodes
 * vote, so that every node is quiesced before any releases.
 *
 * Multicast (NVLS) needs a driver that lets a restored process create and
 * join multicast objects: 610.57.04 on GB300 does, 580.173.02 does not.
 *
 * A process may drive several GPUs: peer access the app enables
 * (cuCtxEnablePeerAccess) is granted on the cuMem memory behind cuMemAlloc,
 * and each allocation is saved and re-created on its own device.
 *
 * Messages are SOCK_SEQPACKET text, replies start with "ok" or "err". The
 * control thread serves only peers in its pid namespace running as root or
 * as its user, and clients talk only to the process a socket is named for
 * (SO_PEERCRED): abstract sockets have no permissions.
 */
#ifndef NVSNAP_GPUSHARE_H
#define NVSNAP_GPUSHARE_H

/* Abstract socket name (without the leading NUL), per pid. */
#define GPUSHARE_SOCK_FMT "nvsnap-gpushare.%d"
/* Control thread name (/proc/<pid>/task/<tid>/comm): must not be frozen. */
#define GPUSHARE_THREAD "nvsnap-gpushare"

#endif
