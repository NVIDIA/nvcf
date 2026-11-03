# `fetch --gpu`: source to GPU memory, local disk in the background

User-facing flags, the ready file and the JSON fields are in
[`../peer-copy.md`](../peer-copy.md#fetch-into-gpu-memory-fetch---gpu-n).
This note is the design.

## Goal

A restore needs a checkpoint in GPU memory, fast; a copy on the node's own
disk is wanted too (restart without refetching, serve the next node), but
must not slow the GPU copy. A plain `fetch` lands every range in a host
slot and writes it to disk; a restore then loads the files from disk into
the GPU, paying for the copy twice in sequence.

`nvstor-peer fetch --gpu N --dest DIR`:

1. Every range moves from the source over the rails straight into
   GPU N (GPUDirect RDMA), is checked, and is placed at its
   file's offset in one GPU buffer.
2. The disk copy follows in the background: each worker's disk writer
   reads its ranges back from the GPU buffer (which holds them, checked)
   and writes them to DIR with the usual staging, fsync and rename rules,
   so the GPU copy never waits for the disk. The disk copy is an ordinary
   nvstor-peer copy (content record, attributes, `--then-serve`).
3. When the last range is in the GPU, before the disk is done, the fetch
   writes `--gpu-ready-file`: the buffer's CUDA IPC handle and each
   file's offset and size. A CUDA process on the node opens the handle
   (`cuIpcOpenMemHandle`) and reads the files from GPU memory.
4. When the disk copy is complete the usual JSON line is printed (with
   the GPU fields); the process then holds the GPU memory until it is
   stopped (SIGTERM), as `--then-serve` holds its serve. Once the ready
   file is written the memory is held even if the disk copy then fails
   (the JSON line reports it): a consumer may already be using it.
   Without `--gpu-ready-file` nothing can open the memory and the process
   exits after the JSON line. A
   fetch that cannot copy every listed file (a path the transport cannot
   request) is refused before any transfer: the GPU copy is all or
   nothing.

## Per range (one worker)

```text
land:   server RDMA-WRITEs the range into the worker's GPU slot   (NIC -> GPU)
flush:  cuFlushGPUDirectRDMAWrites where the device needs it
check:  copy the slot into a free pinned host buffer, CRC-32C      (GPU -> host)
place:  under the file's write guard, the checked host bytes ->
        the file's offset                                          (host -> GPU)
queue:  a disk-write job (target, offset, length) to the worker's writer
write:  the writer reads the range from the GPU buffer, writes it  (GPU -> host -> disk)
```

- Each worker owns one GPU landing slot and one pinned host buffer; its
  writer owns another. What the disk has not written yet waits in the GPU
  buffer, not in host memory: the job queue holds a few words per range.
  Queuing the host buffers themselves instead would bound the backlog by
  host memory and make the GPU copy wait for the disk.
- What is placed is exactly the bytes that were checked (copied from the
  host buffer, not from the landing slot), so a late write into a slot
  cannot change a placed range.
- One disk writer thread per worker, in order. A range is counted (and a
  file renamed into place after its last range) by the writer, after its
  bytes are written, so a file is never renamed with a write still queued.
- The disk writer writes every queued range unless the file is being
  fetched again whole from a newer view (which rewrites it); a ban of
  the range's source after it was placed does not drop it: its bytes
  were checked. A whole-file refetch waits for its worker's writer and
  renames the file itself, as without a device.
- Bad data is caught before anything is placed or queued: a CRC failure is
  `GetError::Corrupt`, handled as in any fetch (retry, then the source is
  retired). Placing takes the same write guard as a disk write, so a range
  from a source view the fetch has since given up on (a file being fetched
  again whole) is neither placed nor written; the refetch replaces it in
  both. The disk writer takes the guard again, as disk writes always do.
- The GPU buffer: every file at a 4 KiB-aligned offset, then the workers'
  slots. Its size is known once the manifest is read; a GPU without room
  fails the fetch before any transfer.

## Interfaces

- `fetch::fetch_to_device(sources, prefix, dest, opts, ToDevice)`: the
  generic fetch with a `Device` (landing slots, flush, read back, place,
  host buffers) made from the layout once the manifest is known, a
  `land` function per source type, and a ready hook. Unit-tested with a
  host-memory device; the GPU device is `gpu::GpuDevice`.
- CLI: `fetch --gpu N [--gpu-mapping default|pcie] [--gpu-ready-file PATH] [--gpu-check]`. `--gpu-check` compares every file's GPU copy with its
  disk copy at the end (a mismatch exits 5).
- JSON line: `gpu`, `gpu_bytes`, `gpu_ready_seconds` (null without `--gpu`).

## Not covered

- Landing in memory the caller allocated (dma-buf fds and a placement
  map passed in) instead of nvstor-peer's own buffer. The library
  interface for registering such memory exists
  (`nvstor_rdma::region::RailRegion::dmabuf`); the CLI does not take one.
- Several GPUs per fetch (a rank's files split over its GPUs): run one
  fetch per GPU.
