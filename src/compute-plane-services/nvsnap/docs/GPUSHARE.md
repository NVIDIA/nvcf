<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# gpushare: checkpointing GPU memory shared between processes

`cuda-checkpoint` cannot checkpoint a process that maps GPU memory imported from
another process. That rules out multi-GPU (tensor-parallel) workloads in their
default configuration, because the following all share memory across processes:

- NCCL peer-to-peer over cuMem imports;
- NCCL NVLS multicast objects;
- CUDA IPC, as used by vLLM custom all-reduce and PyTorch;
- fabric handles;
- page-locked host memory on Grace.

`libnvsnap_gpushare.so` is an LD_PRELOAD shim that tracks this memory. It
releases the memory before the driver checkpoint and re-creates it at the same
virtual addresses after restore, so pointers and captured CUDA graphs stay
valid. `nvsnap-gpu-suspend` drives the shim and the CUDA checkpoint API.

The sources are under `docker/agent/gpushare/`. Both binaries are built in the
agent base image (`Dockerfile.base`, `gpushare-builder` stage, CUDA 13 headers)
and installed in `/criu-bundle/`. Tests are under `tests/gpushare/`.
`make check-gpushare` compiles both binaries and the tests for amd64 and arm64
with warnings as errors, in the same CUDA image (Docker, no GPU); the
`nvsnap gpushare` workflow runs the same check in CI.

## How it works

Each process that loads the shim runs a control thread on the abstract socket
`@nvsnap-gpushare.<pid>`. `nvsnap-gpu-suspend` sends it these commands
(details in `gpushare.h`):

| Command | Who | What it does |
| --- | --- | --- |
| `quiesce` | all pids, before any release | Holds kernel and graph launches, copies, memsets and stream memory operations; drains the GPU; checks that everything shared can be released. |
| `release` | all pids at once | Unmaps imported memory and multicast objects and unregisters host memory. Saves the shim's own cuMem allocations, to host memory or to a chunk store. |
| *(driver)* | | `cuCheckpointProcessLock` / `Checkpoint`, in parallel. |
| `load` | all pids at once, after restore | Re-creates the saved allocations at the same addresses. |
| `remap` | each pid | Re-imports peers' memory and rebuilds multicast objects. |
| `resume` | each pid | Binds multicast memory and reopens launches. |

If any step of `suspend` fails, the tool rolls back: it undoes the driver
lock or checkpoint, then `load`, `remap` and `resume` restore exactly what
`release` had dropped, so the workload runs on as before. The tool says
whether the rollback worked.

The shim backs `cuMemAlloc` of 2 MiB or more with exportable cuMem allocations,
so that CUDA IPC works across a restore. cuMem memory is reachable only from
the devices granted with `cuMemSetAccess`, so the shim also follows
`cuCtxEnablePeerAccess` and `cuCtxDisablePeerAccess`, granting each peer
access to the allocations it covers. A process that drives several GPUs
keeps its peer access, and each allocation is saved and loaded on its own
device.

### Control socket access

Abstract sockets have no file permissions: any process in the pod's network
namespace can connect, which includes sidecars and, with `hostNetwork`, the
whole node. The control thread therefore checks the peer's credentials
(`SO_PEERCRED`). It serves only a peer that is in the workload's pid
namespace and runs as root or as the workload's user. Clients, both the
shim's own requests to other processes and `nvsnap-gpu-suspend`, talk to a
socket only if it belongs to the pid in its name. Two consequences:

- `nvsnap-gpu-suspend` must run in the workload's pid and network
  namespaces, for example through `kubectl exec` or `nsenter -t <pid> -p -n`.
- All processes that share GPU memory must be in one pid namespace.

## Usage

The workload runs with `LD_PRELOAD=/path/to/libnvsnap_gpushare.so`. Pass
`nvsnap-gpu-suspend` every pid of the workload: the API server, the engine and
all tensor-parallel ranks.

