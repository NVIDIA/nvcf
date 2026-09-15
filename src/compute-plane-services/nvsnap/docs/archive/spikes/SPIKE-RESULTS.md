# Fast Data Movement — Spike Results

Branch: `spike/fast-data-movement`. Measurements on hnst (nvsnap-agent-4xsrx),
fresh CRIU dump `vllm-small__nvsnap-system__20260515-042341` (30 GiB, 5,418
files). Blobstore: `nvsnap-blobstore` (custom HTTP CAS), in-cluster.

## Baseline (per-file CAS upload, current production path)

Per-file PUT of all 5,418 files to the blobstore:

| metric | value |
|---|---|
| files | 5,418 |
| bytes | 30 GiB |
| wall-clock | **104 s** |

## Spike 1 — tar + zstd-3, streaming upload (#74)

```
tar -C <parent> -cf - <dump> | zstd -T0 -3 | tee >(sha256sum) | curl --upload-file -
```

| stage | time |
|---|---|
| pack (tar + zstd -T0 -3, 208 cores) | 24 s |
| sha256 (inline via tee) | 0 s (overlapped) |
| PUT to blobstore (4.2 GiB) | 12 s |
| **sender total** | **~36 s** |
| artifact size | 4.2 GiB (7.20× compression) |

Receiver side (separate run, GET + extract on the same node):

| stage | time |
|---|---|
| GET from blobstore (4.2 GiB) | 12 s |
| `zstd -T0 -d -c | tar -xf -` to disk | **70 s** |
| **receiver total** | **82 s** |

**Sender 2.9× faster than baseline. Receiver 0.8× slower than baseline
per-file pulling 30 GiB, because tar+zstd decompression onto disk is
single-stream and disk-bound.**

## Spike 2 — mkfs.erofs + ship + mount (#75)

```
mkfs.erofs -zlz4hc,9 <out.erofs> <dump>     # Ubuntu 22.04 = erofs-utils 1.4
sha256sum <out.erofs>
curl --upload-file <out.erofs>
```

| stage | time |
|---|---|
| `mkfs.erofs -zlz4hc,9` (single-threaded) | **114 s** |
| sha256 (5.7 GiB artifact) | 18 s |
| PUT to blobstore (5.7 GiB) | 16 s |
| **sender total** | **148 s** |
| artifact size | 5.7 GiB (5.24× compression) |

Receiver side:

| stage | time |
|---|---|
| GET from blobstore (5.7 GiB) | 25 s |
| `mount -t erofs -o ro,loop` | **2 ms** |
| **receiver total** | **25 s** |

Validated separately: CRIU reads files identically from an EROFS mount as
from a real directory (mount-NS error reproduces with both paths — not an
EROFS issue).

## Comparison: end-to-end "data readable on target node"

The metric that matters operationally is wall-clock from the moment we
decide to ship until CRIU on the target node can `open()` images.
Sender and receiver can overlap once enough bytes have been uploaded;
materialize must complete before CRIU can run.

| | baseline (per-file CAS) | tar+zstd-3 | EROFS lz4hc,9 |
|---|---|---|---|
| sender (pack + PUT) | n/a (52 s — direct CAS) | 36 s | 148 s |
| receiver (GET + materialize) | n/a (52 s — direct CAS pull) | 82 s | **25 s** |
| compression ratio | 1× | 7.2× | 5.2× |
| L2 (cross-cluster) bytes | 30 GiB | **4.2 GiB** | 5.7 GiB |

(Baseline doesn't fit the sender/receiver split — it's symmetric per-file
PUT/GET against the central blobstore. Listed here for compression context.)

## Findings

1. **mkfs.erofs is single-threaded.** Ubuntu 22.04 ships erofs-utils 1.4;
   pack of a 30 GiB tree takes 114 s on an idle 208-core node, vs. 24 s
   for `zstd -T0`. erofs-utils 1.7+ added multi-shard parallel packing
   (`--workers=N`); we have not measured that. **Until we ship a custom
   userland with 1.7+, EROFS-as-transport loses badly on the sender.**

2. **EROFS mount is essentially free on the receiver** (2 ms). Tar+zstd
   pays 70 s to materialize the same bytes to disk because decompression
   plus syscall-per-file write is single-stream. This is the win the
   user intuited about EROFS — it is real, just on the receiver side.

