# Phase 5d: Peer-to-Peer Fanout + Blob-Store Backstop

**Status:** design — not yet implemented. Supersedes phase 5b (writer-Job-runs-CRIU) and phase 5c (EROFS compaction). Both were derailed by the same root cause: **CRIU's go-criu RPC client rejects any read-only mount of the images dir in 7 ms**, regardless of whether RO is enforced at the kernel level (`mount -o ro`) or kubelet level (`readOnly: true`).

**Empirical evidence (gathered 2026-05-09):**
- v0.17.13 with hostPath restore → ✅ PASS, /v1/completions works.
- Same v0.17.13 dump on PVC with `mountOptions: [ro,norecovery,nouuid]` → ❌ 7 ms reject.
- Same dump on PVC with `mountOptions: []` + `readOnly: true` on the volume → ❌ 7 ms reject.
- Same dump on PVC with `mountOptions: []` + RW (no readOnly) → ✅ PASS, /v1/completions works.
- EROFS-mounted images dir → ✅ CRIU swrk completes, but go-criu RPC still 7 ms-fails (same bug); 4 min restore due to LZ4 decompression — too slow regardless.

**Architectural conclusion:** any shared read-only mount strategy for cross-node fanout breaks CRIU restore. The fix is to **never mount a shared volume into the restore pod** — give each restore its own writable local copy.

**Related docs:**
- `docs/PHASE5B-CRIU-IN-WRITER-JOB.md` — failed phase 5b architecture (kept for history)
- `docs/PHASE5B-DIAGNOSTIC-PLAN.md` — diagnostic notes that led here
- `docs/PHASE5C-EROFS-COMPACTION.md` — failed EROFS pivot (kept for history)
- `docs/MULTI-GPU-CUDA-RESTORE.md` — orthogonal multi-GPU GPU-state restore work, unchanged

## Architecture

Three storage tiers per cluster, queried in priority order on restore. A
fourth tier (cross-cluster S3) is **layered transparently below the
in-cluster blob service** — the agent never knows about it.

```
PER-CLUSTER VIEW (what the agent sees):

┌─────────────────────────────────────────────────────────────┐
│ Tier 1: Same-node hostPath                                  │
│   /var/lib/nvsnap/checkpoints/<id>/  on the capture node      │
│   Restore: hostPath mount — zero transit                    │
└─────────────────────────────────────────────────────────────┘
                          ↓ if not local
┌─────────────────────────────────────────────────────────────┐
│ Tier 2: Peer agent HTTP                                     │
│   Any GPU node that's already restored this checkpoint      │
│   has it cached locally and serves it via agent HTTP.       │
│   Restore: parallel HTTP GET → local emptyDir/NVMe          │
└─────────────────────────────────────────────────────────────┘
                          ↓ if no peer reachable
┌─────────────────────────────────────────────────────────────┐
│ Tier 3: In-cluster blob service (nvsnap-blobstore, S3 protocol)        │
│   One per cluster. Backed by an RWO PVC.                    │
│   Today: authoritative durable store for the cluster.       │
│   Later: also acts as read-through cache for upstream S3.   │
│   Restore: parallel S3 GET via nvsnap-blobstore endpoint → local       │
└─────────────────────────────────────────────────────────────┘

CROSS-CLUSTER VIEW (transparent to agents — happens between nvsnap-blobstores):

cluster-A nvsnap-blobstore  ←→  AWS S3 (or another cluster's nvsnap-blobstore)  ←→  cluster-B nvsnap-blobstore

  - Bucket replication / mirroring keeps clusters' nvsnap-blobstores in sync.
  - Or nvsnap-blobstore read-through cache: on miss, nvsnap-blobstore pulls from S3 lazily.
  - Agents only ever talk to their cluster-local nvsnap-blobstore.
```

**Each successful restore creates a new peer.** Node B restores from node A → B's
local cache now has it → B is registered as a peer for future C/D/E restores.
The system bootstraps fanout capacity organically.

**The in-cluster blob service is the durability anchor** for that cluster:
when source node A dies, the checkpoint survives in the cluster's nvsnap-blobstore. When
local copies are evicted by retention policy, nvsnap-blobstore is the only source until
someone restores again.

**Cross-cluster scale-out** (when S3 access is available later): captures
made in cluster A propagate to cluster B's nvsnap-blobstore via S3 bucket
replication/mirroring. Agents in cluster B fetch from their local nvsnap-blobstore,
which transparently fetches from S3 on miss. **No agent code changes
required for this transition.**