```sh
# In place: free the GPUs, keep the processes paused, then resume.
nvsnap-gpu-suspend suspend <pids...>
nvsnap-gpu-suspend resume <pids...>

# For CRIU: save GPU memory to a chunk store, hand off to CRIU, and restore
# elsewhere. Record which GPUs the checkpoint used, for --gpu-map.
nvsnap-gpu-suspend gpus /ckpt/<id>/gpus
nvsnap-gpu-suspend --store /ckpt/store --ckpt-dir /ckpt/<id> --cache /cache suspend <pids...>
nvsnap-gpu-suspend stop <pids...>          # then: criu dump
# ... criu restore (in a new pod or on another node) ...
nvsnap-gpu-suspend --gpu-map /ckpt/<id>/gpus --cache /cache resume <pids...>

# Node cache: fill it, ahead of a restore or in the background after one,
# and evict from it.
nvsnap-gpu-suspend cache-prefetch /ckpt/store /ckpt/<id> /cache
nvsnap-gpu-suspend cache-gc /cache <max-GiB>
```

### Chunk store

The shim stores the GPU memory it saves as 64 MiB content-addressed chunks
under `<store>/chunks/xx/<hash>`:

- A chunk that is already in the store is not written again. Model weights
  repeat across checkpoints of the same model, so they are stored once.
- All-zero chunks are not stored at all.
- Each chunk is written to a temporary file with a random name, created
  exclusively, then flushed with `fdatasync` and renamed. Writers in other
  pods can share a store or a cache, and a crash never leaves a chunk name
  pointing at partial data.
- Chunk files use `O_DIRECT`. If a filesystem accepts `O_DIRECT` but fails
  the I/O, the shim falls back to buffered I/O.

`release` creates the store and `<ckpt-dir>` if they do not exist (their
parent directories must). `<ckpt-dir>/gpu-<pid>.chunks` lists the chunks
each checkpoint uses, for accounting and garbage collection. The CRIU image
then holds only host process memory.

The optional node cache (`--cache`) is a local directory with the same layout.
Saves copy chunks into it, and loads read the cache first. A load that misses
the cache reads the store and does not fill the cache, because cache writes
would slow the restore down. `cache-prefetch` fills it instead, and
`cache-gc` evicts least-recently-used chunks.

Chunks are named by a fast 128-bit hash, which is not cryptographic, and a
load trusts the name. Whoever can write to a store or cache can therefore
make another workload restore wrong data. A cryptographic hash alone would
not prevent that: the writer could still store wrong data under the right
name. So:

- Only a workload whose checkpoint every restoring workload already trusts
  may write to a store. Restores should mount the store read-only. For
  example, a store inside a checkpoint's own volume, written by the
  checkpointed pod and mounted read-only by the pods restored from it,
  adds no trust beyond the checkpoint itself.
- Workloads whose checkpoints are not trusted by the same restores need
  separate stores. Chunks are then not deduplicated between those stores.
- The node cache is optional (`--cache`); without it, loads read the store
  directly. If it is used, a pod saving with `--cache` writes to it, so
  give each such store its own cache directory, or let only a trusted
  process fill it with `cache-prefetch`, mount it read-only in workloads,
  and pass `--cache` to `resume` only.

## Use through nvsnap

Annotate the pod `nvsnap.io/gpushare: "true"`. nvsnap then does the steps in
Usage itself, around its criu-v2 capture and restore.

```mermaid
flowchart LR
  A[admission: webhook] -->|shim in LD_PRELOAD, bundle at /nvsnap, per-pod store| B[workload runs]
  B --> C[capture: agent]
  C -->|nvsnap-gpu-suspend suspend, stop| D[plain CRIU dump]
  D -->|store moved into the checkpoint| E[node-local checkpoint]
  E --> F[restore: placeholder]
  F -->|CRIU restore, then resume --gpu-map| G[serving]
```

At admission, the webhook changes every container that requests GPUs:

- mounts the node bundle (staged by the agent DaemonSet) read-only at
  `/nvsnap` and appends `/nvsnap/libnvsnap_gpushare.so` to `LD_PRELOAD`,
  keeping any value the pod sets;
- mounts a per-pod directory of the node's checkpoint disk at
  `/nvsnap-gpushare` (the chunk store), using the pod uid as the
  subdirectory so pods never see each other's saved memory.

