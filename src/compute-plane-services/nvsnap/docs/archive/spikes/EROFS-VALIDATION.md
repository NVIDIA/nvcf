# EROFS as a NVSNAP Restore Source — Validation Report

Self-contained record of every EROFS test we ran on the
`spike/fast-data-movement` branch. Captured so we can pick this up
again if the trade-offs change (new kernel features, custom writer,
faster userland, different workload mix).

**Current verdict (2026-05-15): do not ship EROFS as a primary path.**
The compression win is narrow (single-GPU CRIU dumps only), the
multi-GPU rootfs case shows 1.01× compression, and the existing
per-file CAS already gives parallel transfer + dedup + cascade.
**Revisit triggers** are listed at the bottom.

---

## Quick reference

| test | scenario | source | EROFS | compression | restore time | infer |
|---|---|---|---|---|---|---|
| T1 | vllm-small, plain hostPath | 30 GiB / 706 files | n/a | n/a | **42 s** | ✅ |
| T2 | vllm-small, EROFS RO, same node | 30 GiB | 5.8 GiB | **5.24×** | 53 s (+11) | ✅ |
| T3 | vllm-small, EROFS cross-node | 30 GiB | 5.8 GiB | 5.24× | 63 s (+21) | ✅ |
| T4 | vllm-small, overlay (lower=EROFS, upper=tmpfs) | 30 GiB | 5.8 GiB | 5.24× | 64 s | ✅ + caught 1 write |
| T5 | vllm-small, RO EROFS + `/tmp` fix | 30 GiB | 5.8 GiB | 5.24× | 74 s | ✅ + no writes to dump dir |
| T6 | vllm-small, cache-delta probe | n/a | 5.8 GiB | 5.24× | n/a (read 88 s) | n/a |
| T7 | vllm-70b TP=4, rootfs pack + cache | **133 GiB** | **132 GiB** | **1.01×** | n/a (pack 866 s) | n/a |
| T8 | vllm-70b TP=4, EROFS-mounted restore | 133 GiB | 132 GiB | 1.01× | 180 s (cache-warm) vs 233 s raw baseline | ✅ |

---

## Environment

- **Cluster:** `example-gpu-cluster` (GKE)
  - GPU nodes: GKE A3-Mega, 8× H100 80GB, gvnic 200 Gbps × 9, **1.9 TiB RAM**
  - Kernel: 5.15.0-1080-gke (Ubuntu 22.04) — **lz4 only** for EROFS mount
- **Agent:** `nvsnap-agent:v0.24.1-restore-conf-tmp`
- **EROFS userland:** `erofs-utils 1.8.4` built from upstream source on
  the agent pod (Ubuntu 22.04 ships 1.4 which is single-threaded; 1.8.4
  adds `--workers=N` for parallel pack — but we hit an upstream bug
  where `--workers=N` was rejected by `strtoul` with stale `errno`,
  patched with a one-liner)

### Mountable compressors on GKE 5.15

| compressor | pack OK | mount on 5.15 GKE? | min kernel for mount |
|---|---|---|---|
| `-zlz4` (default) | ✅ | ✅ | 5.4 |
| `-zlz4hc,N` | ✅ | ✅ | 5.4 |
| `-zdeflate,N` | ✅ | ❌ "wrong fs type" | 5.16 |
| `-zzstd,N` | ✅ | ❌ "wrong fs type" | 6.1 |

**This is a real constraint:** on the only kernel we have today, we're
stuck with lz4. Mainline GKE 1.30+ ships kernel 6.1 which would unlock
zstd — important for future re-test because T7 showed zstd would give
~7× compression on the same data lz4 gives ~5.2×.

---

## Methodology (reproducibility)

All tests used the same agent pod via `kubectl exec` + `nsenter` for
host-mount-namespace operations (kubelet bind mounts default to
mountPropagation=None, so the EROFS mount has to be on the host
before the restore pod starts).

### Source preparation

