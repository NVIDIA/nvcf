# NVSNAP Data Transport Architecture

**Status:** design proposal, partially implemented
**Audience:** engineering, product, customer engineering
**Date:** 2026-05-17

---

## Executive summary

NVSNAP produces checkpoint artifacts (30 GiB to 322 GiB+). Customers want to:

1. **Move them fast within a cluster** — 1 source node → N target nodes for fan-out scenarios
2. **Ship them to S3/GCS** — for archival, cross-region replication, durable storage
3. **Pull them from S3/GCS into another cluster** and fan them out to N nodes

Today we deliver (1) at ~1.4 Gbit/s single-stream on networks that can do 36 Gbit/s and storage that absorbs 10 Gbit/s. We have **no implementation** of (2) or (3) — only a design abstraction that anticipates them.

**This document proposes a 5–7 week program** to close all three gaps with measured throughput targets.

```mermaid
flowchart LR
    subgraph "Cluster A (capture)"
        CapNode[Capture Node]
        BlobA[(nvsnap-blobstore A)]
        OtherA[N Other Nodes]
    end
    subgraph Cloud["S3 / GCS"]
        S3[(Object Storage)]
    end
    subgraph "Cluster B (restore)"
        BlobB[(nvsnap-blobstore B)]
        FanoutB[N Restore Pods]
    end

    CapNode -->|L1 intra-cluster| BlobA
    BlobA -->|L1 fan-out| OtherA
    BlobA -.->|L2 egress| S3
    S3 -.->|L2 ingress| BlobB
    BlobB -->|L3 fan-out| FanoutB

    style CapNode fill:#bfd
    style BlobA fill:#bfd
    style OtherA fill:#bfd
    style S3 fill:#fec
    style BlobB fill:#cbf
    style FanoutB fill:#cbf
```

---

## The three transport legs

Each leg has its own bottleneck and its own design.

```mermaid
flowchart TB
    subgraph "L1 — Intra-cluster"
        L1desc["Node ↔ node, agent ↔ blobstore,<br/>blobstore → fan-out to N nodes<br/><br/><b>Today:</b> 1.4 Gbit/s single-stream<br/><b>Target:</b> ~10 Gbit/s per node, log(N) fan-out"]
    end
    subgraph "L2 — Cluster ↔ Object Storage"
        L2desc["nvsnap-blobstore ↔ S3/GCS<br/>Cross-region replication leg<br/><br/><b>Today:</b> NOT IMPLEMENTED<br/><b>Target:</b> parallel multi-part, ~10 Gbit/s"]
    end
    subgraph "L3 — Cross-cluster hydration"
        L3desc["S3 → dest cluster N nodes<br/>Composes L1 + L2<br/><br/><b>Today:</b> NOT IMPLEMENTED<br/><b>Target:</b> end-to-end measured, predictable"]
    end

    style L1desc fill:#bfd
    style L2desc fill:#fec
    style L3desc fill:#cbf
```

---

## L1: Intra-cluster transport

### What we have today

```mermaid
sequenceDiagram
    autonumber
    participant Pod as Restore Pod
    participant Agent as Agent (target node)
    participant Peer as Peer Agent
    participant Blob as nvsnap-blobstore
    Note over Pod,Blob: EnsureLocal cascade (Phase 5d)
    Pod->>Agent: needs checkpoint hash X
    Agent->>Agent: Tier 1: local cache?
    Note right of Agent: miss
    Agent->>Peer: Tier 2: peer fetch (sequential, single stream)
    Note right of Agent: ~1.4 Gbit/s
    Peer-->>Agent: file 1
    Peer-->>Agent: file 2
    Peer-->>Agent: ... 5,700 more files
    Note right of Agent: OR if no peer:
    Agent->>Blob: Tier 3: blob store fallback
    Blob-->>Agent: same single-stream pattern
```

**Measurements (Spike 3, 2026-05-15):**

| metric | value |
|---|---|
| Wire ceiling (iperf3 pod ↔ pod, 1 stream) | 36 Gbit/s |
| Storage ceiling (blobstore aggregate) | ~10 Gbit/s (NVMesh-specific) |
| **Actual single-stream PUT/GET we deliver** | **~1.4 Gbit/s** |
| Utilization of available bandwidth | **~14% of storage / ~4% of wire** |