At capture, the agent finds the processes of the dumped session that load the
shim, runs `nvsnap-gpu-suspend gpus`, `suspend` and `stop` inside the pod's
namespaces, and dumps with CRIU without the CUDA plugin and with
`--image-io-mode direct`. It then moves the chunk store into the checkpoint
(`<checkpoint>/gpushare`) and records the pids, the store path and the shim
path in the checkpoint metadata. A process that uses the GPU without the shim
fails the capture. Multi-GPU captures are accepted when the shim is loaded.

At restore, the placeholder must mount the bundle at the shim's path and the
checkpoint's `gpushare` directory at `/nvsnap-gpushare`. The agent's
generated placeholder and the e2e templates do both. The agent runs CRIU,
then `nvsnap-gpu-suspend resume` with the checkpoint's GPU map, limited to the
GPUs the placeholder was allocated (`NVIDIA_VISIBLE_DEVICES`).

Limits of the integration:

- An `LD_PRELOAD` set from a ConfigMap or Secret reference cannot be extended
  at admission; that container is left without the shim.
- An `LD_PRELOAD` set only by the image (Dockerfile `ENV`) is not visible at
  admission and is replaced by the shim.
- One capture per workload at a time; a second request is refused.
- Per-pod store directories are emptied when a capture collects them, not
  deleted when the pod goes away.
- `<checkpoint root>/gpushare-pods/` holds the stores of running pods. A
  cleanup of the checkpoint root must keep it: deleting a pod's directory
  leaves its store mount pointing at an unlinked directory, and that pod's
  next capture fails with "No such file or directory".
- Only processes that load the shim and use the GPU are suspended. The API
  server and helper processes inherit the preload but hold no CUDA state;
  CRIU dumps them as they are.

### Workloads that span nodes

A workload whose ranks share memory over multi-node NVLink runs one pod per
node, and their tools must vote on the quiesce and exchange fabric handles
(see `--fabric-map` under Requirements and limits). nvsnap drives that as a
fabric session:

1. Annotate every pod `nvsnap.io/gpushare-fabric: "true"` as well. The
   webhook sets `NVSNAP_GPUSHARE_FABRIC=1` unless the pod sets it.
2. Ask any agent for a group checkpoint of all the pods:

   ```bash
   curl -X POST http://<agent>:8081/v1/gpushare/group-checkpoint \
     -H "Authorization: Bearer $TOKEN" -d '{
       "leaveRunning": true,
       "members": [
         {"namespace": "llm", "podName": "vllm-0"},
         {"namespace": "llm", "podName": "vllm-1"}
       ]}'
   ```

That agent starts each member's checkpoint on the member's node with a new
session id. Each member's agent creates a fresh directory for the session in
the pod's store, passes it to `suspend` and `resume` as `--fabric-map`, and
serves it at `/v1/gpushare/fabric/<session>/<namespace>/<pod>`. The driving
agent polls those, answers each vote round (`go` once every member is
quiesced, `retry` otherwise), and once every member has written its handle
list, delivers the concatenated lists to all of them, once. A fresh
directory per session means a list from an earlier cycle is never merged.

Limits:

- `leaveRunning` is required: a member stopped by its dump cannot take part
  in the others' resume.
- If a member's checkpoint ends before the vote passes, every later vote is
  answered `abort`. The tool treats that as `retry` and rolls the workload
  back at its own timeout (`--timeout-ms`, 120 s here), not at once.
- A member whose dump fails still resumes, and takes part in the exchange.
  A member whose checkpoint returns without writing a handle list is left
  out of it; the others resume among themselves.
- Restoring a session's checkpoints on other nodes is not covered: the pods
  get new addresses and NCCL's connections between nodes have to be
  re-established.

## Requirements and limits

- Driver: 610 or later (validated: 610.57.04). Driver 580 cannot restore
  multicast (NVLS) objects: after a restore, `cuMulticastAddDevice` fails
  (GB300 with 580.173.02, H100 with 580.126.16), so tensor-parallel vLLM on
  NVSwitch systems fails at `remap`. NCCL NVLS, PyTorch symmetric memory
  and FlashInfer's all-reduce fusion all use multicast. On x86 with
  580.126.16 (RTX PRO 6000), the driver's own restore of processes that
  share GPU memory with each other (vLLM with TP=2) also fails, with
  `CUDA_ERROR_UNKNOWN`.
