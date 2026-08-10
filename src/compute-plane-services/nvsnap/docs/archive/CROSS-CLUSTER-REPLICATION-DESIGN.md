# Cross-cluster checkpoint replication — Design

**Status**: Proposed, 2026-05-12
**Author**: B. Ganesan + Claude
**Related**: ARCHITECTURE.md (tier-3 nvsnap-blobstore), NVSNAP-INTEGRATION-DESIGN.md (NVCA-side)

## 1. Problem

Today, a checkpoint captured on cluster A lives in cluster A's local
nvsnap-blobstore. Cluster B has no path to those bytes — if NVCA stamps
`nvsnap.io/restore-from: <hash>` on a pod scheduled in cluster B, the
agent's tier-3 cascade misses and the pod cold-starts.

NVCA's design assumes one cluster checkpointing a function-version
benefits **all** clusters running that function-version (see
NVSNAP-INTEGRATION-DESIGN.md §"Where exactly does the hash live"). To
deliver that, we need the bytes replicated across clusters
out-of-band.

Goals:
- Once any cluster checkpoints function-version V, every cluster that
  later needs V can restore without going cold.
- Replication is asynchronous — pod creation never blocks on
  cross-cluster pull (the restore cascade handles miss-on-first-pull).
- Each cluster decides what it replicates. An A100-only cluster
  ignores H100-only checkpoints; a CUDA-12.4 cluster ignores
  CUDA-12.8-captured blobs.
- No new coordination layer. Use S3 as the source of truth + SNS for
  fan-out.

Non-goals:
- Multi-region S3 (use S3 cross-region replication if needed; this
  design is replication-agnostic).
- Live migration (this is for cold pod creation, not in-flight pod
  moves).
- Tier-1 / tier-2 cache layout — those stay per-node, populated on
  demand from tier-3.

## 2. Architecture

```
                ┌─────────────────────────────────────────────┐
                │  Cluster A (capture)                         │
                │  nvsnap-agent → nvsnap-blobstore-A               │
                │       │                                      │
                │       └──── PUT chunks → S3 source bucket    │
                │                          + sidecar manifest  │
                └────────────────────────┬─────────────────────┘
                                         │
                                         ▼
                            ┌─────────────────────────────┐
                            │  S3: nvsnap-checkpoints-prod  │
                            │  - <hash>/manifest.json     │
                            │  - <hash>/chunk-<N>.bin     │
                            │  ObjectCreated:* → SNS      │
                            └────────────┬────────────────┘
                                         │ fan-out
                ┌────────────────────────┼─────────────────────────┐
                ▼                        ▼                         ▼
        ┌──────────────┐         ┌──────────────┐          ┌──────────────┐
        │ SQS cluster-B│         │ SQS cluster-C│   ...    │ SQS cluster-N│
        └──────┬───────┘         └──────┬───────┘          └──────┬───────┘
               │                        │                          │
               ▼                        ▼                          ▼
        ┌──────────────┐         ┌──────────────┐          ┌──────────────┐
        │ blobstore-B  │         │ blobstore-C  │          │ blobstore-N  │
        │ replicator   │         │ replicator   │          │ replicator   │
        └──────────────┘         └──────────────┘          └──────────────┘
              │                         │                          │
              ▼ filter pass             ▼ filter pass              ▼ filter (skip)
         GET sidecar +              GET sidecar only        (no GET, just ack)
         GET chunks                 (lazy mode)
```

## 3. The sidecar manifest

Every capture writes a small JSON sidecar to the same S3 prefix as
the chunks. The sidecar is what consumers fetch first to decide
whether to pull the (much larger) chunk set.

```json
{
  "hash": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  "size_bytes": 81604378624,
  "chunk_count": 1237,
  "capture_format_version": 1,

  "captured_at": "2026-05-12T19:00:00Z",
  "captured_in_cluster": "example-gpu-cluster",

  "image_digest": "sha256:abc123...",
  "model_id": "meta-llama/Llama-3.1-8B-Instruct",
  "engine": "nim",
  "engine_version": "1.0.3",

  "gpu_sm": "9.0",
  "gpu_count": 1,
  "tp_size": 1,
  "cuda_driver_major": 555,
  "cuda_driver_full": "555.42.06"
}
```