### What we propose for L1

```mermaid
sequenceDiagram
    autonumber
    participant Pod as Restore Pod
    participant Agent as Agent (target node)
    participant Peer1 as Peer A
    participant Peer2 as Peer B
    participant Peer3 as Peer C
    participant Blob as nvsnap-blobstore

    Pod->>Agent: needs checkpoint hash X
    Agent->>Agent: Tier 1: local cache?
    Note right of Agent: miss
    par Multi-source parallel pull
        Agent->>Peer1: chunks 1-32, parallel
        Agent->>Peer2: chunks 33-64, parallel
        Agent->>Peer3: chunks 65-96, parallel
        Agent->>Blob: chunks 97-128, parallel (fallback)
    end
    Peer1-->>Agent: parallel stream
    Peer2-->>Agent: parallel stream
    Peer3-->>Agent: parallel stream
    Blob-->>Agent: parallel stream
    Note right of Agent: ~10 Gbit/s aggregate per receiver
```

**Expected impact:** Spike 3 measured 7× scaling from single-stream to 8-way PUT (and symmetrically for GET). Receiver-side throughput goes from 1.4 Gbit/s to ~10 Gbit/s — close to storage saturation.

### What fan-out at scale looks like

The big win is when one source must serve N target nodes (200-pod scale-out, blue-green deploy, regional broadcast).

```mermaid
flowchart TB
    subgraph S[Source — first node with hash]
        Seed[Seed Agent]
    end
    subgraph T["Tier 1 — 8 nodes (peers pull from seed)"]
        T1[T1-A]
        T2[T1-B]
        T3[T1-C]
        T4[T1-D]
        T5[T1-...]
    end
    subgraph T2["Tier 2 — 64 nodes (peers pull from any seed or tier 1)"]
        T2a[Many]
    end
    subgraph T3["Tier 3 — N nodes (torrent-style swarm)"]
        T3a[All N]
    end

    Seed --> T1
    Seed --> T2
    Seed --> T3
    Seed --> T4
    Seed --> T5
    T1 --> T2a
    T2 --> T2a
    T3 --> T2a
    T2a --> T3a

    style Seed fill:#bfd
```

**Math for the 200-pod / 100-node case (vllm-70b at 133 GiB per node):**

| approach | time-to-all-nodes-ready |
|---|---|
| Naive: every node pulls from one blobstore | **~3 hours** (13.3 TiB / 10 Gbit/s) |
| Torrent-style at 36 Gbit/s pod-to-pod | **~110 s** |

The 100× improvement comes from the fan-out, not from any single connection being faster.

---

## L2: Cluster ↔ Object Storage (S3 / GCS)

### What we have today — nothing

```mermaid
flowchart LR
    Agent[Agent] -->|put hash X| Blob[(nvsnap-blobstore)]
    Blob -.->|??| S3[(S3 / GCS)]
    style S3 stroke-dasharray: 5 5
    style S3 stroke:#f00,stroke-width:2px
```

An object-storage replicator now ships (`internal/agent/replication_push.go` / `replication_pull.go`): captures push to a per-cluster bucket and restores pull through it lazily. The cross-cluster design is documented in [design/cross-cluster-replication.md](design/cross-cluster-replication.md). The remaining items below (s3-rdma transport, eager cross-cluster mirroring) are designed but not yet implemented.

### What we propose for L2

```mermaid
flowchart TB
    subgraph Cluster
        Blob[(nvsnap-blobstore)]
        Uploader[Cloud Uploader<br/>—new—]
    end
    subgraph S3backend["S3 / GCS"]
        Part1[part-1]
        Part2[part-2]
        Part3[part-3]
        PartN[part-N]
    end
    Blob --> Uploader
    Uploader -->|parallel multi-part PUT<br/>~10 Gbit/s aggregate| Part1
    Uploader -->|parallel multi-part PUT| Part2
    Uploader -->|parallel multi-part PUT| Part3
    Uploader -->|parallel multi-part PUT| PartN

    style Uploader fill:#fec
```

**Components to build:**

