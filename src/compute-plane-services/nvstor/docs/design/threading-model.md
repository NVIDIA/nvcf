# Threading Model

What runs on which executor. Required reading before adding any new
async work or any new blocking I/O path.

This document exists because of a head-of-line blocking bug: the NFS
COMPOUND dispatcher ran inline on tokio worker threads while the
underlying `PassthroughVfs::read` did blocking `fs::read` calls on the
same thread. A single slow disk could stall an async worker, blocking
unrelated connections sharing that worker. The fix was to push COMPOUND
processing onto `tokio::task::spawn_blocking` and switch passthrough
I/O to positional `read_at` / `write_at`. The bug existed because
nobody had drawn the line between "what's async-only" and "what may
block."

Several components below are **experimental** (the NFS server, the S3
gateway, FUSE, Raft); this document describes how they run today, gaps
included.

## Rule

For each long-running component, the contract must specify:

1. **What's allowed to call this code on a tokio async worker** (no
   blocking syscalls, no contended lock that may wait > 1 ms).
2. **What's required to be on `spawn_blocking` or a dedicated thread**
   (synchronous fs syscalls, RocksDB calls, anything that may take more
   than ~1 ms).
3. **What thread pool the work uses** (tokio default, tokio blocking
   pool, a dedicated OS thread, a crate-owned worker pool).

If a function ever does blocking I/O *and* is reachable from an async
context, it must either be `async` (and use `tokio::fs` / `tokio::task::spawn_blocking`)
or its caller must be on `spawn_blocking` already.

## Components

### `nvstor_nfs` over TCP

Each connection is split into three parts: a reader task, one worker
per COMPOUND (up to `MAX_INFLIGHT_PER_CONN` = 256 in flight per
connection), and a writer task. Replies go from the workers to the
writer through a bounded channel; that bound is the backpressure on the
reader.

```text
TCP accept loop ── tokio default worker
  │
  └── per connection
        ├── reader task  ── tokio default worker
        │     rpc::read_record (BufReader, 1 MiB)
        │     NULL procedure answered inline
        │
        ├── COMPOUND worker (one per call) ── tokio::task::spawn_blocking
        │     ├── PassthroughVfs::{read,write}
        │     │     (read_at/write_at — positional, may block on disk I/O)
        │     ├── synthetic Vfs ops (PmStore, RocksDB)
        │     └── persist_all_state (RocksDB writes, blocking)
        │
        └── writer task ── tokio default worker
              rpc::write_record (BufWriter, 1 MiB), per-record write timeout
```

| Boundary | Pool | Why |
| -------- | ---- | --- |
| `accept`, reader and writer tasks | Tokio default workers | Async I/O, no blocking. |
| NULL procedure reply | Tokio default workers | Trivial work, no I/O. |
| `process_with_creds_policy` / `process_rdma` | **`spawn_blocking`** | Calls into the Vfs, which does fs syscalls and RocksDB. |

Rule: **never call `process_with_creds_policy` on a tokio default worker.**
The `spawn_blocking` call in `server.rs` (shared by the TCP and RDMA
listeners) is the only entry point.

### `nvstor_nfs` over RDMA

`rdma_server.rs`. The accept loop runs on the tokio blocking pool. Each
accepted connection gets one dedicated OS thread (`nfs-rdma-io`) that
posts receives, polls completions, pulls Read chunks and posts the
reply's RDMA WRITEs and SEND. Each COMPOUND is handed to a tokio task
that goes through the same `spawn_blocking` entry point as TCP; the
result comes back to the connection thread over a channel.

| Boundary | Pool | Why |
| -------- | ---- | --- |
| Accept loop | Tokio blocking pool | Waits on the RDMA listener; joins finished connection threads. |
| Per-connection verbs and completions | Dedicated `nfs-rdma-io` thread | Polls the CQ; must not occupy a tokio worker. |
| COMPOUND processing | **`spawn_blocking`** | Same as TCP. |

### `nvstor_object` (S3 gateway)

Built on `axum`, which runs on tokio default workers.

| Boundary | Pool | Why |
| -------- | ---- | --- |
| Request handler | Tokio default | `axum` handlers. |
| Object body upload | Tokio default | The body is collected as `axum::body::Bytes`. |
| `ChunkManager` / `MetadataStore` calls | **Tokio default (known gap)** | Called directly from the async handlers, not through `spawn_blocking`. A slow disk or RocksDB stall blocks the worker running the handler. |
| SigV4 signature check | Tokio default | CPU-bound but cheap. |

The `ChunkManager` calls violate the rule above; moving them onto
`spawn_blocking` is open work (see below).

### S3-over-RDMA and S3-over-TCP servers (`nvstor_rdma`)

These do not use tokio.