`hash` is the SHA-256 from `internal/checkpointstore/hash.go`. The
extra fields are out-of-band metadata that the hash doesn't encode
but that consumers need for filtering. The producer cluster computes
the sidecar at capture time and PUTs it to
`s3://nvsnap-checkpoints-prod/<hash>/manifest.json` **last** — after
all chunks are uploaded — so its presence is the "capture complete"
signal.

The S3 notification is configured to fire on `manifest.json` writes
only, not chunk writes. This avoids N+1 events per capture.

## 4. Filter logic (per-cluster)

The replicator in each cluster's blobstore decides per event:

```
on SNS notification:
  HEAD s3://nvsnap-checkpoints-prod/<hash>/manifest.json
  GET  s3://nvsnap-checkpoints-prod/<hash>/manifest.json   # small JSON

  if manifest.gpu_sm not in this_cluster.gpu_sm_set:
    skip (ack SQS, no fetch)

  if manifest.cuda_driver_major < this_cluster.min_cuda_driver_major:
    skip (driver too old, kernel-format mismatch risk)

  if manifest.capture_format_version not in this_cluster.supported_formats:
    skip

  if manifest.image_digest matches this_cluster.image_allowlist:
    pull eagerly
  else:
    record-only (mark blobstore index so tier-3 cascade knows it
                 *can* fetch lazily on first restore)
```

Cluster GPU/driver inventory comes from a daemon already deployed for
other reasons (NFD labels on nodes, aggregated by the nvsnap-blobstore
controller). We don't introduce new cluster-shape discovery.

## 5. Eager vs lazy pull

Two modes; both backed by the same notification stream.

**Eager pull** — replicator pre-fetches all chunks to local
blobstore immediately on notification. Pro: zero-latency first
restore. Con: pays full cross-region S3 egress for blobs that may
never be used.

**Lazy pull** — replicator records the (hash → S3 location) mapping
but doesn't pull chunks. The first time tier-3 cascade misses
locally, it pulls from S3 on-demand. Pro: only pay egress for blobs
that get used. Con: first-restore latency includes the cross-region
pull (can be GBs).

Default: **lazy**. Eager is opt-in per function-version family via a
cluster-level allowlist (e.g., always eagerly pull NIM blobs because
we know they'll get used; lazy-pull everything else). This keeps the
egress bill bounded.

## 6. Eviction

S3 `ObjectRemoved:*` events fan out the same way. Replicator on
receipt:
- Removes the local index entry.
- Triggers local GC of chunks (subject to the existing TTL — recent
  chunks may be retained for in-flight restores).

The producer cluster's retention policy (see internal/server
retention) drives the S3 delete. There's no separate cross-cluster
retention loop.

## 7. Failure modes

| Failure | Handling |
|---|---|
| SQS visibility timeout exceeded | Standard retry; falls into DLQ after N attempts. Operator inspects. |
| Sidecar GET fails (transient S3 error) | Exponential backoff up to 1 hour; then DLQ. |
| Partial chunk pull (network drop mid-replication) | Resume via S3 multi-part on retry; checksum verify per chunk against sidecar `chunk_count` + content hash. |
| Filter says "skip" but a pod later requests this hash | Tier-3 cascade goes direct to S3 (lazy fetch). Filter only controls *pre-emptive* pull, not blocked access. |
| S3 bucket policy mis-permits a cluster | Hard fail; replicator logs and exits the event. No cluster gets a checkpoint it shouldn't have. |
| Hash collision in S3 (impossible at 256 bits, but) | New PUTs to same key are rejected via S3 object lock + retention; producer logs and fails the capture. |

## 8. Operational concerns

**Metrics** (per cluster):
- `nvsnap_replication_events_total{action="skip|eager_pull|lazy_record|fail"}`
- `nvsnap_replication_pull_bytes_total`
- `nvsnap_replication_pull_duration_seconds`
- `nvsnap_replication_lag_seconds` (event_time → local index updated)
- `nvsnap_replication_dlq_depth`