- Exported fds the app keeps: after `release`, every fd in the process that
  refers to memory it exported is pointed at `/dev/null` (the fd numbers
  stay valid), so that CRIU can dump the process. FlashInfer keeps such fds
  on H100. Fds the app received from another process and kept are not
  covered.
- IMEX and fabric handles: fabric handles need the process's driver
  clients to be subscribed to an IMEX channel. libcuda subscribes once; a
  driver restore closes the channel fd and re-creates the clients without
  the subscription, and libcuda does not subscribe again, so fabric create,
  import and multicast fail with `NOT_PERMITTED`, and the memory exports
  the restore re-creates cannot be imported by other processes. The shim
  watches libcuda's ioctls: it subscribes each root client the restore
  creates as soon as it is created
  (`NV0000_CTRL_CMD_CLIENT_SUBSCRIBE_TO_IMEX_CHANNEL`, the call libcuda
  makes), and again at `load`. Imports from other nodes are signalled on an
  OS event, a GPU device file libcuda opens once; the restore closes it and
  libcuda then passes none, so the shim registers a device file of its own
  for the import and has libcuda wait on it (`poll`/`ppoll` on fd -1), and
  closes it at `release`. Memory shared as fabric handles is copied into a
  new allocation at `load`, before it is exported again. Memory that was created fabric-capable still
  cannot be exported as a POSIX fd after a restore, and with an IMEX
  channel NCCL, PyTorch and FlashInfer create such memory even on one
  node. So by default the shim hides fabric support from the workload:
  `CU_DEVICE_ATTRIBUTE_HANDLE_TYPE_FABRIC_SUPPORTED` reads 0, and fabric
  `cuMemCreate` and `cuMulticastCreate` fail with `NOT_PERMITTED`, as
  without an IMEX channel, and those libraries share memory as POSIX fds.
  `NVSNAP_GPUSHARE_FABRIC=1` keeps fabric support, which multi-node NVLink
  needs; memory shared as fabric handles is then re-imported as fabric
  handles.
- Multi-node NVLink: with `NVSNAP_GPUSHARE_FABRIC=1`, memory and multicast
  objects shared across nodes are imported by fabric handle. Fabric handles
  change across a restore, and a node must not release memory while another
  node's kernels still read it. So `nvsnap-gpu-suspend --fabric-map DIR`
  (on `suspend` and `resume`, on every node) coordinates the nodes through
  whoever drives them, with files in `DIR`:
  - Quiesce vote: each attempt, the tool writes `DIR/q`
    (`<attempt> ok` or `<attempt> busy`) and waits for `DIR/d`. Once every
    node has voted, the driver writes `<attempt> go` to every node if all
    are quiesced, else `<attempt> retry`.
  - Handle exchange: after `load`, the tool writes the node's new fabric
    handles to `DIR/out` and waits for `DIR/in`, the `out` files of all
    nodes concatenated, to re-import from at `remap`.
- Scope: exporters on the same node are found among the processes in the
  same pid namespace.
- Large shared allocations: driver 610.57.04 will not export an allocation
  of 512 MiB or more that a restored process created before its
  checkpoint (`INVALID_VALUE`). FlashInfer's all-reduce workspace is such
  an allocation. When a peer's re-import fails that way, the shim replaces
  the allocation: it copies the contents into a new one at the same
  address and releases the old one, and the app's handle then stands for
  the new one. Only an allocation mapped once, from offset 0, is replaced.
- `hostNetwork`: such pods share the node's abstract socket names. If two of
  them run a process with the same pid, the second cannot bind its control
  socket, and its checkpoint is refused.
- glibc: the shim is built on Ubuntu 22.04 and needs glibc 2.35 or later in
  the workload image.
- Held calls: while suspended, the shim holds the CUDA calls that use device
  memory: launches, copies, memsets and stream memory operations, made
  through the CUDA runtime or the driver's entry-point lookup (PyTorch,
  NCCL and vLLM all are). Copies to or from CUDA arrays, managed-memory
  prefetches and batched copies (`cuMemcpyBatchAsync`) are not held, nor are
  calls a program links directly against `libcuda`.