3. **Compression ratio winner: tar+zstd-3 (7.20× vs EROFS lz4hc,9 5.24×).**
   This matters for L2 (cross-cluster egress) and L3 (S3 storage), where
   bytes-on-wire and bytes-at-rest are the cost. For an L1 hop on a fat
   in-cluster network it does not move the needle.

4. **No single artifact format wins all three pipeline legs.** Per the
   design doc (`docs/FAST-DATA-MOVEMENT-IDEAS.md`):
   - **L1 (capture-node → blobstore)** — fat network, want minimum sender
     CPU. tar+zstd-3 (or even uncompressed CAS) wins.
   - **L2 (blobstore → S3)** — pay for egress + S3 storage. tar+zstd-3
     wins on ratio.
   - **L3 / fanout to many receivers** — want zero-cost materialize on
     receiver, especially when fanning out to N nodes. EROFS wins.

5. **A two-format pipeline is plausible**: capture as tar+zstd for L1/L2
   (cheap to make, best ratio for cross-cluster); have the blobstore (or
   a per-cluster repackager) convert to EROFS once on first hit, cache,
   and serve EROFS to nodes for restore. The 114 s pack cost is paid once
   per checkpoint, amortized across all subsequent restores in the
   destination cluster. **This wants a follow-up spike to validate.**

## Spike 4 — erofs-utils 1.8.4 parallel pack (#77)

Built `erofs-utils 1.8.4` from source (Ubuntu 22.04 ships 1.4). Fixed
a real upstream bug: `--workers=N` was rejected because `errno` was not
reset before `strtoul` in the option parser. One-line patch on
`mkfs/main.c:810`.

### Worker sweep (`-zlz4hc,9`, 30 GiB dump)

| workers | pack | speedup vs 1.4 |
|---|---|---|
| 1.4 single-threaded (baseline) | 114 s | 1× |
| 1.8.4 workers=2 | 69 s | 1.65× |
| 1.8.4 workers=8 | **42 s** | **2.69×** |
| 1.8.4 workers=32 | 42 s | 2.69× |
| 1.8.4 workers=64 | 49 s | (regression) |
| 1.8.4 workers=128 | 59 s | (regression) |
| 1.8.4 workers=208 | 63 s | (regression) |

Scaling plateaus at workers=8 and regresses past 32 — likely contention
on a shared output stream / mutex inside mkfs. **`workers=8` is the
operating point.**

### Compressor sweep (workers=8, mountability checked separately)

| compressor | pack | size | ratio | mounts on 5.15 GKE? |
|---|---|---|---|---|
| `-zlz4` (no hc) | **37 s** | 5.86 GiB | 5.24× | ✅ |
| `-zlz4hc,9` | 44 s | 5.70 GiB | 5.38× | ✅ |
| `-zlz4hc,12` | 156 s | 5.70 GiB | 5.39× | ✅ (slow pack) |
| `-zdeflate,1` | 85 s | 5.30 GiB | 5.79× | ❌ |
| `-zdeflate,6` | 79 s | 4.59 GiB | 6.70× | ❌ |
| `-zzstd,1` | **45 s** | **4.37 GiB** | **7.03×** | ❌ |
| `-zzstd,3` | 57 s | 4.38 GiB | 7.02× | ❌ |
| `-zzstd,9` | 260 s | 4.33 GiB | 7.10× | ❌ |
| uncompressed | 58 s | 29.9 GiB | 1.03× | ✅ |

### Findings