```bash
# Single-GPU CRIU dump path (T1-T6)
./scripts/test-e2e.sh vllm-small        # produces /var/lib/containerd/nvsnap-checkpoints/vllm-small__nvsnap-system__<TS>/
DUMP_DIR=/var/lib/containerd/nvsnap-checkpoints/vllm-small__nvsnap-system__<TS>

# Multi-GPU rootfs path (T7-T8)
./scripts/test-e2e.sh vllm-70b          # triggers rootfs watcher; output at /var/lib/nvsnap/cache/<hash>/
SRC_DIR=/var/lib/nvsnap/cache/<sha256>
```

### Pack to EROFS

```bash
# Build erofs-utils 1.8.4 on the agent pod (Ubuntu 22.04 deps required)
apt-get install -y --no-install-recommends git build-essential pkg-config \
    autoconf automake libtool liblz4-dev libzstd-dev zlib1g-dev uuid-dev
git clone --depth 1 --branch v1.8.4 \
    https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git
cd erofs-utils
./autogen.sh && ./configure --enable-multithreading
# UPSTREAM BUG FIX: --workers=N is rejected by strtoul because errno
# isn't reset. One-liner:
sed -i 's|cfg.c_mt_workers = strtoul(optarg, \&endptr, 0);|errno = 0; cfg.c_mt_workers = strtoul(optarg, \&endptr, 0);|' mkfs/main.c
make -j$(nproc)

# Pack (workers plateaus at 8 — past 32 it regresses, likely shared
# output mutex contention)
./mkfs/mkfs.erofs -zlz4 --workers=8 /var/lib/containerd/_t/dump.erofs $SRC_DIR
```

### Mount + restore substitution

```bash
NSEN="nsenter --target 1 --mount --uts --ipc --net --pid"

# CRIU path (T2/T3/T5): substitute dump dir
$NSEN mv $DUMP_DIR ${DUMP_DIR}.orig
$NSEN mkdir -p $DUMP_DIR
$NSEN mount -t erofs -o ro,loop /var/lib/containerd/_t/dump.erofs $DUMP_DIR
kubectl apply -f vllm-small-restore.yaml   # __CHECKPOINT_ID__ + nodeName already substituted

# Rootfs path (T8): substitute cache dir, restore pod uses webhook annotation
$NSEN mv $SRC_DIR ${SRC_DIR}.orig
$NSEN mkdir -p $SRC_DIR
$NSEN mount -t erofs -o ro,loop /var/lib/containerd/_t/dump.erofs $SRC_DIR
kubectl apply -f vllm-70b-restore.yaml      # __CAPTURE_HASH__ already substituted
```

### T4 overlay-write probe

```bash
# lower=EROFS, upper=tmpfs (or a fresh dir on local disk), target=dump dir
LOWER=/var/lib/nvsnap/cache/_t/lower
UPPER=/var/lib/nvsnap/cache/_t/upper
WORK=/var/lib/nvsnap/cache/_t/work
$NSEN mkdir -p $LOWER $UPPER $WORK
$NSEN mount -t erofs -o ro,loop /var/lib/containerd/_t/dump.erofs $LOWER
$NSEN mount -t overlay overlay \
    -o lowerdir=$LOWER,upperdir=$UPPER,workdir=$WORK $DUMP_DIR
# ... do restore ...
$NSEN find $UPPER -type f   # any file here was written during restore
```

---

## Findings

### F1 — RO EROFS works for CRIU restore (T2, T3, T5)

Pure read-only EROFS mount at the dump-dir path is a transparent
substitution for the directory. CRIU reads images, the CUDA plugin
reads `nvidia-files.img`, the CPU/GPU pages stream out of `pages-*.img`,
md5 of mounted files matches source byte-for-byte. End-to-end vllm-small
inference works identically.

### F2 — One write to the dump dir, fixed (T4 → T5)

T4 overlay caught exactly one write during restore: `restore-criu.conf`
(98 bytes), from `cmd/restore-entrypoint/main.go:886`. The writer was
wrapped in `if err := os.WriteFile(...); err == nil { use config }` —
silent fallback to CRIU CLI defaults when the write failed on RO mount.
Fix (one line): write to `/tmp/restore-criu.conf` instead. T5 validated
the fix on pure RO EROFS, no overlay.

### F3 — mkfs.erofs scaling plateaus at workers=8