**Alerts**:
- DLQ depth > 0 → page (something fundamentally broken)
- Replication lag P99 > 5 min → warn (S3 throttling or SNS issue)
- Egress bytes/hour > budget → warn

**Cost model**:
- S3 storage: `sum(checkpoint_size) × $0.023/GB-month` in source region
- Cross-region GET: `pull_bytes × $0.02/GB` (egress)
- SNS publish: `events × $0.50/1M` (negligible)
- SQS: `messages × $0.40/1M` (negligible)

For 100 active function-versions × 80 GB each × 10 clusters with eager
pull: 80 TB cross-region transfer = $1600 one-time + per-rebuild. Lazy
mode reduces this to ~10% (only pulled when restored).

## 9. Components to build

- **Producer side (cluster A):** `internal/blobstore/s3uploader.go`.
  After local capture promotes to tier-3, also PUT to S3 (chunks +
  sidecar manifest, manifest written last). Same content-addressed
  layout we already use locally.
- **S3 + SNS + SQS infra:** Terraform module
  `infra/cross-cluster-replication/`. One source bucket + one SNS
  topic + per-cluster SQS queue + IAM roles.
- **Consumer side (cluster B..N):** new
  `internal/blobstore/replicator.go`. Subscribes to SQS, reads
  sidecar, applies filter, updates local index, optionally pulls
  chunks.
- **Filter config:** `nvsnap-blobstore` ConfigMap key
  `replication-filter.yaml` per cluster. GPU SM allowlist, driver
  major min, eager-pull image allowlist.
- **Metrics + alerts:** wire into the existing Prometheus pipeline
  (already running for catalog + retention).
- **Local index extension:** the blobstore's local index needs a
  `remote_only` bit so tier-3 cascade knows "I don't have it locally
  but I know where to find it on S3" — different from a true miss.

## 10. Open questions

1. **Eager-pull policy granularity.** Cluster-level allowlist of
   image digests is the simplest. Should we also support
   function-version-level eager-pull (set by NVCA on the
   `NvSnapFunctionState` CRD)? Probably yes for hot functions, but
   adds NVCA-side complexity. **Recommendation:** start
   cluster-level, add per-function-version override later.

2. **S3 region for the source bucket.** Single region keeps the
   design simple; cross-region restore pays egress per restore.
   Alternative: S3 Cross-Region Replication to mirror the bucket
   into every region. Trades storage cost for egress cost.
   **Recommendation:** start single-region; revisit after we have
   real traffic patterns.

3. **Encryption at rest + in transit.** S3 SSE-KMS for at-rest,
   TLS for in-transit are table stakes. The per-cluster KMS key
   grants are the only operational complexity. **Recommendation:**
   yes from day one; don't retrofit.

4. **What's the source-of-truth for "this hash exists across the
   fleet"?** The S3 bucket itself. NGC's `nvsnap_checkpoint_hash`
   field is *what NVCA stamps onto pods*; the bucket is *whether
   the bytes exist*. If they diverge (NGC says hash X exists, S3
   says no), the restore cascade misses and pod cold-starts. NGC
   shouldn't be authoritative for byte existence.

## 11. Implementation phasing

Phase 1 — **Single-region S3 source-of-truth.** Producer-side
upload; one cluster, no replication. Verifies the bucket layout,
manifest format, and that the existing tier-3 cascade can also
fetch from S3 (extends the cascade from HTTP CAS to S3-backed
HTTP CAS).

Phase 2 — **Replication wiring.** SNS + per-cluster SQS,
replicator daemon, filter config. Default lazy pull. Verifies
fan-out works and filters are sane.

Phase 3 — **Eager-pull + per-function-version overrides.** Lift
the policy surface, ConfigMap → CRD migration if needed.

Phase 4 — **Multi-region.** Cross-region replication, regional
SQS queues, ownership transfer between regions.

Each phase ships independently and adds value alone.
