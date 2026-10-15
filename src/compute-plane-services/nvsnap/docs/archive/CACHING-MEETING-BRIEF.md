# Caching & Data Movement — meeting brief (2026-05-19)

Source data from in-cluster benches on GKE A3-Mega (3 GPU nodes:
1sd6, 3604, hnst — `us-central1-c`, gvnic, 16× local NVMe RAID0 at
`/var/lib/nvsnap/checkpoints`).

## 1. In-cluster data movement — what's measured

### CRIU cascade fetch (single-GPU)

vllm-small (30 GiB CRIU dump, dominated by 1× pages-14.img at 27 GiB —
the remaining ~3 GiB is spread across smaller image files; exact file
count not enumerated this session) cross-node from one peer:

| Measurement | Value | Notes |
|---|---|---|
| Per-receiver cascade fetch | **25 s = 1.20 GB/s** | EnsureLocal wall-clock |
| Same-node Tier-1 hit | < 1 s | hostPath short-circuit |
| Cold-start vllm-small | 90 s | reference for cache value |
| Capture-side (CRIU + dump) | 90 s | reference |

### Rootfs cascade fetch (multi-GPU, vllm-70b) — IMPORTANT FINDING

**The rootfs path does NOT currently fan out across nodes.** Tested
2026-05-19 with vllm-70b TP=4 (133 GiB rootfs capture on node `3604`).
Attempted to apply restore pods pinned to `1sd6` and `hnst`. Pods
admitted but **kubelet rejected with `NodeAffinity` predicate failure**.

Root cause: the admission webhook (`internal/webhook/mutate.go`) reads
`Manifest.CapturedOnNodes` (just `[3604]` here) and injects a
`requiredDuringScheduling` nodeAffinity restricting to that node only.
Restore pods can only schedule on the source node.

**For NVCF's "spin up 40 pods" multi-GPU pattern, this is broken** —
all 40 replicas must fit on the source node's 8 GPUs.

**`EnsureCaptureLocal` exists for cross-node rootfs fetch but is never
called for fan-out** — only as a recovery path when local cache is
missing on an allowed-affinity node.

Filed as GH issue #114. Fix options:
- Webhook injects an init container that calls EnsureCaptureLocal on
  the target node, instead of pinning via nodeAffinity
- Push-to-seeds after capture (Phase 4 from TRANSPORT-ARCHITECTURE.md)
- Hybrid: if `len(CapturedOnNodes) == 1`, webhook synchronously
  triggers copy to target node before letting kubelet schedule

### Rootfs cross-node measurement (production code path, v0.24.16)

