# Fast Data Movement — Ideas and Measurements

**Status:** Exploration notes, not a final design. Captures the options
discussed for moving NvSnap checkpoint data fast between nodes, between
clusters, and to/from S3. Measurements are from real cluster runs on
`gcp-ct1` against a 30 GB `vllm-small` CRIU dump. Where a claim is
unmeasured, it says so.

## Problem framing

Today, every NvSnap capture commits as **5,628 per-file content-addressed
HTTP PUTs** to `nvsnap-blobstore`. Three observed costs:

1. Per-request overhead dominates the upload time — N small round-trips
   vs the one stream we could be doing.
2. Per-file deduplication ratio is measured at ~1.04× across captures
   of the same function-version, so the dedup story doesn't justify
   the per-file CAS architecture in practice.
3. The receive side mirrors the cost: 5,628 GETs + manifest assembly.

The conversation explored four angles for making this faster:
compression, format/transport, parallelism, and RDMA. Different angles
help different legs of the pipeline.

## Legs of the pipeline (where the cost actually lives)

```
[Node A]           [NvSnap blobstore]         [S3]              [Cluster B]
   |                     |                    |                    |
   |  L1                 |  L2                |  L3                |
   | ──────────────────► | ─────────────────► | ─────────────────► |
   |                     |                    |                    |
                         [hostPath, NVMe]    [object storage]   [Node B]
```

- **L1 — Node A → in-cluster `nvsnap-blobstore`.** Same-cluster HTTPS today;
  RDMA fabric is available on A3-class GKE nodes.
- **L2 — NvSnap blobstore → S3 (cross-region).** TCP/HTTPS only; RDMA does
  not reach S3.
- **L3 — S3 → receiving cluster's blobstore.** Same as L2, in reverse.
- (Plus a same-cluster fanout case: one capture → N restore pods on N
  nodes in the same cluster, all reading the same artifact.)

The right transport differs per leg. Anyone trying to pick one
universal answer is forcing a compromise.

## Options explored

### 1. tar + zstd-3 in a streaming pipe (no disk staging)

```
Source:       tar -cf - dump-dir/ | zstd -T0 -3 | <uploader>
Destination:  <downloader> | zstd -T0 -d | tar -xf - -C dump-dir/
```

| Property | Measured |
|---|---|
| Compression ratio | **7.19×** (30 GB → 4.2 GB) |
| Pack time | **25 s** |
| Pack throughput | **1.2 GB/s** (uses all 208 cores with `-T0`) |

Decompresses cleanly back to a directory; CRIU restore reads from that
directory exactly as today. **Zero compatibility risk.** Default
recommendation for any leg that isn't RDMA-eligible.

### 2. EROFS (read-only compressed FS image)

```
Source:       mkfs.erofs -zlz4hc,9 <dumpdir> <id>.erofs
Receive:      mount -t erofs -o ro,loop <id>.erofs <mountpoint>
              criu restore --images-dir=<mountpoint> ...
```

