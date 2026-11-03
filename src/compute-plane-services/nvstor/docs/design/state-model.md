# State Model

How state is partitioned across nvstor's long-running components — what
lives on a shared `Arc<>`, what's per-connection, what's per-request.

This document exists because of a credential-contamination bug: the NFS
`CompoundProcessor` was an `Arc<>` shared across every TCP connection,
but it carried `effective_uid: Mutex<u32>` as a per-connection field.
Two concurrent client compounds raced on that mutex, contaminating each
other's caller credentials. The fix was to move uid into a stack-local
`CallerCtx` passed by reference. The bug existed because nobody had ever
asked the question this document forces every new component to answer.

The NFS server, the S3 gateway and FUSE described below are
**experimental**.

## Rule

For every long-running component (anything that lives behind an
`Arc<>`), each field MUST be classifiable as one of:

| Class | Lifetime | Mutation |
| ----- | -------- | -------- |
| **`Server`** | Whole process | Configured at startup, rarely changes |
| **`Connection`** | One TCP connection | Per-task local |
| **`Request`** | One RPC / op invocation | Stack-local |
| **`Persistent`** | Outlives the process | Backed by RocksDB / WAL / disk |

If a field can't be classified, the component has a design bug.

A field of class **Connection** or **Request** MUST NOT live on a
shared `Arc<>` — that's the contamination class. It must be either
threaded through the call path as a parameter, or held in
`tokio::task_local!` / per-task storage.

## Components

### `nvstor_nfs::nfs4::CompoundProcessor`

Source: `crates/nvstor-nfs/src/nfs4.rs`.

| Field | Class | Notes |
| ----- | ----- | ----- |
| `vfs: Arc<VfsBackend>` | Server | Synthetic Vfs has Persistent state behind it. |
| `metadata: Option<Arc<MetadataStore>>` | Persistent | RocksDB. |
| `metrics: Arc<NfsMetrics>` | Server | Atomic counters. |
| `raft: Option<Arc<RaftNode>>` | Server | |
| `open_states: RwLock<HashMap<...>>` | Server (cache) + Persistent | NFSv4 stateids; persisted to RocksDB on mutation. |
| `clients: RwLock<HashMap<u64, ClientEntry>>` | Server (cache) + Persistent | NFSv4 client leases; persisted. |
| `client_ids: RwLock<HashMap<Vec<u8>, u64>>` | Server (cache) + Persistent | Reverse index. |
| `sessions: RwLock<HashMap<[u8; 16], SessionState>>` | Server (cache) + Persistent | NFSv4.1 sessions; persisted. Slot reply cache size-bounded. |
| `slot_locks: [Mutex<()>; 256]` | Server | Serializes requests on the same session slot. |
| `persist_lock: Mutex<()>` | Server | Serializes state persistence. |
| `next_state, next_clientid: AtomicU64` | Server (counter) + Persistent | Persisted on tick. |
| `write_verifier: AtomicU64` | Server | Unique per boot; changed whenever a write or a sync fails, so clients resend uncommitted data. |
| `boot_time: i64` | Server | |
| `pnfs_ds_addr: RwLock<Option<(String, u16)>>` | Server | pNFS data-server address; None disables pNFS layouts. |
| `trunk_addrs: RwLock<Vec<String>>` | Server | Addresses advertised as `fs_locations` for session trunking. |
| **Caller credentials** | **Request** (stack-local `CallerCtx`) | Was a `Mutex<u32>` on the processor. Now built by `CallerCtx::from_auth` and passed by reference through `dispatch_op`. |
| **Export policy** | **Request** (passed via `&ExportPolicy`) | root_squash, read_only, advertised_flavors. Not stored on the processor. |

Concurrency invariants:

- COMPOUND processing runs on tokio's blocking pool (`spawn_blocking`;
  see [threading-model.md](threading-model.md)). Any state behind a
  mutex must not be held across `.await` (none currently are; the
  processor itself is sync from the dispatcher's perspective).
- A cached slot reply is bounded at 64 KiB
  (`MAX_SESSION_REPLY_CACHE_BYTES`); larger replies are not cached.

### `nvstor_nfs::server::NfsServer`

Source: `crates/nvstor-nfs/src/server.rs`.

| Field | Class | Notes |
| ----- | ----- | ----- |
| `config: NfsServerConfig` | Server | Read-only after construction. |
| `processor: Arc<CompoundProcessor>` | Server | See above. |
| `active_connections: Arc<AtomicUsize>` | Server (counter) | Bounded by `max_connections`. |
| `peers: Arc<PeerSlots>` | Server (counter) | Per-client-address share of `max_connections`, TCP and RDMA together. |
| `policy: Arc<ServerPolicy>` | Server | Auth flavors + ExportPolicy template. |
| `metrics: Arc<NfsMetrics>` | Server | Atomic counters. |
| **TCP stream** | **Connection** | Split into a reader half and a writer half owned by the per-connection tasks. |
| **Read and write buffers** | **Connection** | `BufReader` and `BufWriter`, 1 MiB each, per connection. |
| **Reply channel** | **Connection** | Bounded at `MAX_INFLIGHT_PER_CONN` (256) replies; carries replies from COMPOUND workers to the writer. |
| **idle/write timeouts** | **Connection** | Local `Duration`. |

The connection is the boundary: nothing inside `handle_connection` may
leak to siblings except via `processor`/`policy` (shared state with its
own locking) or the atomic counters.

### `nvstor_nfs::passthrough::PassthroughVfs`

Source: `crates/nvstor-nfs/src/passthrough.rs`.

| Field | Class | Notes |
| ----- | ----- | ----- |
| `root: PathBuf` | Server | Canonical export root; logging only. |
| `root_fd: OwnedFd` | Server | Base of every `openat2(RESOLVE_BENEATH \| RESOLVE_NO_SYMLINKS)` and the mount fd for `open_by_handle_at`. |
| `root_id`, `mount_id` | Server | Set at construction. |
| `decode: bool` | Server | `CAP_DAC_READ_SEARCH` present: unknown handles can be decoded (restart safety). |
| `key: FhKey` | Server (+ persistent with `--fh-key`) | HMAC key sealing filehandles. |
| `nodes: Mutex<NodeCache>` | Server (cache) | (parent, name, kernel handle) per inode, no descriptors; bounded two-generation LRU (`DEFAULT_NODE_CACHE`, 512Ki entries). |
| `read_only: bool` | Server | |
| `server_uid: u32` | Server | Effective uid of the process; owner of every inode it creates before handing it to the caller. |

The cache is bounded and holds no descriptors; an evicted inode is found
again from its filehandle (with `CAP_DAC_READ_SEARCH`).

### `nvstor_storage::manager::ChunkManager`

Not yet fully classified (see Open audits).

| Field | Class | Notes |
| ----- | ----- | ----- |
| `backend: Box<dyn StoreBackend>` | Server | POSIX or io_uring chunk store. |
| `metadata: Arc<MetadataStore>` | Persistent | RocksDB. |
| `config: ChunkManagerConfig` | Server | Read-only after construction. |
| `cache`, `data_cache` | Server (cache) | Chunk metadata and hot chunk data. |
| `volumes` | Server (cache) | Volume maps. |
| `wal: Option<Mutex<WalWriter>>` | Persistent | Optional write-ahead log. |
| `chunk_write_locks` | Server | Per-chunk lock so concurrent writers of the same content keep reference counts correct. |
| `unsynced` | Server | Chunks written with `SyncMode::None` whose bytes are not yet synced; their rows stay unsealed until `flush`. |
| Write throttle counters | Server | |
| **In-flight write state** | **Request** | `ObjectWriteCtx` for streamed writes is owned by the caller and passed by `&mut`. |

### `nvstor_object` (S3 gateway)

Source: `AppState` in `crates/nvstor-object/src/s3/mod.rs`.

| Field | Class | Notes |
| ----- | ----- | ----- |
| `manager: Arc<ChunkManager>`, `metadata: Arc<MetadataStore>` | Server + Persistent | |
| `quota`, `raft`, `distributed` | Server | |
| `iam: Option<Arc<IamStore>>`, `kms: Option<Arc<KmsManager>>` | Server (cache) + Persistent | |
| **HTTP request body** | **Request** | Collected per request. |
| **SigV4 signing context** | **Request** | Verified per request. |

### `nvstor_fuse::remote::RemoteFs`

Source: `crates/nvstor-fuse/src/remote.rs`.

| Field | Class | Notes |
| ----- | ----- | ----- |
| `pool: Mutex<Vec<TcpStream>>` | Server (pool) | Bounded at `pool_size`. |
| `cache: SharedCache` | Server (cache) + Persistent | On-disk content cache under `--cache-dir`, bounded by `--cache-size`. |
| `stat_cache`, `neg_cache` | Server (cache) | Bounded; the oldest tenth is evicted when full. |
| `ino_to_path`, `path_to_ino` | Server (cache) | **Not bounded**: entries are never evicted (the kernel may still hold the inodes); a warning is logged once the map passes `MAX_INODE_MAP_ENTRIES`. |
| `consecutive_failures`, `last_failure` | Server | Circuit breaker: fast-fail while the server is unreachable. |
| Per-FUSE-op state | **Request** | Stack-local. `fuser` runs ops one at a time on the session thread. |

## Adding a new component

When adding any new long-running component, the PR description must
contain a table like the ones above. Reviewer's first check: is every
field classified, and does any field of class Connection or Request
live on a shared `Arc<>`?

## Open audits

- [ ] `nvstor-storage::manager::ChunkManager` — full classification.
- [ ] `nvstor-raft::node::RaftNode` — leader/follower state vs persistent log.
- [ ] `nvstor-iam::store::IamStore` — policy cache vs RocksDB.
- [ ] `nvstor-kms` — key cache vs persisted material.