| workers | pack time (30 GiB) | notes |
|---|---|---|
| 1 (1.4 baseline) | 114 s | single-threaded |
| 2 | 69 s | 1.65× speedup |
| **8** | **42 s** | **2.7× — sweet spot** |
| 32 | 42 s | flat |
| 64 | 49 s | regression |
| 208 | 63 s | regression |

Above 32 workers, throughput regresses — likely contention on a shared
output stream / mutex inside mkfs. This is the practical ceiling
without a custom writer.

### F4 — Compressor sweep at workers=8

| compressor | pack (30 GiB) | size | ratio | mountable? |
|---|---|---|---|---|
| `-zlz4` (no hc) | **37 s** | 5.86 GiB | 5.24× | ✅ |
| `-zlz4hc,9` | 44 s | 5.70 GiB | 5.38× | ✅ |
| `-zlz4hc,12` | 156 s | 5.70 GiB | 5.39× | ✅ (slow) |
| `-zdeflate,1` | 85 s | 5.30 GiB | 5.79× | ❌ |
| `-zdeflate,6` | 79 s | 4.59 GiB | 6.70× | ❌ |
| **`-zzstd,1`** | **45 s** | **4.37 GiB** | **7.03×** | ❌ |
| `-zzstd,3` | 57 s | 4.38 GiB | 7.02× | ❌ |
| `-zzstd,9` | 260 s | 4.33 GiB | 7.10× | ❌ |

On a kernel that mounts zstd, `-zzstd,1` would tie tar+zstd-3's 7.2×
with EROFS receiver semantics. **This is the strongest revisit
trigger** — see "Revisit triggers" below.

### F5 — Lazy decompression is real, but invisible on 1.9 TiB nodes

T6 vllm-small full sequential read of the EROFS mount:
- Cached grew by **+35 GiB** (raw EROFS layer + decompressed pages, both cached)
- MemAvailable dropped only **−0.4 GiB** (kernel correctly accounts cache as reclaimable)

T7 vllm-70b full read of 132 GiB EROFS:
- Cached grew by **+95 GiB**
- MemAvailable dropped only **−1.8 GiB**

Both: kernel caches everything because there's headroom; under memory
pressure it would evict and re-trigger lz4 decompression. We cannot
demonstrate eviction on these 1.9 TiB nodes without a memory cgroup
constraint — left open.

### F6 — Compression collapses on rootfs-of-LLM-workloads (T7)

| dataset | source | EROFS lz4 | ratio |
|---|---|---|---|
| vllm-small CRIU dump (page/core images) | 30 GiB | 5.8 GiB | **5.24×** |
| vllm-70b rootfs (HF cache + vllm artifacts) | **133 GiB** | **132 GiB** | **1.01×** |

The 70B rootfs is dominated by HF `safetensors` files — already binary-
incompressible. lz4 correctly skips them with minimal overhead but
gives no savings. **This is the central reason EROFS doesn't fit the
multi-GPU path.**

### F7 — EROFS restore works end-to-end at TP=4 (T8)

| | raw rootfs baseline | EROFS-mounted |
|---|---|---|
| ready | 233 s | 180 s |
| inference | ✅ | ✅ |

The 180 s < 233 s is **not a real EROFS perf win** — `mkfs.erofs`
read 133 GiB source + wrote 132 GiB EROFS just before the test, so
the kernel held both layers hot when the EROFS-mounted restore ran.
The raw-baseline restore (233 s) ran from cold cache after we'd
freshly deleted the source pod and the pages were evicted by other
activity. For an honest comparison both runs would need a clean
`echo 3 > /proc/sys/vm/drop_caches` immediately before the apply.

What T8 *does* prove: end-to-end correctness. EROFS-mounted source
goes through nvsnap-server webhook + cascade fetch + agent restore
successfully for a 4-GPU tensor-parallel workload.

---

## Code changes made during validation