- State files: `nvsnap-gpu-suspend` keeps its state in
  `/tmp/nvsnap-gpu-suspend`. It refuses to run if that directory exists and
  is not owned by its user with mode 0700, so another user cannot redirect
  its writes.
- Host memory: without `--store`, the GPU memory the shim saves is kept in
  host RAM. Size the pod's memory limit for it.
- Store garbage collection is not implemented. A store only grows: delete a
  checkpoint's directory and the store's chunks that no remaining
  `gpu-*.chunks` list names. Temporary files left in a store by a crash
  are not removed either. `cache-gc` covers the node cache only.

## Validation

All results are with vLLM 0.20.0 at default flags on 4x GB300 with driver
610.57.04, unless noted otherwise. In every case the output after restore
matched the output before the checkpoint byte for byte.

In-place suspend and resume, Qwen2.5-7B-Instruct, TP=4: 3 out of 3 cycles
passed. Each suspend took about 6.5 s, and each resume about 5.9 s. The
same passed in a pod with an IMEX channel (a ComputeDomain), with NCCL
NVLS and FlashInfer's all-reduce fusion active, both by default and with
`NVSNAP_GPUSHARE_FABRIC=1 NCCL_MNNVL_ENABLE=1`, where NCCL, PyTorch and
FlashInfer share memory and multicast objects as fabric handles, as across
nodes.

Checkpoint on one node and restore on another, Qwen2.5-72B-Instruct, TP=4,
with the store on a network block storage PVC and the node cache on local
NVMe:

| Phase | Restore from the store | Restore from the node cache |
| --- | --- | --- |
| New pod to first token | 87.8 s | 66.7 s |
| GPU memory load | 30.1 s | 7.7 s |
| `criu restore` (processes, host memory) | 18.0 s | 18.3 s |

The first checkpoint wrote 159 GiB to the store, and a second checkpoint of
the same model wrote 20 GiB. A cold start of the same model, including a
136 GB model download, took 363 s to the first token, or 242 s without the
download.

Across 2 nodes (4x GB300 each, one NVLink domain, an IMEX channel), 8
ranks running a checked 256 MB NCCL all-reduce with `NCCL_MNNVL_ENABLE=1`
(NVLS multicast across the nodes): in-place suspend and resume with
`--fabric-map`, 3 out of 3 cycles passed, one after a retried vote. vLLM
with Qwen2.5-14B-Instruct, TP=8 across the 2 nodes (`--nnodes 2`,
`NVSNAP_GPUSHARE_FABRIC=1 NCCL_MNNVL_ENABLE=1`): in-place suspend and
resume, output identical to the baseline in 5 out of 6 cycles; in the
other, the test's driver merged a stale `DIR/out` (written by the
previous cycle), and resume succeeded when run again with the right
lists. Whoever drives the nodes must remove `DIR/out` and `DIR/in` before
`resume`.

On 4x H100 with driver 580.126.16, Qwen2.5-7B-Instruct, TP=4 was dumped
with CRIU, restored into a new pod and answered as before, with the three
multicast users off for driver 580 (`NCCL_NVLS_ENABLE=0`,
`VLLM_ALLREDUCE_USE_SYMM_MEM=0`, and `fuse_allreduce_rms` false in the
compilation config's `pass_config`).

The GPU tests in `tests/gpushare/` pass on GB300 (driver 610) and on RTX
PRO 6000 (x86, driver 580). `test_multi_gpu` covers a single process driving
two GPUs with peer access. `test_export_copies` covers an app that keeps the
fds of its own exports, and `test_large_export` a shared 512 MiB
allocation. `test_ipc_release` and `test_cumem_release` are
probes of the driver without the shim. On both platforms, the driver cannot
checkpoint or re-export memory shared through its own CUDA IPC, which is
why the shim replaces it.

To reproduce, run `tests/gpushare/k8s/vllm_criu_bench.sh` (see its header)
and `tests/gpushare/k8s/vllm_ckpt_cycle.sh`.
