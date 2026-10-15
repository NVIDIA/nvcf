# Cross-cluster / cross-cloud checkpoint replication

**Status:** Proposed
**Supersedes:** the custom `nvsnap-blobstore` CAS service (L4) as the durability/reach tier.
**Related:** #18 (S3/GCS store), #46 (cross-cluster mobility), #163 (multi-zone L2), #171 (L2 too GCP-specific).

## Motivation

A capture taken on one cluster should be restorable on another — e.g. capture on
**GCP asia**, restore on **AWS us-west-2**, both H100 with the same driver major.

Within a cluster we already have a fast tier: the **L2 per-capture PVC**
(`PerCapturePVCBackend`, Hyperdisk-ML rox on GCP / io2 ROX on AWS). One capture,
N restore pods mount the same ReadOnlyMany volume and fan out at storage-tier
bandwidth. But L2 **cannot leave its zone, let alone its cluster or cloud**:

1. A PVC/PV is a binding in *one* cluster's API server to *one* CSI-provisioned
   disk; another cluster cannot reference it.
2. The backing disk (Hyperdisk-ML, EBS) is zonal — even a second zone in the same
   cluster cannot mount it.
3. The only K8s-native cross-disk move is snapshot → export → import: slow,
   cloud-specific, and still "copy the bytes."

So crossing a zone (and therefore a cluster/cloud) **requires copying the bytes
through a neutral store both sides reach.** Managed object storage (GCS/S3) is
that store.

Today's `nvsnap-blobstore` is a custom stateful HTTP CAS holding a *second copy* of
every capture on a 1 TiB balanced-PD disk (`standard-rwo`, not Hyperdisk-ML) plus
a service we operate (GC, retention, orphan sweeps). It works as a single-cluster
backstop but does not solve cross-cluster, and its custom-store design is the
wrong shape for it.

## Identity is already global

A capture's content hash (`checkpointstore.HashInput`) is:

```
imageDigest + modelID + engineCompatFlags + cudaDriverMajor
            + gpuComputeCapability + captureFormatVersion
```

None of that is cloud- or cluster-specific. An H100 (`sm_90`) / driver-550 capture
has the **identical hash** in asia-GCP and usw2-AWS, so by identity it is already a
valid restore source there. `gpuComputeCapability` in the hash is what *guarantees*
this: a capture only matches a target with a compatible GPU arch; mismatched GPUs
produce different hashes and are correctly refused. **Cross-cluster is therefore a
data-movement + discovery problem, not a correctness one.**

## Design

Replace the custom CAS with **object storage as the canonical durable tier** and a
**stateless replicator** that moves bytes between object storage and the local L2
PVC. L2 remains the universal *local* read/fan-out tier on every cluster.

### Tiers (restore cascade)

```
L1  same-node hostPath cache
L2  local-zone per-capture PVC (ROX fan-out)         <- unchanged, the speed tier
L3  same-cluster peer agent (HTTP)                    <- unchanged
L4  object storage (GCS/S3) via the replicator        <- REPLACES custom nvsnap-blobstore
```

### Components

- **Per-cluster home bucket**, in that cluster's own cloud:
  `gs://nvsnap-asia/<hash>/…`, `s3://nvsnap-usw2/<hash>/…`. Writes stay local + cheap.
- **Replicator** — a stateless per-cluster service (or an agent subcommand), two verbs:
  - **push**: on capture-commit, upload the capture artifact to the home bucket.
  - **pull-through**: on a restore that misses L1/L2/L3, GET `<hash>` from the home
    bucket; on miss, GET from a configured **remote** bucket (cross-cloud HTTPS);
    stream into a **local L2 PVC** via the existing `PerCapturePVCBackend` writer
    Job; then fan out. The pulled bytes are also written to the **local** bucket so
    the *next* restore in that cluster reads local object storage, never cross-cloud
    (**pull-through cache**).

### Discovery (control plane)

Because the object key *is* the content hash, no federated database is needed for
the data path:

- NVCA / operator stamps `nvsnap.io/restore-from=<hash>` on the target pod — the same
  hash it would use anywhere.
- The replicator probes a configured **bucket list** (home + known peers) with a
  HEAD on `<hash>`; first hit wins.
- The per-cluster catalog (SQLite in nvsnap-server) stays for UI only. A thin global
  index (`hash → buckets`) is a later optional optimization, not required for a
  handful of clusters.

### Worked example — capture in asia, restore in usw2

1. Capture commits on asia-GCP → replicator pushes artifact to `gs://nvsnap-asia/<hash>`;
   catalog row written in asia nvsnap-server.
2. A usw2-AWS pod needs `<hash>` (stamped `nvsnap.io/restore-from`). usw2 has no
   L1/L2/L3 copy and no local-bucket copy.
3. usw2 replicator: HEAD `s3://nvsnap-usw2/<hash>` (miss) → GET `gs://nvsnap-asia/<hash>`
   (cross-cloud, **first time only**) → writer Job lands it in a usw2 L2 PVC →
   also caches to `s3://nvsnap-usw2/<hash>`.
4. usw2 restore fans out from its local L2 PVC. Every subsequent usw2 restore of
   this hash is fully local.

## Decisions (ratified)

1. **Per-cluster home buckets + lazy pull-through cache** — not one canonical bucket.
   Keeps writes/reads local; cross-cloud egress is paid once, on the first remote
   restore.
2. **Discovery by content-addressed bucket-probe** — not a federated catalog. The
   per-cluster catalog stays for UI.
3. **Lazy pull-through by default; eager-mirror as a per-policy DR knob** — eager
   gives warm cross-region standby at the cost of always-on replication egress.
4. **One packed artifact per hash** (tar / EROFS) in the bucket — simple, fast
   PUT/GET for rootfs trees. Content-defined chunking for cross-capture dedup is a
   later layer.
5. **Cross-cloud auth** via per-cloud workload identity so each cluster's replicator
   can read the peer cloud's bucket. Concrete cred/trust wiring is per-deployment.

## What this removes

- The custom `nvsnap-blobstore` HTTP CAS service and its 1 TiB balanced-PD `standard-rwo`
  disk (no second persistent copy of every capture; no GC/retention/orphan service).
- The HTTP-CAS serve path and the tier-3 peer-cascade-to-blobstore in the restore
  cascade, replaced by "object storage + local L2 materialization."

Hyperdisk-ML cost stays where it earns its keep — the L2 read tier.

## Non-goals

- Cross-**GPU-arch** restore (different `sm_*`): out of scope by design — the hash
  segments these and they must re-capture.
- Live cross-cluster migration of a *running* process: this is checkpoint/restore
  replication, not live migration.
- A global control plane / federated catalog: deferred; bucket-probe suffices for
  the near-term cluster count.

## Phasing

1. **Replicator push/pull + object-storage backend** (GCS + S3 clients behind one
   interface); wire pull-through into the existing restore cascade in place of the
   tier-3 blobstore fetch. Single-cluster first (push to bucket, pull from bucket).
2. **Cross-cloud probe + pull-through cache** (peer bucket list, cross-cloud creds).
3. **Eager-mirror DR policy** + retention on object storage (lifecycle rules instead
   of our own GC).
4. Retire `nvsnap-blobstore`.

Migration: run object-storage L4 alongside the existing blobstore behind a flag;
cut the cascade over once pull-through is validated, then delete the blobstore.