| Property | Measured |
|---|---|
| Compression ratio | **5.23×** with lz4hc,9 (30 GB → 5.7 GB) |
| Pack time | **116 s** (single-threaded; `mkfs.erofs` doesn't parallelize) |
| Pack throughput | **257 MB/s** |
| Mount time | **<1 s** |
| CRIU read-path compatibility | **validated** — `crit decode` works on
  mount; `criu restore` reads the same image set and reaches the same
  expected error line as a direct-dir restore (mount-NS mismatch
  unrelated to EROFS). |

The operational story is the reason to consider EROFS, not the
compression ratio (tar+zstd compresses better and packs ~5× faster).
What EROFS delivers:

- **One artifact per capture** instead of 5,628 CAS blobs. One HTTP
  PUT, one GET, one mount.
- **No extract step on the receiver.** Mount and the kernel
  decompresses pages on-demand.
- **Catalog gets simpler.** Per-capture key, no per-file manifests.
- **Webhook gets simpler.** One volume mount, not N.

What EROFS does **not** claim:

- Faster restore wall-clock. Not measured.
- Memory wins during restore. Not measured.
- Anything about cold-start time for the restored workload.

### 3. CRIU `--lazy-pages` + page-server + userfaultfd

The architecture that would actually deliver lazy restore — pages
decompressed and read only when the restored process touches them.
Tracked as GH issue #37. **Not built.** Estimated multi-day work to
wire properly. Punted in the conversation.

### 4. `criu-image-streamer` (existing in NvSnap)

Already integrated; default off (`NVSNAP_STREAM_CHECKPOINT=0`). The
existing implementation **pre-loads the entire decompressed checkpoint
into RAM** before serving CRIU's read requests — doesn't scale past
~30 GB. GH issue #40 tracks the redesign to true on-the-fly streaming.
Not a useful path in current form.

### 5. RDMA (NIXL or UCX) for same-cluster fanout

The A3-class GKE GPU nodes have 200 Gbps Mellanox NICs with RoCE /
GCP-Falcon fabric (the same fabric NCCL uses for distributed training).
RDMA does not extend past the cluster — it can't help L2 or L3.

Bandwidth math for 30 GB on the L1 leg:

| Transport | Wall time |
|---|---|
| Compress + ship (tar+zstd over TCP) | ~27 s (compression dominates) |
| Ship raw over TCP (gVNIC, 25 Gbps) | ~12 s |
| Ship raw over RDMA (200 Gbps NIC) | ~1.2 s |

**RDMA inverts the compression decision.** At line rate, compressing
takes longer (25 s) than transferring uncompressed (1.2 s). The right
architecture *with* RDMA is "ship raw bytes, don't compress."

Programming model — you need a library:
- **NIXL** (NVIDIA Inference Xfer Library): purpose-built for moving
  inference state over RDMA. Closest tool to our use case.
- **UCX**: abstracts RDMA verbs + TCP behind one API; picks best
  transport.
- **NFS-over-RDMA**: off-the-shelf but adds an NFS server.
- **AIStore**: NVIDIA object store with RDMA paths; replaces
  `nvsnap-blobstore`.

Not POC'd yet. Recommendation: spike NIXL with a 30 GB transfer between
two agent pods on different nodes; measure wall-clock vs current path
before deciding.

### 6. `s5cmd` for the S3 leg

The default `aws-cli` is single-threaded for upload and slow.
**`s5cmd`** is 32-way parallel multi-part by default, lock-free Go,
routinely hits 5–10 Gbit on a single host. ~3–5× faster than `aws-cli`
for the same workload. **No reason to use aws-cli for L2 or L3.**

### 7. S3 Cross-Region Replication (CRR)

Server-side replication between S3 buckets. Bytes never come to your
machine. Replaces "blobstore-on-cluster-A → S3 → blobstore-on-cluster-B"
with "blobstore-on-cluster-A → S3 → (CRR fan-out) → all other regions".
Settings live on the bucket. Standard AWS feature.

## Picking per leg

| Leg | Recommended transport | Why |
|---|---|---|
| **L1** Source node → in-cluster blobstore | **RDMA (NIXL/UCX)** if hardware allows; **single-stream tar+zstd or EROFS** otherwise | Replaces 5,628 PUTs with one. RDMA buys ~10× over TCP for the raw-bytes case. |
| **L2** Blobstore → S3 (cross-region) | **`s5cmd pipe`** of a compressed artifact (tar+zstd or EROFS) | RDMA doesn't reach S3. Compression matters because cross-region egress costs money + time. |
| **L3** S3 → receiving cluster | **`s5cmd cp`** + extract or mount | Mirror of L2 in reverse. |
| **Same-cluster fanout** (one capture → many restore pods) | **EROFS image cached on each target node** OR **RDMA-fetched on demand** | Mount once per node, restore pods share. |

These compose — you can have all four legs using different transports.

## What was measured (real numbers, gcp-ct1)

Corpus: vllm-small CRIU dump, **30 GB across 5,628 files**.

| Operation | Time | Throughput | Notes |
|---|---|---|---|
| tar + zstd-3 pack | 25 s | 1.2 GB/s | Uses all 208 cores |
| tar + zstd-9 pack | 95 s | 315 MB/s | Marginal compression gain |
| mkfs.erofs -zlz4hc,9 | 116 s | 257 MB/s | Single-threaded |
| mkfs.erofs default (uncompressed) | 101 s | 296 MB/s | No compression — just a packer |
| mount -t erofs -o ro,loop | <1 s | — | Instant |
| sequential read of mounted EROFS | 99 s | 301 MB/s | Single-threaded kernel decompress |
| `crit decode` from EROFS mount | works | — | Validated against multiple image types |
| `criu restore` read path from EROFS mount | works | — | Same image-collection sequence as direct dir; both fail at expected mount-NS error |
| RDMA transfer | not measured | (200 Gbps NIC capable) | POC needed |
| `s5cmd` to S3 | not measured | (5–10 Gbit/host typical) | Documented, not benchmarked in our env |

## What was explicitly **not** measured

- Restore wall-clock from EROFS-mounted images
- End-to-end cross-cluster restore times
- RDMA transfer wall-clock in this environment
- Memory cost of CRIU restore from a compressed read-only mount

These are open questions before adopting any of these paths.

## Tradeoffs that matter

- **Compression vs RDMA.** They fight. At RDMA line rate, the compressor
  becomes the bottleneck and uncompressed transfer wins on wall-clock.
  Pick per leg, don't try to use both.
- **EROFS pack throughput.** `mkfs.erofs 1.4` (Ubuntu 22.04) is
  single-threaded. **Upgrade to 1.7+** (tail-packing, fragments, dedupe)
  if EROFS is adopted — ratio goes from 5.2× to ~6.5×, and 1.7's
  multi-shard mode parallelizes the pack.
- **Per-file CAS vs single-artifact.** Per-file dedup is small (1.04×
  measured). The architectural complexity of per-file CAS doesn't pay
  for itself in our workload. A single-artifact-per-capture model
  (tar+zstd or EROFS) is much simpler.
- **L2/L3 dominate when scaling across clusters.** RDMA wins L1 but
  doesn't touch L2/L3. If "scale checkpoints across clusters" is the
  top-priority goal, optimize the S3 leg first (s5cmd, CRR, compression)
  — that's where the wall-clock and money are spent.

## Open questions / decision points

1. **Single-artifact format**: tar+zstd or EROFS?
   - tar+zstd: better compression, faster pack, no mount complexity, no
     restore-time benefit beyond simpler architecture.
   - EROFS: 30% bigger, slower pack, but mountable and operationally
     simpler ("one file, one mount, no extract step"). Format
     compatibility with CRIU **validated**.
2. **Same-cluster fanout via RDMA**: spike NIXL or not?
   - 10× over TCP if it works
   - Adds a new library/protocol dependency
   - Same-cluster only — doesn't help cross-cluster
3. **S3 leg tooling**: adopt `s5cmd` for the agent's blob uploader?
   - Strict improvement over the current `http.Client` PUT loop
   - No architectural risk
4. **Cross-cluster replication**: roll our own (SNS/SQS fan-out per the
   prior design doc) vs S3 CRR?
   - CRR is simpler operationally
   - Loses per-cluster filter control (the sidecar manifest scheme from
     the cross-cluster design doc)

## Pointers

- `docs/CROSS-CLUSTER-REPLICATION-DESIGN.md` — the broader replication
  architecture this feeds into.
- `docs/archive/PHASE5C-EROFS-COMPACTION.md` — original EROFS proposal
  (never implemented; risks called out here are now resolved by
  today's test).
- `internal/agent/blob_uploader.go` — current per-file PUT path.
- `internal/agent/cascade_fetch.go` — current per-file GET cascade.
- GH issues:
  - #37 — CRIU `--lazy-pages` work
  - #40 — `criu-image-streamer` true-streaming redesign
  - #18 — persistent checkpoint store (S3/GCS)

## Next step (if any)

Pick **one** of these for the first concrete spike, in order of
information-per-hour-spent:

1. Replace the agent's per-file uploader with a **single tar+zstd
   streaming PUT** (1 day). Measure end-to-end capture-to-blobstore
   wall-clock vs current. Strict improvement, low risk, no
   architectural lock-in.
2. **EROFS pack + ship via existing HTTP** behind a feature flag
   (~2 days). Measure end-to-end including mount-time-restore on a
   target node. Gives data for the architectural question.
3. **NIXL RDMA POC** between two agents (~1–2 days). Confirms RDMA
   bandwidth in this environment and informs whether to invest in the
   dual-leg architecture.

Don't do all three at once; pick the one that answers the most
ambiguous question for your roadmap.