After adding `/v1/captures/{hash}/ensure-local` to the agent
(commit, GH #114 partial fix), bench from receiver agents via curl
against the new endpoint. This runs the EXACT production code:
EnsureCaptureLocal → 8-worker HTTP pool → 5688 file fetches → md0
write — same path the webhook would trigger if the fan-out was
fully wired.

| Measurement | Value | Notes |
|---|---|---|
| Capture size on disk | **133 GiB** | smaller than CRIU 322 GiB — no GPU state |
| Per-receiver fetch (production path) | **415 s = 0.32 GB/s** | identical on both nodes |
| Parallel wall-clock (2 receivers) | **417 s (~7 min)** | both identical → no sender contention |
| Aggregate sender bandwidth | 0.64 GB/s | well under disk's 2.5 GB/s cap |
| Cold-start vllm-70b | 332 s (~5.5 min) | measured tonight, TP=4 on 1sd6 |

**Important — production matches the naive proxy.** Earlier I projected
"3× faster with real Go HTTP client" — that was wrong. The proxy at
0.35 GB/s was already at the per-file-overhead ceiling. The production
cascade hits 0.32 GB/s for the same reason: 5688 files with a heavy
small-file tail means per-file HTTP setup dominates over network.

**Implications:**
- vllm-70b rootfs cross-node restore floor is ~7 min per receiver
- CRIU cascade's 1.2 GB/s number was specific to vllm-small's file mix
  (1× 27 GiB pages-14 dominates), NOT representative for rootfs
- To accelerate rootfs cascade, we need to attack per-file overhead:
  EROFS-pack the capture into one big file (Spike 2 already validated),
  ship that one file, mount on receiver. Single-file shape gets us
  back to the CRIU cascade ceiling (~1.2 GB/s) or beyond.

**Remaining gap before this is end-to-end usable:** GH #114 webhook
still needs to inject an init container that POSTs to
`/v1/captures/{hash}/ensure-local` instead of pinning via nodeAffinity.
Without that, restore pods still can't schedule cross-node. The
endpoint exists now; the wiring is the next step.

### Cascade vs PVC — the architectural finding

The 4× difference between vllm-small (1.2 GB/s) and vllm-70b rootfs
(0.32 GB/s) on the SAME code path tells us something important:
**cascade throughput depends heavily on file shape.**

| Workload shape | Cascade speed | Why |
|---|---|---|
| 1 big file (vllm-small CRIU: 1× 27 GiB) | 1.2 GB/s | Network-bound — per-file overhead negligible |
| Many small + medium (vllm-70b rootfs: 5688 files) | 0.32 GB/s | Per-file HTTP overhead dominates |

**PVC reads have zero per-file overhead** — kernel VFS handles
inode/page-cache/prefetch directly. For many-small-files workloads,
PVC is structurally faster than HTTP cascade if backing storage is
sized for throughput:

### Storage options — full table (cluster-confirmed + spec)

Each row is labeled with provenance to avoid hallucinated numbers:

- **(measured)** — benchmarked on this cluster tonight
- **(GCP spec)** — from official GCP docs, not measured by us
- **(cluster present)** — CSI driver installed in test cluster, not benchmarked
- **(vendor claim)** — vendor literature, not validated by us

#### Storage classes installed in the test cluster

(Confirmed via `kubectl get storageclass` 2026-05-19. Beta cluster
may have a different/smaller set — verify before planning.)

| Storage class | Backing | Access | Notes |
|---|---|---|---|
| `premium-rwo` | PD-SSD (pd.csi) | RWO (+ ROX multi-attach) | **measured 100 GiB: 349 MB/s write, 403 MB/s read** |
| `standard-rwo`, `standard` | PD-Standard / HDD | RWO | cluster present, not benchmarked |
| `premium-rwx` | Filestore Premium | RWX | cluster present, not benchmarked |
| `enterprise-rwx` | Filestore Enterprise | RWX | cluster present, not benchmarked |
| `enterprise-multishare-rwx` | Filestore Enterprise multishare | RWX | cluster present, not benchmarked |
| `regional-rwx` | Filestore Regional (multi-zone replicated) | RWX | cluster present, not benchmarked |
| `zonal-rwx` / `standard-rwx` | Filestore Standard tier | RWX | cluster present, not benchmarked |
| `nvcf-sc`, `nvcf-cc-sc`, `nvcf-function-storage-sc` | NVMesh (Excelero) | RWO | cluster present, not benchmarked this session |
| `nvmesh-raid0`/`raid1`/`raid10`/`concatenated`/`xfs-raid10` | NVMesh + raid variants | RWO | cluster present, not benchmarked |
| `longhorn-static` | Longhorn | depends on PV | cluster present, not benchmarked |

#### PD-SSD throughput shape (GCP spec)

Provisioned read throughput scales linearly with size, capped at
per-VM aggregate. **Multi-attach ROX shares the per-volume cap
across attached VMs** — capacity doesn't help when multiple readers
share one PD.

| Volume size | Provisioned read (GCP spec) | Notes |
|---|---|---|
| 100 GiB | 48 MB/s baseline | **measured 403 MB/s** — burst credits in play for short reads |
| 500 GiB | ~240 MB/s | GCP spec, not measured |
| 1 TiB | ~480 MB/s | GCP spec, not measured |
| 2.5 TiB | ~1.2 GB/s | matches the per-VM cap on typical modern VMs |
| 5 TiB+ | still ~1.2 GB/s | paying for capacity past throughput cap |

Per-VM PD aggregate cap depends on machine type — **verify current
GCP docs for A3-Mega's exact number before quoting**.

#### Hyperdisk (GCP spec, NOT measured by us)

GCP's newer disk family. Throughput and IOPS are configured
separately from capacity (you pay for each independently).

| Variant | Use case (GCP positioning) |
|---|---|
| Hyperdisk Balanced | general purpose; pay separately for IOPS and throughput |
| Hyperdisk Throughput | streaming / HDFS-like workloads |
| Hyperdisk Extreme | highest single-disk throughput |
| Hyperdisk ML | optimized for ML model loading; multi-reader access |

**Specific MB/s numbers per variant intentionally omitted** — quote
the current GCP Hyperdisk docs for definitive throughput caps. The
Hyperdisk family generally exceeds PD-SSD on per-disk throughput,
and Hyperdisk ML's multi-reader mode is the most interesting
candidate for our many-pods-reading-one-model use case. **None
benchmarked by us — would need a focused measurement.**

#### Filestore tiers (GCP spec, NOT measured by us)

Per-instance throughput depends on tier and capacity. Numbers below
are general characterization from GCP docs — **cite the current spec
page for precise tier-by-tier numbers**:

| Tier | Characteristic |
|---|---|
| BASIC HDD | Lowest cost, modest throughput |
| BASIC SSD | Higher throughput, single zone |
| ZONAL / STANDARD | Mid-range, single zone |
| ENTERPRISE | Higher throughput, can be multi-zone |
| ENTERPRISE multishare | Multiple small filesystems per instance |
| REGIONAL | Replicated across zones |
| PREMIUM | Highest throughput tier |

Filestore exposes per-instance bandwidth (shared across concurrent
mounts), so multi-receiver reads divide that bandwidth.

#### Object storage via GCSFuse (GCP spec, NOT measured)

Mount a GCS bucket as POSIX filesystem via the GCSFuse CSI driver.
Not currently installed in our test cluster (no GCSFuse storage class
present).

- Per-VM read bandwidth limited by VM egress
- Lower latency for sequential reads, higher for small/random reads
- Suitable as **cold tier** (cross-region durability + reuse), not
  hot read loops
- Cost: GCS standard storage + egress

#### Third-party DFS (vendor claims, NOT validated)

Could be available in customer clusters but require additional setup:

- **DDN EXAScaler (Lustre)** on GCP marketplace — vendor claims
  tens of GB/s aggregate, 1-5 GB/s per client. Unverified by us.
- **Weka** — similar positioning. Unverified.
- **NetApp Cloud Volumes** — similar positioning. Unverified.
- **NVMesh (Excelero)** — already in our cluster but not benchmarked
  this session. *Could be benchmarked next.*

### Cascade vs PVC for 133 GiB many-small-files — what we can say

| Path | Per-receiver (133 GiB, rootfs shape) | Source |
|---|---|---|
| Cascade (current production code) | **0.32 GB/s, ~7 min** | measured tonight |
| Cascade with EROFS-packed rootfs | **projected ~1.2 GB/s, ~2 min** | projection: same code, single-big-file shape ≈ vllm-small CRIU |
| PD-SSD 2.5 TiB ROX, 1 receiver | ~1.2 GB/s | GCP spec, not measured |
| PD-SSD 2.5 TiB ROX, N receivers sharing | per-VM cap divided across attached VMs | **Verify in GCP docs** — multi-attach ROX bandwidth model not double-checked |
| PD-SSD 300 GiB ROX, smaller volume | ~144 MB/s baseline per spec formula | not measured |
| Hyperdisk ML | "multi-GB/s per client" | GCP spec, not measured |
| Filestore Premium | tier-dependent, shared across mounts | GCP spec, not measured |
| Lustre / Weka / Filestore Premium | "multi-GB/s per client" | vendor claim, not measured |
| GCSFuse | ~VM egress bound | GCP spec, not measured |

### Recommendation by customer profile

- **Managed DFS available (Lustre / Weka / Filestore high-tier):**
  PVC RWX. Simpler ops; better fit for rootfs shape. **Validate per
  customer with a real bench.**
- **Only block-storage available (GPD-class):** size for throughput
  (~2.5+ TiB PD-SSD or Hyperdisk Balanced/ML) **and/or** EROFS-pack
  the rootfs capture into one file so cascade hits its 1-big-file
  rate (~1.2 GB/s).
- **Customer with NVMesh** (like this cluster): RWO; can host the
  nvsnap capture cache. Cascade still does cross-node distribution.
- **GCS as cold/cross-cluster tier:** durable storage and source of
  truth for cross-region. First receiver in destination cluster
  pulls from GCS, then intra-cluster cascade fans out.

This frames PVC support not as "an alternative to cascade" but as
"the right tool when the workload shape and storage fabric align."
They compose.

### Component bandwidth ceilings (measured today)

| Layer | Throughput | How measured |
|---|---|---|
| md0 RAID0 write (8 parallel writers, fdatasync) | **2.9 GB/s** | 8× `dd if=/dev/zero ... conv=fdatasync` |
| md0 RAID0 write (single dd) | 1.4 GB/s | single-threaded ceiling |
| md0 RAID0 read (cold, 8 parallel) | 2.5 GB/s | drop_caches + 8× `dd of=/dev/null` |
| Network single TCP stream | 2.0 GB/s | curl → /dev/null, one stream |
| Network 8 parallel TCP streams | **5.4 GB/s** | curl Range, 8 concurrent |
| **Network + disk (8 par curl → md0)** | **2.5 GB/s** | closest to cascade pattern |

**Key insight:** ceiling for cascade-shaped traffic is **~2.5 GB/s** per
receiver on this cluster. We're at 1.2 GB/s. Gap is mostly Go's
net/http stack overhead (curl in C hits 2.5 GB/s with same I/O shape).

### What didn't pan out tonight

- Range chunking (Phase 2 in code): 8 parallel chunks per file →
  regressed to 1.0 GB/s due to multi-stream stack overhead
- Linux splice for receiver zero-copy: microbench showed 1.7 GB/s
  isolated, but production cascade with splice ran at ~0.88 GB/s
  (no faster than userspace WriteAt). Root cause was never isolated
  — possibilities include http.Client small-file contention, Go
  stdlib splice-via-pipe overhead, or the dup-fd bug we hit and
  fixed. Reverted to stable WriteAt path.
- GPD multi-attach ROX: 100 GiB PD-SSD measured at 349 MB/s write,
  403 MB/s read single-VM. Worse than cascade unless you provision
  a 2.5+ TiB PD-SSD (per-VM throughput cap).

### What's queued

- **Rust receiver (GH #112)** — biggest single lever. curl numbers
  (2.5 GB/s in C) show ~2× headroom past Go. Rust with splice/io_uring
  should close that. Scope: ~1 week.

## 2. Caching strategies — design ideas

### Hot tier: in-cluster peer cache (current design)

Every node that pulls a checkpoint becomes a peer cache for it. This
is what the cascade does today. **Properties:**

- Aggregate bandwidth scales linearly with peers (each peer serves)
- log₂(N) cascade-tree depth for fan-out to N nodes
- No central choke point
- LRU eviction needed (#42 — checkpoint lifecycle / retention)

**Hit ratio:** depends on workload churn. For an NVCF-style scenario
where "spin up 40 pods from function X" is common, hot-tier hit ratio
is very high — first pod fetches, next 39 hit local hostPath.

### Warm tier: shared filesystem (per-cluster)

Lustre / Weka / Filestore / FSStore-mount. Restore pods mount the
shared PVC `readOnly: true` directly. **Properties:**

- Single source of truth per cluster
- Aggregate bandwidth bounded by backing-storage capacity
- No cascade orchestration needed — kubelet does mounting

**Code already present:** Phase 2c FSStore (`internal/agent/fsstore.go`)
publishes captures to a configured mount and reads from it on restore.
**Customer status:** none have it yet (beta is GPD-only).

### Cold tier: object storage

S3 / GCS / Azure Blob. Capture node uploads checkpoint; any cluster
in any region downloads. **Properties:**

- Durable — survives cluster teardown
- Cheap storage (~$0.02/GB-month GCS)
- Cross-cluster bridge — the path for inter-region

**Code present but disabled:** nvsnap-blobstore (custom HTTP CAS).
Currently runs as cluster-local. Repurposing to GCS-backed is the
right move.

### Combined tiering

```
       L1 hot         L2 warm           L3 cold
   peer hostPath  →  shared PVC    →   object store
   (local NVMe)     (Lustre/etc)      (S3/GCS)
   nanos           millis            seconds
   GB/s            GB/s              MB/s-GB/s
```

EnsureLocal already implements this: same-node → FSStore → peer cascade
→ blobstore. The right thing is to keep this tiered fetch logic and
swap which tier is "warm" based on cluster config.

### Caching policy levers

1. **Eviction (LRU vs TTL)** — beta cluster sees high churn during
   workload turnover. LRU on hostPath when disk fills past threshold.
2. **Pre-warming** — capture node pushes to N seed peers
   immediately after capture, so the first restore from cluster
   doesn't pay full cascade latency. Phase 4 in the doc, ~3 days work.
3. **Dedup across captures** — different captures of same model share
   model weights. Content-addressed blob naming (currently in
   nvsnap-blobstore for the cold tier) — could extend to hot tier.
4. **Compression at rest** — CRIU `--stream` mode is blocked by CUDA
   plugin (#46). Rootfs path via EROFS already gets compression
   "for free" via the image format (Spike 2).

## 3. Cross-cluster / cross-region data movement

### The fundamental constraint

Within-region: 200 Gbit/s gvnic, ~ms latency, multi-stream goes far.
Cross-region: 10-100 Gbit/s interconnect, 50-200 ms RTT, single TCP
stream is bandwidth-RTT product limited (typically <1 Gbit/s without
window-size tuning).

**Order-of-magnitude projections for 322 GiB at cross-region rates
(NOT measured — calculated from assumed rates):**

- Single TCP with default window: bandwidth-RTT limited, typically
  hundreds of Mbps over WAN → tens of minutes
- Multi-stream parallel (16+ streams): scales until peering capacity
  cap → minutes
- Via GCS multi-part (many parallel readers): minutes to ~1 minute
  if egress bandwidth headroom allows

**These are rough projections, not benchmarked.** Real numbers depend
on Cloud Interconnect tier, VPC peering setup, and concurrent traffic.

### Recommended architecture

**Object storage is the right cross-cluster medium.** Specifically:

- **Capture writes to regional bucket** (GCS multi-region or per-region
  with replication policy)
- **Destination cluster's first receiver pulls from GCS** with
  parallel multi-part GET (15-30 connections in parallel)
- **Destination's other nodes use intra-cluster cascade** from the
  first receiver — they don't hit GCS, just the in-cluster peer

This composes the L1 + L3 tiers: one GCS download per destination
cluster (paid once), then peer cascade fans out for free.

### Cross-region considerations (NOT measured by us)

A3 VMs have high egress bandwidth (200 Gbit/s gvnic per the test
cluster's interface listing) but **GCS-to-VM throughput per VM is
not measured by us this session.** GCP documents per-VM egress
limits and GCS multi-part read scaling separately — quote those
docs for definitive numbers rather than my estimates.

Cross-region (e.g., us-central1 → us-east1):
- Bandwidth depends on Cloud Interconnect tier and VPC peering setup
- Egress cost varies by tier — currently $0.01–$0.08/GiB range per
  GCP pricing docs (verify before quoting precise number)
- TCP single-stream over WAN is bandwidth-RTT-product limited;
  multi-stream is required to saturate

For our use case (cross-region checkpoint hydration), the right path
is **GCS multi-part parallel GET** to a single receiver in the
destination region, then **intra-cluster cascade** to fan out within
the destination cluster.

### Pre-warming for active workload migration

For "shift workloads us-west → us-east on GPU availability" use case:
- Async background replication: every capture in us-west replicates
  to us-east within minutes
- Restore in us-east hits a warm bucket — milliseconds to start,
  seconds to fan out
- Cost: 2× storage, 1× cross-region egress per capture
- Worth it if active migration is the norm (vs. DR which can pay the
  cold restore latency)

## 4. Open questions for the meeting

1. **Beta cluster customer profile** — what's the workload churn rate?
   Determines L1 hot-tier hit ratio expectations + eviction policy.
2. **Cross-cluster latency budget** — minutes acceptable or need
   seconds? Drives pre-warming policy.
3. **Storage budget** — 322 GiB × N captures × 2 (DR replication) is
   real money. Caching policy may need quotas.
4. **GPU residency** — does customer have one persistent workload per
   GPU or constant spin-up/down? Determines cascade-vs-warm-tier mix.
5. **Multi-GPU workloads** — currently blocked on cuda-checkpoint
   libcudart issue (#25). The rootfs-only path handles this but tests
   data movement differently (single big EROFS blob vs many CRIU
   files). Want this measured too.

## 5. What I'd build next, by ROI

| Item | Effort | Throughput gain | When |
|---|---|---|---|
| Rust receiver (#112) | 1 week | 1.2 → 2.5+ GB/s (2×) | Q3 |
| Pre-warm to seed peers | 3 days | first-restore latency: full cascade → seed-local fetch | Q3 |
| Retention/LRU (#42) | 1 week | required before soak | Q3 |
| GCS-backed blobstore | 1 week | cold-tier durability + cross-cluster | Q3 |
| Multi-region replication policy | 1 week | active migration use case | Q4 |

---

*Brief compiled 2026-05-18 from in-cluster benches. All measured
numbers reproducible from `scripts/bench-cross-node-restore.sh`,
`scripts/bench-gpd-vs-cascade.sh`, and inline curl/dd tests on the
agent pods.*
