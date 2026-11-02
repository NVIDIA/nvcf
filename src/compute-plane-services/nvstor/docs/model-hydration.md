# Node-to-node model hydration

Copy multi-GiB model shards (safetensors etc.) from an nvstor-server to a
node's local disk, with every byte verified.

**Status: experimental.** The client library and the `s3_hydrate` example
live in `nvstor-rdma`; the server side is `nvstor-server`, which is
experimental.

## Server

Any `nvstor-server`, single-node or clustered, serves the object fast
paths. Enable S3-over-TCP in `nvstor.toml`:

```toml
[network]
s3_tcp_port = 9879

[auth]
access_key = "..."
secret_key = "..."
```

Objects live in the server's PM object store (`<data_path>/pm_objects`),
one file per key. `direct_io = true` in `[storage]` selects the io_uring
backend.

## Client

```bash
cargo build --release -p nvstor-rdma --example s3_hydrate
export NVSTOR_ACCESS_KEY=... NVSTOR_SECRET_KEY=...

# seed: streamed from the file; the server streams it into storage (bounded memory both sides)
target/release/examples/s3_hydrate put --addr host:9879 --key models/llm/shard-00 --in shard-00.safetensors

# hydrate
target/release/examples/s3_hydrate get --addr host:9879 --key models/llm/shard-00 \
    --out /nvme/models/shard-00.safetensors --connections 8 --range-mib 16
```

`get` prints one JSON line: bytes, generation, ranges, retries,
reconnects, total seconds, `sync_seconds` (time waiting on the local
disk's fdatasync), GiB/s.

`--transport rdma` dials the server's `rdma_port` instead: each
connection first authenticates in the TCP bootstrap with the same access
key and secret (the server creates no QP before that), then is its own QP
set; the server RDMA-WRITEs each range into a registered client slot of
`--range-mib`, and the range is written to the file straight from that
slot. The report's `rails` field counts
connections per local NIC.

### Multiple NICs (rails)

The server opens one RDMA transport per HCA whose port is ACTIVE, each
with its own memory pool (a memory region belongs to one device, so
pools are not shared). Rail 0 listens on `rdma_port` and is also the
transport cluster replication uses; rail k ≥ 1 listens on
`rdma_rail_port_base + k - 1` (default `rdma_port + 16`, clear of the
RPC port at `rdma_port + 1` and of `s3_tcp_port`). `rdma_pool_slots` is
a per-node total, split evenly across rails; the in-flight request cap
is shared by all rails.

The client asks rail 0 for the server's rail list (a signed `RAILS`
request, authorized as a read of the key being fetched), assigns
connections to server rails round-robin, and puts each connection on the
local NIC that reaches that server NIC. Pairing is the same as
`nvstor-peer`'s (`crates/nvstor-rdma/src/rail.rs`, described in
[Rails, pairing and routes](peer-copy.md#rails-pairing-and-routes)): a
subnet attached to the NIC, else a route through it other than the
default (a default route is no evidence), else the server rail's plane
clustered from the server's RAILS reply; ties go to the NIC with fewer
connections, then to the NIC at the same position as the server rail.
Each new connection is checked with a RAILS request, and on a path
failure it moves to the next local NIC.

Device index is only a tiebreak because `mlx5_k` names follow PCIe
enumeration, not cabling. Each local NIC's pool holds `--connections`
slots, since every connection can land on one NIC when only one reaches
the server. Open firewall ports for every rail.

Library: `nvstor_rdma::hydrate` holds the transport-independent core
(`RangeSource` trait); `nvstor_rdma::tcp::hydrate_to_file` and
`nvstor_rdma::s3_rdma::hydrate_to_file` are the two sources.

## Contract

- **Ranged.** The object moves as ranged GETs (≤ 64 MiB each,
  `MAX_GET_RANGE`) over N parallel connections, each `pwrite`n straight
  into a file preallocated with `fallocate`. Memory use is one range per
  connection, independent of object size.
- **Verified.** Every range carries a CRC-32C (Castagnoli) of exactly its
  bytes; the client rejects a mismatch and retries that range.
- **One generation.** The initial stat pins the object's generation (the
  identity of the published file). Every range must come from it; if the
  object is replaced mid-transfer the server refuses with
  `GENERATION_CHANGED` before sending bytes, and hydration fails rather
  than stitching two versions together.
- **Durable completion marker.** Bytes land in `<out>.partial`. Only after
  all ranges are verified and the file is `fdatasync`ed is it renamed to
  `<out>` and the directory fsynced. `<out>` exists ⇔ the object is
  complete and durable. On failure the partial file is removed.
- **Failure handling.** Corrupt / busy / transport errors retry the same
  range with exponential backoff (5 attempts); a transport error first
  replaces that worker's connection with a fresh socket. Finished ranges
  are kept, so a dropped connection costs one range, not the shard.
  NotFound and GenerationChanged are final.

## Validation

Exercised over loopback with the TCP transport: a multi-GiB random object
(size not a multiple of the range) hydrates byte-identically (sha256) with
16 MiB and 64 MiB ranges; an object replaced mid-hydration fails with
`object replaced during transfer` and leaves neither `<out>` nor
`<out>.partial`; an unknown key fails with `not found` and leaves no files;
a connection cut mid-range (unit-test proxy) reconnects and the file is
byte-identical. On a single host, `get` time is usually dominated by the
final `fdatasync` (the `sync_seconds` field shows how much), so measure on
the target disks.

## Limits (not done yet)

- No in-place import of files already on the server's disk: seeding goes
  over the network (streaming `put`), even when the bytes are local.
- The RDMA path (`--transport rdma`), including multi-NIC rails, has run
  only on Soft-RoCE (`rdma_rxe`): the release binary hydrates an object
  byte-identically, with missing key, bad signature and replace-mid-transfer
  failing cleanly. That shows the verbs path is correct, not how fast it is.
  `s3_hydrate` itself has not run on a physical HCA. The same transport
  (protocol v2 GET, rails, the `RangeSource` fetch core) runs cross-node on
  RDMA hardware as `nvstor-peer` ([`peer-copy.md`](peer-copy.md)). Rail
  selection is unit-tested against synthetic routing tables, including an
  8-rail host wired out of index order.
- Rail selection reads only the main IPv4 routing table (and the IPv6
  prefix rule above); policy-routing tables are not consulted (an unmatched rail falls back to the
  least-loaded NIC with a warning).
  Each connection keeps one range in flight (no per-peer pipelining).
- A pool-exhausted server answers `NO_SLOT` (RDMA) and the client
  retries, but the server does not queue requests.
- No resume across process restarts: a killed `get` restarts the shard.
- Client writes and server reads go through the page cache (no `O_DIRECT`).