### Why the per-cluster blob service must exist (even when S3 is available)

A naive "agents talk to AWS S3 directly" architecture has three problems:

1. **Cross-cluster latency.** Cluster B's agent fetching from cluster A's
   region's S3 incurs WAN bandwidth + latency. Cluster-local nvsnap-blobstore with a
   read-through cache from S3 collapses this to LAN latency for repeat
   accesses.
2. **Air-gapped clusters.** Some deployments have no internet egress. The
   in-cluster blob service IS the only durable store there.
3. **S3 outages or quota throttling.** A cluster-local cache keeps recent
   captures restorable even when upstream S3 is degraded.

So nvsnap-blobstore isn't a transitional component — it's a load-bearing piece of the
final architecture. S3 just becomes the cross-cluster transport medium
between nvsnap-blobstore instances when it's available.

## Blob protocol: custom HTTP, not S3-compatible

We build a **minimal custom HTTP blob service** (`nvsnap-blobstore`), not an
S3-compatible surface. AWS S3 access isn't available today, so building
S3-compat now to make a hypothetical future swap easier is YAGNI. When AWS
S3 access arrives later, we add a small adapter (cluster-local cache that
translates between our protocol and S3) — that adapter buys us
cross-cluster goodness without touching agent code.

shared-snapshotter precedent supports this — their stratumd uses a simple
Rust HTTP service for blob serving, not S3-compat.

### Protocol

```
# Content-addressed blobs (sha256-keyed, dedup across captures)
PUT  /v1/blob/{sha256}             body: file stream → 201 Created (or 200 if exists)
GET  /v1/blob/{sha256}             → 200 + body (file stream)
HEAD /v1/blob/{sha256}             → 200 if exists, 404 if not (idempotent dedup check)
DELETE /v1/blob/{sha256}           → 204 (used by GC sweep)

# Capture manifests (which blobs make up a checkpoint)
PUT  /v1/capture/{hash}/manifest.json   body: JSON {files:[{path, sha256, size}, ...]}
GET  /v1/capture/{hash}/manifest.json   → manifest JSON
DELETE /v1/capture/{hash}              → 204 (un-refs blobs; orphans GC'd separately)

# Health / capacity
GET  /v1/healthz                   → 200 with disk usage stats
```

Large objects (e.g., `pages-16.img` at 28 GB) stream via HTTP/1.1 chunked
transfer encoding or HTTP/2. **No multipart upload** — single PUT with
streaming body. The server writes to a temp file, fsyncs, renames into
place after sha256 verification.

Estimated implementation:
- Server: ~150 lines Go (net/http + os.WriteFile streaming)
- Client (in agent): ~150 lines Go (parallel uploads/downloads with concurrency limit)

### Future cross-cluster path

When AWS S3 access is available:
- Add a `nvsnap-blob-sync` sidecar to nvsnap-blobstore that periodically pushes
  new blobs to S3 and pulls missing ones from S3 on demand.
- Agents continue speaking only the local nvsnap-blobstore protocol.
- Cross-cluster scale-out: cluster A's blob-sync pushes to S3 → cluster B's
  blob-sync mirrors from S3 → cluster B agents fetch from local
  nvsnap-blobstore.

## Implementation sequencing

Phase 5d ships in two stages:

**Stage 5d.1 — peer-to-peer only (in-cluster fanout, no durability):**
- Agent peer HTTP server (#57)
- NvSnap-server catalog routing — peer list only, no s3_uri yet (#59)
- Restore-side cascading fetch (peer → peer → fail) (#60)
- Validation: cross-node restore via peer fanout works for vllm-small / sglang-small / trtllm-small / nim-llama-8b.

**Stage 5d.2 — durability backstop (nvsnap-blobstore added):**
- Build nvsnap-blobstore HTTP service + Pod + PVC (#61)
- Agent background uploader → blob store after capture (#58)
- NvSnap-server catalog: add s3_uri field (#59 extension)
- Restore-side cascading fetch falls back to blob store (#60 extension)
- Validation: kill source node after blob upload, verify restore from blob store works.

**Stage 5d.3 — cleanup:**
- Delete obsolete code: gpdrox/, phase 5b runPhase5bDump, EROFS test yamls, etc. (#62)

Stage 5d.1 unblocks the demo. Stage 5d.2 unblocks production. Stage 5d.3 keeps the codebase honest.

## Capture flow

```
T=0      nvsnap-server triggers checkpoint via nvsnap-agent on node A
T=0      Agent: quiesce + cuda Lock + CRIU dump → /var/lib/nvsnap/checkpoints/<id>/
                                                  [~150 s frozen on vllm-small]
T=150s   Source pod RESUMES (cuda Restore+Unlock + SIGUSR2)
T=150s   Agent: register in catalog
           POST /api/v1/checkpoints
             body: { id, hash, node="A", local=true, captured_at, size }
T=150s   Agent: spawn goroutine — start S3 upload in background
           Walk /var/lib/nvsnap/checkpoints/<id>/, for each file:
             1. compute sha256
             2. HEAD s3://nvsnap-blobs/blobs/<sha256>  (idempotent dedup check)
             3. if missing: PUT s3://nvsnap-blobs/blobs/<sha256> [body: file bytes]
           After all files uploaded:
             PUT s3://nvsnap-blobs/captures/<hash>/manifest.json
               { files: [{path, sha256, size}, ...] }
             POST /api/v1/checkpoints/{id}/blob-uploaded
               body: { s3_uri: "s3://nvsnap-blobs/captures/<hash>/" }
T=180s   Upload completes (~30 s for 30 GB on p5.48xlarge's 100 Gbps NIC)
         Catalog updated; checkpoint is now durable.
```

**Source-pod frozen window: 150 s** (same as legacy v0.17.13). Background S3
upload doesn't extend it.

**Failure window:** if node A crashes between T=150s and T=180s (the ~30 s
upload window), the checkpoint is lost. For high-value captures, an opt-in
flag waits for upload completion before reporting success — extends frozen
window to ~180 s.

## Restore flow

```
1. Restore request lands on node B (nvsnap-server picks node).
2. nvsnap-server: GET /api/v1/checkpoints/<id>/sources
     → {
         "local_peers": [
           {"node":"A", "agent_url":"http://10.0.x.y:8082"},
           {"node":"C", "agent_url":"http://10.0.x.z:8082"}   // an earlier restorer
         ],
         "blob_store_uri": "s3://nvsnap-blobs/captures/<hash>/"
       }
3. Restore-side init container (or agent code on node B):
     for peer in local_peers (shuffled for load balancing):
         try HTTP download → /local-cache/<id>/   [ephemeral on instance NVMe]
         if success: break
     else (all peers failed/unreachable):
         download from blob_store_uri to /local-cache/<id>/
4. POST /api/v1/checkpoints/<id>/peer-add
     body: { node="B", agent_url="http://..." }
   (Now B is also a source for future restores.)
5. Restore container: CRIU restore from /local-cache/<id>/  [RW local — no 7 ms reject]
6. Agent: wakeRestoredThreads → vLLM serves /v1/completions.
```

**Latency math (vllm-small, 30 GB):**

| Restore type | Network bytes | Time |
|---|---|---|
| Same-node | 0 (hostPath) | ~60-90 s (CRIU restore) |
| Cross-node from peer | 30 GB at ~10 Gbps in-VPC | ~30 s download + ~60 s restore = ~90 s |
| Cross-node from blob store | 30 GB at ~5-10 Gbps S3 | ~30-60 s download + ~60 s restore = ~90-120 s |

The dominant cost at ~30 GB is the local **write** speed at the receiver (CRIU
images go to disk before restore). On EBS root (~190 MB/s), that's ~150 s
serialized. On instance NVMe (~3 GB/s), it's ~10 s. **Use instance NVMe for
the restore-side ephemeral cache.**

## Component changes

### `nvsnap-agent` (extend existing)

**Add HTTP endpoints (peer server):**
- `GET /v1/checkpoints/{id}/manifest` — JSON list of files in the local dump dir, with sha256 + sizes
- `GET /v1/checkpoints/{id}/file?path=...` — streams the file bytes
- Read-only. Serves from `/var/lib/nvsnap/checkpoints/<id>/`.
- Listens on a separate port (e.g., 8082) so peer downloads don't compete with agent API traffic on 8081.
- Estimated ~150 lines of Go.

**Add S3 client:**
- AWS SDK v2 — likely already a transitive dep. If not, add `github.com/aws/aws-sdk-go-v2/service/s3`.
- Configuration via env: `NVSNAP_BLOB_ENDPOINT`, `NVSNAP_BLOB_BUCKET`, `NVSNAP_BLOB_ACCESS_KEY` (or IRSA on EKS, GKE Workload Identity, etc).

**Background upload after capture:**
- Goroutine triggered post-resume of source pod.
- Walks dump dir, parallel uploads (4-8 workers), content-addressed.
- On completion, POSTs to nvsnap-server to register the s3 URI.

**Cascading download for restore:**
- Pre-restore step (init container OR agent code path).
- Calls nvsnap-server for sources, tries peers, falls back to blob.
- Writes to `/var/lib/nvsnap/checkpoints/<id>/` on the local node (so this restore-target becomes a future peer).

**Local retention / eviction:**
- LRU by total bytes; configurable max-size flag (`--checkpoint-cache-max-bytes`).
- On eviction, POSTs `/api/v1/checkpoints/{id}/peer-remove` to nvsnap-server.

### `nvsnap-server` catalog (extend)

**Schema additions to checkpoints table:**
- `local_peers` (JSON array of `{node_name, agent_url, registered_at}`)
- `s3_uri` (nullable string)
- `blob_uploaded_at` (nullable timestamp)

**New endpoints:**
- `GET /api/v1/checkpoints/{id}/sources` — returns prioritized peer list + blob fallback
- `POST /api/v1/checkpoints/{id}/peer-add` — agent registers itself as a peer
- `POST /api/v1/checkpoints/{id}/peer-remove` — agent deregisters on eviction
- `POST /api/v1/checkpoints/{id}/blob-uploaded` — agent reports S3 upload completion

**Catalog cleanup:**
- Periodic sweep: ping each registered peer's HTTP `/healthz`. Remove dead peers from catalog.
- Don't auto-delete entries that lost all peers but still have `s3_uri` — they're recoverable.

### nvsnap-blobstore deployment (new component, but no custom code)

`deploy/k8s/nvsnap-minio.yaml`:
```yaml
# Single-replica nvsnap-blobstore with a backing PVC.
# Production: replace with multi-replica nvsnap-blobstore + erasure coding, OR migrate to AWS S3.
apiVersion: apps/v1
kind: Deployment   # or StatefulSet for stable identity
metadata: { name: nvsnap-minio, namespace: nvsnap-system }
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: minio
          image: quay.io/minio/minio:RELEASE.2024-01-01T01-00-00Z
          args: ["server", "/data", "--console-address=:9001"]
          env:
            - name: MINIO_ROOT_USER
              valueFrom: { secretKeyRef: { name: nvsnap-minio-creds, key: access-key } }
            - name: MINIO_ROOT_PASSWORD
              valueFrom: { secretKeyRef: { name: nvsnap-minio-creds, key: secret-key } }
          volumeMounts:
            - { name: data, mountPath: /data }
          resources:
            requests: { cpu: "1", memory: "2Gi" }
            limits:   { cpu: "4", memory: "8Gi" }
      volumes:
        - name: data
          persistentVolumeClaim: { claimName: nvsnap-minio-data }
---
apiVersion: v1
kind: Service
metadata: { name: nvsnap-minio, namespace: nvsnap-system }
spec:
  selector: { app: nvsnap-minio }
  ports: [{ port: 9000, targetPort: 9000, name: api }]
```

PVC sized for retention: e.g. 1 TB (`storageClassName: nvsnap-capture` or whatever the cluster has). Single replica is fine — when nvsnap-blobstore is unavailable, peers still serve hot captures; only cold-tier fanout breaks.

## Failure handling

| Failure | Behavior | Mitigation |
|---|---|---|
| Source node crashes during async S3 upload | The ~30 s of in-flight captures are lost. Catalog still has the entry but no available source. | Opt-in `--sync-blob-upload` flag for high-value captures; trades 30 s frozen-window for guaranteed durability. |
| Source node crashes after S3 upload | Hot tier loses one peer. Other peers (if any) and blob store still serve. | None needed; system designed for this. |
| All peers gone, blob store healthy | Restore falls back to blob store. ~30-60 s longer. | None needed. |
| Blob store down | Restore falls back to peers. Captures lacking a live peer can't restore. | nvsnap-blobstore HA (multi-replica) for high uptime; or migrate to AWS S3 for cloud-grade SLA. |
| Network partition between node B and node A | B's peer fetch fails; B falls back to blob store (cross-AZ S3 is reachable). | None needed. |
| Catalog out-of-date (stale peer URL) | B's peer fetch fails (connection refused or 404); B retries next peer or blob. | Periodic peer health-check sweep on nvsnap-server. |

## Validation gates (before declaring 5d done)

1. ✅ vllm-small: capture on node A, restore on node B (different node), `/v1/completions` works.
2. ✅ Failure injection: kill node A after S3 upload completes, restore on node B from blob store.
3. ✅ Failure injection: kill nvsnap-blobstore, restore from peer alone (still works).
4. ✅ All four single-GPU workloads pass `test-e2e.sh`: vllm-small, sglang-small, trtllm-small, nim-llama-8b.
5. ✅ Storage cost measurement: confirm content-addressed dedup saves ≥ 30% across re-captures of the same workload.
6. ✅ Same-node restore is faster than cross-node restore (proves the priority cascade actually short-circuits).

## What we keep / delete from earlier phases

**KEEP:**
- `cmd/restore-entrypoint/main.go` — the wakeRestoredThreads timeout fix (v0.18.6) is a genuine robustness improvement, unrelated to the architecture.
- Agent-side CRIU dump path (legacy v0.17.13). Local hostPath capture is the foundation.
- `internal/checkpointstore/store.go` `Manifest` / `VolumeMeta` types, with new fields for peer list + s3_uri.
- Helper functions: `mirrorOverlayDir`, `parseSkippedResources`, `getMappedFilesInfo`, etc — needed for capture.

**DELETE (after 5d validates):**
- `internal/checkpointstore/gpdrox/` — entire package. PV-flip + multi-attach RO is gone.
- Phase 5b code in `internal/agent/checkpoint.go`: `runPhase5bDump`, `stagePhase5bMetadata`, `phase5bMetadataInputs`, `phase5bDumpResult`.
- Phase 5b `Kind=criu` branch in `cmd/agent/capture_write.go::runCRIUDump`.
- Phase 5b `CaptureSource.CRIURPCOptionsJSON`.
- Phase 5b unit tests: `internal/agent/checkpoint_phase5b_test.go`, `internal/criu/rpc_dump_json_test.go`, `cmd/agent/capture_write_test.go` (the criu-source tests).
- gpdrox-aware patches in `scripts/test-e2e.sh` (the Python YAML mutator block).
- The PVC restore manifest patching logic.

**ADD:**
- `internal/agent/peer_server.go` — HTTP endpoints serving local checkpoints.
- `internal/agent/blob_uploader.go` — S3 client + async upload after capture.
- `internal/agent/cascade_fetch.go` — download with peer→peer→blob fallback.
- `internal/server/sources.go` — catalog routing endpoints.
- `internal/server/catalog/peers.go` — peer registration + eviction.
- `deploy/k8s/nvsnap-minio.yaml` — nvsnap-blobstore deployment.
- `scripts/test-e2e.sh` — replace PVC patching with cross-node flag (`E2E_CROSS_NODE=1` makes Step 9 schedule restore on a different node).

## Out of scope (deferred — implement after 5d v1 ships)

- **Pre-warming downloads.** When nvsnap-server schedules a restore, the
  cluster-local nvsnap-blobstore + selected target node could begin pulling the
  checkpoint from S3 / a peer **before** the restore pod is even scheduled.
  Init-container download time → ~0 because the bytes are already local
  by the time the pod starts. The agent and nvsnap-blobstore already have the hooks
  needed (cluster-local nvsnap-blobstore has the source URL from nvsnap-server; agent
  can be told to pre-cache); the orchestration is a nvsnap-server scheduling
  hint that fires off "warm node X for checkpoint Y" alongside the restore
  decision. Not in v1 — adds value only at scale where many restores share
  predictable hot checkpoints.
- Multi-replica nvsnap-blobstore with erasure coding (single replica acceptable for v1).
- Geo-replicated blob store (single region acceptable for v1).
- BitTorrent-style swarm protocol (sequential peer-fetch acceptable for v1).
- Automatic upload to AWS S3 from nvsnap-blobstore (manual lifecycle policy acceptable).
- Multi-GPU (still in `docs/MULTI-GPU-CUDA-RESTORE.md`, unrelated track).
- Cross-cluster fanout (single bucket per cluster acceptable for v1; cross-cluster = bucket replication, separate work).

## Bottom line

Phase 5d is the **simplest architecture that survives single-node failure**. It uses K8s primitives + a stock nvsnap-blobstore deployment + agent code extensions — no new custom services. CRIU always sees a writable local mount, so the 7 ms RPC reject we've been chasing is gone by construction. Demos run in ~90-120 s for cross-node fanout, which is well inside the 5-min budget.

The blob store **is** the durability story. Peers are the speed story. Local hostPath is the same-node-restore story.