| commit | change |
|---|---|
| `96934e1` | `cmd/restore-entrypoint/main.go:886`: write `restore-criu.conf` to `/tmp` instead of the dump dir (one-line; see F2) |
| `96934e1` | deleted `pkg/restore/` (992 lines, 6 files, zero imports anywhere) |
| `f54d5f1` | `scripts/test-e2e.sh`: pre-flight skips cordoned nodes; multi-GPU detection routes to rootfs path (poll nvsnap.io/kind=rootfs-capture-manifest CM, extract hash, substitute `__CAPTURE_HASH__`) |

---

## Recommendation (current)

**Don't ship EROFS as a primary path.**

Reasons:
1. The compression win (5.2× on CRIU dumps) is real but narrow —
   single-GPU workloads only. The cases where bytes actually matter
   (multi-GPU rootfs, 100s of GiB of safetensors) get 1.01×.
2. The "instant mount on restore" advantage is +10–30 s vs disk for
   vllm-small (T2/T5). Real, but small.
3. Per-file CAS already gives parallel transfer + dedup + cascade for
   free, with no new artifact format.
4. EROFS introduces dependencies that misalign with the multi-GPU case:
   custom userland (erofs-utils ≥ 1.7 needed + our `--workers` patch),
   kernel mount-feature variance (only lz4 on 5.15; zstd needs 6.1+),
   privileged loop mount.

The real lever from this spike series is Spike 3's parallel-PUT
finding: single-stream upload caps at 1.4 Gbit/s on a 36 Gbit/s wire,
10 Gbit/s storage. Sharding the artifact and PUT/GET in parallel
yields **7×** wall-clock improvement and applies to BOTH CRIU and
rootfs paths — without a new artifact shape.

---

## Revisit triggers

When any of these change, redo the analysis:

1. **Kernel 6.1+ rolls out broadly.** `-zzstd,1` jumps ratio to **7.03×**
   on CRIU dumps (vs lz4's 5.24×), pack 45 s. Closes the gap with
   tar+zstd-3. Still doesn't help the rootfs case (safetensors).

2. **Custom EROFS writer.** Reasons to consider:
   - mkfs.erofs's workers=8 plateau means it can't saturate fast NVMe
   - For our specific use case (snapshot of an immutable tree), many
     general-purpose features (hardlink scanning, xattr edge cases,
     incremental update support) are unneeded
   - Our 133 GiB pack took 14 min at ~150 MiB/s — way below NVMe peak
   - Ideas: per-shard parallel pack into separate output regions,
     content-type aware compression (skip lz4 for safetensors entirely,
     copy raw — no overhead, no ratio loss), direct mmap output instead
     of streaming through a single fd
   - Risk: this is research-grade work; correctness against the EROFS
     kernel reader is non-trivial to verify

3. **A multi-receiver fanout pattern** (one source → N target nodes).
   EROFS's instant mount is per-receiver; the pack cost amortizes
   across N restores. If N gets large, the calculus shifts. We didn't
   measure this scenario.

4. **A workload with compressible rootfs.** Our 70B test was dominated
   by safetensors. A workload whose rootfs is mostly Python source +
   pickled state + JSON would compress fine, and EROFS would look more
   attractive again.

5. **A new use case for read-only artifact-as-mount semantics**
   beyond CRIU restore — e.g., shipping training datasets, model
   shards, base images. EROFS is well-fit for these even when our
   current restore use case isn't.

## What we did NOT test (left open)

- Memory-cgroup constrained read to actually force EROFS page-cache
  eviction and measure re-decompression cost.
- Cold-cache apples-to-apples T8 comparison (drop caches between runs).
- Multi-receiver fanout (one EROFS, N parallel mounts).
- erofs-utils `--workers=128+` with kernels that don't have the shared-
  output-mutex bottleneck (might exist in newer userland).
- True hardware-accelerated lz4 (CPU AVX2 path is what we used; no GPU
  decompress).
- Streaming GET → loop-mount without writing the EROFS file fully to
  disk first.
- EROFS on a customer-representative blobstore backend (NVMesh was the
  only one tested; gp3/Hyperdisk would have different throughput).

---

## Related docs

- `docs/spikes/SPIKE-RESULTS.md` — full chronological spike results
  including parallel-PUT findings (Spike 3)
- `docs/FAST-DATA-MOVEMENT-IDEAS.md` — the design exploration that
  preceded the spikes