| Thread | Count | Role |
| ------ | ----- | ---- |
| `rdma-listen-<port>`, `rdma-accept` | One listener per transport, short-lived accept threads | Authenticated TCP bootstrap, QP setup. |
| `s3-rdma-peer-<id>` | One per connected peer | Dispatcher: receives requests on lane 0, verifies signatures, refuses disallowed verbs, then hands admitted work to the admission pool. |
| `rdma-worker` | Started on demand, at most one per admission permit | Runs storage callbacks (`object_read`, `object_write`) and the one-sided transfers of a request (`admission.rs`). Workers live until the admission is closed. |
| `s3-rdma-slot-reaper` | One per process | Drops parked ALLOC slots older than `SLOT_TTL`. |
| `rdma-liveness-<dev>` | One per transport | Polls every peer's bootstrap TCP stream; EOF tears the peer down. |
| Completion poller | One per transport (`completion.rs`) | Drains the shared send and receive CQs and routes each CQE to the waiting operation or lane. |
| `s3-tcp-accept`, `s3-tcp-dispatch-<id>` | Listener, one dispatcher per peer | S3-over-TCP equivalent of the above, with its own admission pool. |

ibverbs posting calls (`ibv_post_send`, `ibv_post_recv`) do not sleep.
A thread that posts a chain of work requests then parks; the completion
poller unparks it when the chain's tail CQE (or an error CQE) arrives.
The poller spins briefly while operations are in flight, then arms the
CQs and blocks in `poll(2)` on the completion channel. Waits are bounded
by wall clock, not by a spin count, so a QP error surfaces as its real
completion status instead of a timeout.

### `nvstor_fuse`

`fuser::mount2` runs the FUSE session loop on the mounting thread: the
kernel's requests are handled one at a time on that thread (the
`Filesystem` trait takes `&mut self`), NOT on tokio and not on a thread
pool.

| Boundary | Pool | Why |
| -------- | ---- | --- |
| FUSE op handler (passthrough) | FUSE session thread | Synchronous syscalls on the underlying FS. |
| FUSE op handler (remote) | FUSE session thread | Synchronous TCP RPC over a bounded connection pool. |
| RPC server (`fs_server.rs`, `nvstor-fs-serve`) | One OS thread per client connection | Blocking `std::net` accept loop; at most 256 concurrent clients. |

### `nvstor_storage` / `nvstor_metadata`

Public API is **synchronous** throughout. These crates don't know about
tokio. Callers from async contexts MUST use `spawn_blocking` (the S3
gateway currently does not, see above).

| Operation | Behavior |
| --------- | -------- |
| `MetadataStore::*` | RocksDB calls — blocking. |
| `ChunkManager::object_read`, `object_write` | POSIX or io_uring backend. POSIX blocks; io_uring submits and waits for its own completions, so it also blocks the calling thread. |
| `PmStore` reads and writes | POSIX or io_uring (sharded submission rings); blocks the calling thread. |
| `WalWriter::append` | Buffered write, plus fsync when `sync_on_append` — blocking. |

### `nvstor_raft`

| Operation | Pool | Why |
| --------- | ---- | --- |
| Heartbeat / election timers | Tokio | Time-driven, non-blocking. |
| Log append | **Tokio default (known gap)** | RocksDB write, called inline from `propose` and the AppendEntries handler. |
| Replication RPC client and server | Tokio | Async TCP. |
| Apply to state machine | **Tokio default (known gap)** | `apply_committed` runs inline and may call into `ChunkManager`. |

## When to use `spawn_blocking` vs `tokio::fs`

- `tokio::fs` is a thin wrapper over `spawn_blocking` for individual
  syscalls (read, write, open, metadata). For one-off operations, fine.
- For longer sequences of syscalls (e.g. `read_at` 1 MiB, then a
  RocksDB write), wrap the **whole sequence** in a single
  `spawn_blocking` call. Avoids round-tripping the executor between
  every syscall.

The NFS COMPOUND path follows the second pattern: one
`spawn_blocking` per COMPOUND, the inner code is regular synchronous
Rust.

## Tokio runtime configuration

`nvstor-server` uses `#[tokio::main]` and `nvstor-nfs-export` builds
`Runtime::Builder::new_multi_thread().enable_all()`; both keep tokio's
defaults:

- Worker threads: one per CPU.
- Blocking pool: at most 512 threads.

Every in-flight NFS COMPOUND holds a blocking-pool thread, so with many
connections at full concurrency the pool can fill and further COMPOUNDs
queue. If that becomes a limit, raise it with
`Builder::max_blocking_threads(N)` in the binary and record the choice
here.

## Adding new code

When adding any new component or function that does I/O, the PR description must answer:

1. What pool does this run on?
2. Does it block? If yes, is it called only from `spawn_blocking` or a
   dedicated thread?
3. Does any call into this hold a lock across an `.await`?

Reviewer's first check: do these answers match what's in this doc?

## Open work

- [ ] Move the S3 gateway's `ChunkManager` and `MetadataStore` calls
  onto `spawn_blocking`.
- [ ] Move Raft log appends and state-machine apply off tokio workers.
- [ ] Audit `nvstor-iam` and `nvstor-kms` for blocking calls in
  async paths.
- [ ] Document the `nvstor-server` startup sequence and which threads
  are created where.
- [ ] Decide the blocking pool size for `nvstor-server` (currently the
  default; may need to scale with `max_connections`).