| component | what it does | est. effort |
|---|---|---|
| `S3Store` implementing `checkpointstore.Store` | Persists captured blobs to S3 with the existing CAS hash addressing | 1 week |
| Parallel multi-part uploader | Splits artifact into chunks, threaded PUT, retries, hash verification | 1 week |
| Parallel multi-part downloader (mirror) | For the ingress side; reused by L3 | 3 days |
| Auth / IAM plumbing | S3 STS, GCP workload identity, NGC pull secrets | 3 days |
| Bench harness | Measure throughput; compare single-stream to N-way | 2 days |

**Both paths are first-class:** the s3-rdma service is a coordinated workstream (separate team), expected to be available alongside this work. The S3Store ships with both transports wired from day one, selected at runtime by cluster capability:

```mermaid
flowchart LR
    subgraph "L2 path options"
        direction TB
        Rdma[s3-rdma via NIXL<br/>~100 Gbit/s, RoCE-capable clusters<br/>primary on capable hardware]
        Vanilla[Parallel HTTP multi-part<br/>~10 Gbit/s, anywhere<br/>production path for non-fabric clusters]
    end
    Caller[agent / blobstore] --> Selector{Cluster<br/>capability<br/>probe}
    Selector -->|has RDMA fabric| Rdma
    Selector -->|no fabric| Vanilla
    style Vanilla fill:#bfd
    style Rdma fill:#fec
```

Both target the same `S3Store` interface so the caller doesn't see the difference. See "Design decisions" section at the end.

---

## L3: Cross-cluster hydration

This is L1 + L2 composed. We download from S3 to one (or a few) seed nodes in the destination cluster, then fan out using the L1 parallel cascade.

```mermaid
sequenceDiagram
    autonumber
    participant SrcAgent as Cluster A agent
    participant SrcBlob as Cluster A blobstore
    participant S3 as S3 / GCS
    participant DestBlob as Cluster B blobstore
    participant DestSeeds as Cluster B seed nodes
    participant DestFanout as Cluster B N pods

    Note over SrcAgent, S3: Phase 1 — Egress (L2)
    SrcAgent->>SrcBlob: capture committed (existing)
    SrcBlob->>S3: parallel multi-part upload<br/>(new — L2)
    Note over S3, DestBlob: Phase 2 — Cold cluster receives hash reference
    DestBlob->>S3: parallel multi-part download<br/>(new — L2)
    DestBlob->>DestSeeds: pre-stage to N seed nodes<br/>(L1 extension)
    Note over DestSeeds, DestFanout: Phase 3 — Fan-out (L1 cascade)
    DestSeeds->>DestFanout: torrent-style cascade<br/>(L1 parallel multi-source)
```

**End-to-end target metric:** "Capture in Cluster A completes → all N pods in Cluster B are Ready." For a 70B model fanning out to 100 nodes:

```mermaid
flowchart LR
    Cap[Cluster A<br/>capture done] -->|L2 egress<br/>~5 min| S3[(S3)]
    S3 -->|L2 ingress<br/>~5 min<br/>+ pre-stage to 10 seed nodes| Seeds[Cluster B<br/>10 seeds warm]
    Seeds -->|L1 fan-out<br/>~2 min| All[Cluster B<br/>100 nodes hydrated]
    style Cap fill:#bfd
    style S3 fill:#fec
    style All fill:#cbf
```

(Times above are projected; not measured. Phase 2c measures them.)

---

## What needs to happen — phased delivery

```mermaid
gantt
    dateFormat YYYY-MM-DD
    title NVSNAP Transport Roadmap (relative weeks)
    section L1 (intra-cluster)
    Parallel multi-source cascade fetch     :a1, 2026-05-19, 14d
    Data-locality pod placement             :a2, after a1, 7d
    Pre-staging seed service                :a3, after a1, 14d
    section L2 (S3 / GCS)
    S3Store impl + auth plumbing            :b1, after a1, 7d
    Parallel multi-part uploader            :b2, after b1, 7d
    Multi-part downloader                   :b3, after b2, 3d
    Benchmark + tune                        :b4, after b3, 3d
    section L3 (hydrate)
    End-to-end wiring                       :c1, after b3, 5d
    Cross-region measurement                :c2, after c1, 3d
```

**Cumulative deliverable:**