1. **`zstd,1` would be ideal** — pack 45 s, ratio 7.03× (matches
   tar+zstd-3's 7.20×), keeps EROFS receiver advantage. **Not usable
   today**: GKE Ubuntu 5.15 kernel`s EROFS module only mounts lz4.
   Mount fails with "wrong fs type, bad superblock." Same for deflate.
2. **`lz4` (no hc) is the realistic choice on 5.15**: 37 s pack, 5.24×
   ratio, mount in 3 ms, md5 of mounted file matches source byte-for-
   byte.
3. **Kernel 5.16+ added EROFS deflate; 6.1+ added EROFS zstd.** Newer
   GKE node images (1.30+) and EKS AMIs (recent) ship 6.1 — re-test
   there before committing to `lz4` long-term.
4. **Spike 4 reduces the EROFS sender penalty from 3.6× (vs tar+zstd-3)
   to 1.5×**. Combined with the user-accepted "one-time async
   conversion" stance, this is small enough that direct EROFS
   production at capture is plausible — no separate repackager stage
   needed.

### Spike 4 summary line

`mkfs.erofs 1.8.4 -zlz4 --workers=8`: **37 s pack, 5.24× compression,
mountable on 5.15.** The realistic "one artifact, one PUT, one mount"
configuration for today.

## Spike 3 — L1 throughput ceiling probe (#76)

Original framing was "NIXL RDMA POC between agents." That experiment
**cannot run on this cluster**: GKE A3-Mega exposes gvnic (200 Gbps
TCP/IP) plus softiWARP (TCP-emulated iWARP). No Mellanox CX-7. Real
RDMA needs A3-Ultra nodes we don't have.

Reframed as: **what is the actual L1 (node → blobstore) throughput
ceiling, and where is it set?**

### Wire ceiling (pod-to-pod, iperf3)

| streams | throughput |
|---|---|
| 1 | **36.0 Gbit/s** |
| 8 | 17.6 Gbit/s |
| 16 | 18.7 Gbit/s |

Single-stream Cilium fast-path beats multi-stream — but **the wire is
not the bottleneck**.

### Blobstore PUT/GET (1 GiB random blobs, parallel)

**Caveat: this cluster's blobstore PVC is backed by NVMesh / Excelero
(an internal-only NVMe-over-Fabrics storage). Absolute numbers are not
representative of customer clusters, which use GCP Hyperdisk, AWS gp3,
or Azure Premium SSD. The *shape* of the result — single-stream caps
far below the wire, parallel scales — is portable.**

| | 1 stream | 4 parallel | 8 parallel |
|---|---|---|---|
| PUT | 1.42 Gbit/s | 3.23 Gbit/s | **10.1 Gbit/s** |
| GET | 2.57 Gbit/s | 9.16 Gbit/s | 7.99 Gbit/s |

PUT scales **7.2×** from 1→8 streams. GET saturates at ~9 Gbit/s
(NVMesh storage ceiling).

### Findings

1. **Our single-stream upload (`curl --upload-file` or Go's
   `http.Client`) caps at ~1.4 Gbit/s.** The wire delivers ≥36 Gbit/s
   pod-to-pod and storage absorbs ~10 Gbit/s. **The bottleneck is the
   client.**

2. **The simple lever is sharding.** Split the tar+zstd output into
   N chunks → N parallel HTTP PUTs (each its own SHA blob); manifest
   stitches them on restore. We already have the primitive — per-file
   CAS does exactly this for many small files. Extending to "chunk a
   big artifact" rephrases the spike 1 single-blob PUT as parallel
   chunked PUTs.

3. **Expected speedup on the spike 1 pipeline:** L1 PUT goes from 12 s
   (single-stream) to ~3-4 s (8-way), trimming spike 1 sender total
   from 36 s → ~28 s on this cluster.

4. **Storage ceiling is cluster-specific.** We measured NVMesh; we
   should re-measure on a customer-like backend (GCP Hyperdisk, AWS
   gp3) before committing the throughput target. The *single-stream
   under-utilization* finding is portable; the *absolute parallel
   ceiling* is not.

## What's still unmeasured

- erofs-utils 1.7+ parallel pack — could collapse the EROFS sender
  penalty from 114 s to single-digit seconds.
- True streaming receiver: pipe blobstore GET → `tar -x` without
  writing the tarball to disk first. Would cut ~10 s off the tar+zstd
  receiver path.
- Multi-receiver fanout — operational case where EROFS instant-mount
  shines. Needs N nodes simultaneously.
- Parallel-chunked PUT prototype — convert spike 1 to 8-way chunked
  upload, measure end-to-end on a customer-representative backend
  (gp3 or Hyperdisk, not NVMesh).
- True hardware RDMA — requires A3-Ultra (CX-7 RoCE) nodes that this
  cluster doesn't have. Not blocked; just not measurable here.

## End-to-end CRIU restore from EROFS (T1-T5)

Spikes 1-4 measured transfer mechanics. T1-T5 validated that CRIU can
restore a real GPU workload (vllm-small, TinyLlama 1.1B) end-to-end
with the checkpoint sourced from a read-only EROFS mount.

### Setup (all five tests)

- Same fresh dump: `vllm-small__nvsnap-system__20260515-155136` (30 GiB,
  706 files: 14× `pages-*.img` incl. GPU memory pages from the CUDA
  plugin, 628× `core-*.img`, `nvidia-files.img`, TCP-stream state,
  inventory, etc.)
- EROFS pack: `mkfs.erofs -zlz4 --workers=8` → 5.8 GiB artifact
- Restore harness: standard `vllm-small-restore.yaml` against the host
  hostPath. Mount substitution done via `nsenter` from privileged
  agent pod, BEFORE applying the restore manifest (kubelet's bind
  mount picks up the EROFS as part of its initial tree).

### Results

| test | what | time-to-ready | inference | notes |
|---|---|---|---|---|
| T1 | baseline (plain dir) | 42 s | ✅ | reference; matches historical numbers |
| T2 | RO EROFS, same node | 53 s | ✅ | direct EROFS mount at dump path |
| T3 | RO EROFS, cross-node | 63 s | ✅ | shipped via blobstore (28 s GET); restored on a different node than capture |
| T4 | overlay (lower=EROFS, upper=tmpfs) | 64 s | ✅ | **caught the one write** — see below |
| T5 | RO EROFS + fixed restore-entrypoint | 74 s | ✅ | direct RO mount, no overlay; new binary writes config to `/tmp` |

All five tests return identical inference output (`" often the topic
of philosophical debate. As people"`).

### What T4 caught and T5 fixed

T4's tmpfs upper layer recorded exactly **one** write during restore:
`restore-criu.conf` (98 bytes) — the CRIU options config file
(`libdir /nvsnap/plugins`, `enable-external-masters`, `tcp-established`,
`link-remap`, `allow-uprobes`, `timeout 120`).

The write came from `cmd/restore-entrypoint/main.go`, which used
`filepath.Join(checkpointPath, "restore-criu.conf")`. T2 and T3 passed
against pure RO EROFS only because the writer was wrapped in
`if err := os.WriteFile(...); err == nil { use config }` — silent
fallback to CRIU CLI defaults when the write failed on RO mount.

**T5 fix** (one-line, `cmd/restore-entrypoint/main.go:886`): write to
`/tmp/restore-criu.conf` instead. CRIU's `--config <path>` accepts any
absolute path; nothing requires the file to be next to the images.
Validated:

- ✅ Restore succeeds on pure RO EROFS (no overlay)
- ✅ `/tmp/restore-criu.conf` exists in restore pod, 98 B, expected content
- ✅ Dump dir on RO mount has no `restore-criu.conf`

### Implication for the design

CRIU's restore path **does not require write access to the checkpoint
directory** (with the one-line `/tmp` change). The EROFS-as-checkpoint
design from Spikes 1-4 is now end-to-end validated for single-GPU
workloads. Production agent: rebuilt as `v0.24.1-restore-conf-tmp` and
synced into all 12 workload manifests.

### Other code hygiene

`pkg/restore/` (992 lines across 6 files) was dead code — zero imports
anywhere in the repo. Removed in the same commit.

## Restore performance: disk vs EROFS

| | T1 baseline (plain dir) | T2 EROFS same-node | T3 EROFS cross-node | T5 EROFS + `/tmp` fix |
|---|---|---|---|---|
| time-to-ready | 42 s | 53 s | 63 s | 74 s |
| delta vs T1 | — | +11 s (+26%) | +21 s (+50%) | +32 s (+76%) |
| inference | ✅ | ✅ | ✅ | ✅ |

EROFS adds **10–30 s of restore overhead** for vllm-small. Sources: loop
device indirection, kernel-side lz4 decompression on every cold read,
and (for T3/T5) cascade fetch from blobstore on cold target node.
Modest in absolute terms — but it is degradation, not free.

## Cache behavior (T6) + multi-GPU rootfs (T7)

### T6 — vllm-small EROFS cache delta

| metric | value |
|---|---|
| node RAM | 1,932 GiB |
| EROFS raw | 5.8 GiB |
| source dump (decompressed) | 29.2 GiB |
| sequential-read wall-clock | 88 s |
| Cached delta (post-read) | +35 GiB |
| MemAvailable delta | −0.4 GiB |

Kernel caches BOTH layers (raw EROFS file + decompressed pages) but
`MemAvailable` barely moves — the cache is **evictable**, not pinned.
Lazy decompression IS happening; we just have so much RAM (1.9 TiB)
nothing gets evicted in practice.

### T7 — vllm-70b TP=4 rootfs (full e2e)

**Key inversion from earlier assumptions.** Rootfs captures of LLM
workloads are dominated by HF `safetensors` files (~140 GiB of 70B
weights) — already binary-incompressible. lz4 correctly skips them
with minimal overhead but yields no compression.

| metric | value |
|---|---|
| source (rootfs capture) | **133 GiB** |
| EROFS lz4 | **132 GiB** |
| **compression** | **1.01× — none** |
| pack time | **866 s (14 min)** |
| read time (sequential through lz4) | 148 s |
| Cached delta (post-read) | +95 GiB |
| MemAvailable delta | −1.8 GiB |

### T8 — vllm-70b TP=4 EROFS restore end-to-end (measured)

| | raw rootfs (baseline) | EROFS-mounted |
|---|---|---|
| ready | 233 s | 180 s |
| inference | ✅ | ✅ |

EROFS PASSed end-to-end on multi-GPU. The 180 s < 233 s is **not a
real EROFS win** — `mkfs.erofs` pre-warmed the page cache (read 133 GiB
source + wrote 132 GiB EROFS just before the test), so the EROFS-mounted
restore had a hot cache. An apples-to-apples cold-cache run would
expect EROFS to be slightly slower than baseline due to lz4-decompress-
on-every-read overhead. T8 proves correctness, not perf.

### Implications

1. **CRIU dumps (single-GPU): EROFS lz4 wins on bytes.** 5.2× compression,
   fast pack, instant mount; modest 10–30 s restore overhead vs disk.
2. **Rootfs captures (multi-GPU): EROFS is a wash to a loss.** 1.01×
   compression on safetensors-dominated data + ~14 min pack cost for
   zero savings. For rootfs the raw tree is the right shape.
3. **Lazy decompression works as documented** — cache is evictable.
   Untestable on 1.9 TiB RAM nodes without a memory cgroup; left
   open.

### Recommendation

**Do not ship EROFS as a primary path.** The compression win only
applies to single-GPU CRIU dumps, where per-file CAS already gives
parallel transfer + dedup + cascade. The argument for EROFS (instant
mount, no decompress on restore) is real but small (T2 +11 s, T5 +32 s
for vllm-small) — and it introduces userland/kernel/loop-mount deps
that misalign with the multi-GPU case (which is where bytes actually
matter, and where EROFS gives nothing).

**The real lever from these spikes is Spike 3's finding:** single-stream
HTTP upload caps at 1.4 Gbit/s on a wire that does 36 Gbit/s and
storage that absorbs 10 Gbit/s with 8-way concurrency. Sharding the
artifact and PUT/GET in parallel is a 7× wall-clock improvement that
applies to BOTH CRIU and rootfs paths, no new artifact format.

## Tracking

- #74 Spike 1 tar+zstd-3 — DONE
- #75 Spike 2 EROFS — DONE
- #76 Spike 3 throughput ceiling — DONE (RDMA framing retired)
- #77 Spike 4 erofs-utils 1.8.4 parallel pack — DONE
- #78 T1 baseline restore from directory — DONE
- #79 T2 single-node EROFS-mount restore — DONE
- #80 T3 cross-node EROFS-mount restore — DONE
- #81 T4 overlay write-trace — DONE (caught restore-criu.conf)
- #82 T5 validate `/tmp` fix on pure RO EROFS — DONE
- #83 T6 vllm-small EROFS cache-delta — DONE
- #84 T7 vllm-70b TP=4 pack + cache-delta — DONE
- #85 test-e2e.sh multi-GPU rootfs branch — DONE
- #86 T8 vllm-70b EROFS restore end-to-end — DONE (correctness only, perf inconclusive)