| at end of week | what's possible |
|---|---|
| W2 | 8× faster intra-cluster fan-out (L1 parallel cascade) |
| W3 | Smart pod placement reduces transfers for multi-pod-per-node cases |
| W5 | Smaller transfers via pre-staged seeds |
| W7 | First S3 upload from nvsnap-blobstore at parallel-line-rate throughput |
| W8 | Bidirectional S3 transport |
| W9 | End-to-end cluster-A → S3 → cluster-B hydration measurement |

---

## Design decisions (confirmed 2026-05-17)

### S3 is the primary target

GCS support comes via the same `S3Store`-shaped abstraction later; not in scope for Phase 2.

### s3-rdma is a first-class path, not optional

> **Status:** s3-rdma is **designed, not implemented.** Today only the
> HTTPS multi-part transport ships. The two-transport design below is the
> target; RDMA is gated on hardware + a separate fabric service.

The S3Store implementation is designed with two transports, selected at runtime by cluster capability:

```mermaid
flowchart TB
    subgraph S3Store
        API[S3Store.Put/Get API<br/>content-addressed]
        Router[Transport Selector]
    end
    subgraph Paths
        Rdma[s3-rdma<br/>fabric-local<br/>RoCE/GPUDirect<br/>~100 Gbit/s]
        Http[Parallel multi-part HTTPS<br/>any cluster<br/>~10 Gbit/s]
    end
    Caller[agent / nvsnap-blobstore] --> API
    API --> Router
    Router -->|RDMA available| Rdma
    Router -->|fallback / non-fabric| Http
    Rdma --> S3[(S3)]
    Http --> S3

    style Rdma fill:#fec
    style Http fill:#bfd
```

The router picks based on cluster capability probed at startup:
- s3-rdma service reachable AND host has CX-7+ → RDMA path
- Otherwise → HTTPS multi-part

The HTTPS path is **not** the slow fallback to limp along on — it's a real production path for clusters without the fabric, targeting the ~10 Gbit/s we know parallel-multi-part can hit. RDMA is the accelerator on top.

### Cross-cluster use case is dual: DR AND active workload migration

```mermaid
flowchart TB
    subgraph Scenarios
        DR["DR / archival<br/>rare, latency-tolerant<br/>(hours OK)"]
        Active["Active load shift<br/>frequent, sub-minute<br/>e.g. us-west → us-east<br/>when GPU capacity opens up"]
    end
    DR --> Trans[Same L1 + L2 + L3<br/>transport substrate]
    Active --> Trans

    style Active fill:#fec
    style DR fill:#bfd
```

**The active workload migration scenario is the more demanding driver.** Customer pattern:
- Workload running in `us-west` cluster, serving at capacity
- GPU availability opens in `us-east` (or scarce in `us-west`)
- Operator (or autoscaler) decides to shift some/all serving to `us-east`
- Required: ship checkpoint(s) to S3, hydrate dest cluster, bring up pods — **in minutes**

This drives the latency budget for L3 even more tightly than the 200-pod cold-fan-out case in `SCALE-OUT-DESIGN.md`. It also strongly motivates the warm-pool architecture as a follow-on (Phase 3+), because once cross-region migration is fast, the next ask is "make the dest cluster's first inference fast too."

### Durability/consistency

Blobs are content-addressed (SHA-256) so corruption is detectable. Open: ack-on-write or ack-on-replication for S3? For the active migration scenario, ack-on-write to local-region S3 plus async cross-region replication is likely the right call — minimizes capture-side latency, accepts seconds-of-window risk before cross-region durability lands.

---

## Related documents

- `docs/archive/spikes/SPIKE-RESULTS.md` — Spike 1–4 measurements that drive the design
- `docs/archive/spikes/SCALE-OUT-DESIGN.md` — the 200-pod cold-cluster scenario
- `docs/archive/spikes/WORKLOAD-ROADMAP.md` — overall two-track optimization strategy
- `docs/archive/spikes/EROFS-VALIDATION.md` — EROFS as an artifact format; not shipping but revisit triggers captured
- `internal/checkpointstore/mounter.go` — the `Backend` interface that anticipates S3/GCS implementations
- `internal/agent/cascade_fetch.go` — the L1 cascade implementation being upgraded

## Tracking

- Phase 2a — L1 parallel multi-source cascade: branch `feat/parallel-cascade-fetch` (in progress)
- Phase 2b — L2 S3 backend + multi-part: new branch when 2a lands
- Phase 2c — L3 end-to-end: new branch when 2a + 2b land
