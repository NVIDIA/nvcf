# NvSnap Architecture

Transparent GPU checkpoint/restore for Kubernetes. NvSnap snapshots a
running GPU workload's process + GPU state (or its on-disk rootfs cache),
stores it across a tiered fabric, and restores it onto N pods — turning a
multi-minute cold start into a sub-minute warm restore, with **zero
application changes**.

This document is the current-state architecture: the node agent, the
control-plane server, the L1–L4 data fabric, the storage-agnostic L2
promotion tier (nvsnap#171), and the NVCA integration (Hook A/B + the
CryoFunctionState → **NvSnapFunctionState** controller, capture-once
nvca#189).

> Diagram palette (draw.io-classic): 🟦 agent/node · 🟩 control-plane ·
> 🟧 storage · 🟪 NVCA · 🟥 capture/CRIU · ⬜ external/workload.

---

## 1. System overview

```mermaid
flowchart TB
    subgraph cluster["GKE cluster (GCP-H100-a / -b)"]
        direction TB
        subgraph wl["namespace: <your workload namespace>"]
            fpod["function pod<br/>(vLLM / SGLang / NIM)<br/>+ utils sidecar"]:::ext
            rox["rox-&lt;hash&gt; PVC<br/>(ReadOnly, restore source)"]:::store
        end
        subgraph nvca["namespace: nvca-operator / nvca-system"]
            op["nvca-operator<br/>(reconcile + CRD install)"]:::nvca
            agentw["nvca agent + webhook<br/>Hook A (stamp) · Hook B (capture)"]:::nvca
            cfs["NvSnapFunctionState CRD<br/>(per function-version)"]:::nvca
        end
        subgraph sys["namespace: nvsnap-system"]
            ds["nvsnap-agent (DaemonSet)<br/>CRIU · cuda-checkpoint · L2 writer<br/>+ in-process webhook"]:::agent
            srv["nvsnap-server<br/>REST API + UI + catalog (SQLite)"]:::cp
            blob["nvsnap-blobstore<br/>(durable CAS backstop)"]:::store
            obs["grafana · jaeger · prometheus"]:::cp
        end
        node["GPU node NVMe<br/>/mnt/.../nvsnap/{cache,staging,overlays}"]:::agent
    end
    gcs["GCS bucket<br/>(cross-cluster replication)"]:::store

    fpod -- "Ready" --> agentw
    agentw -- "POST /v1/checkpoint" --> srv
    srv -- "dispatch" --> ds
    ds -- "dump → promote" --> rox
    ds -- "L1 stage" --> node
    ds -- "L3 backstop" --> blob
    ds -- "L4 replicate" --> gcs
    agentw -- "read/write" --> cfs
    op -- "manages" --> agentw
    rox -- "RO mount on restore" --> fpod
    srv -. "metrics/traces" .-> obs

    classDef agent fill:#d4e6f1,stroke:#2471a3,stroke-width:2px,color:#1b2631;
    classDef cp    fill:#d5f5e3,stroke:#1e8449,stroke-width:2px,color:#1b2631;
    classDef store fill:#fdebd0,stroke:#ca6f1e,stroke-width:2px,color:#1b2631;
    classDef nvca  fill:#e8daef,stroke:#7d3c98,stroke-width:2px,color:#1b2631;
    classDef ext   fill:#eaeded,stroke:#566573,stroke-width:2px,color:#1b2631;
```

**Components**

- **nvsnap-agent** (DaemonSet, one per GPU node) — process/GPU discovery,
  CRIU + `cuda-checkpoint` dump, rootfs capture, the single-copy L2
  writer, and an in-process mutating webhook the operator points BYOC
  workloads at. Runs privileged-ish with `/host` mount + containerd access.
- **nvsnap-server** — REST API + embedded React UI + the checkpoint
  catalog (SQLite on a PVC). Dispatches captures, tracks
  `pvc_promote_state`, serves the cross-cluster replicate API, reverse-
  proxies the observability stack into one pane.
- **nvsnap-blobstore** — content-addressed durable store (L3 backstop).
- **NVCA** (operator + agent/webhook) — the NGC autoscaler. Hook A stamps
  restore intent on admission; Hook B drives the post-Ready capture. State
  lives in the per-function-version `NvSnapFunctionState` (CFS) CRD.

---

## 2. Capture flow (NVCA Hook B → agent → L2 promote)

```mermaid
flowchart TB
    start(["function pod reaches<br/>PodReady = true"]):::ext --> gate0a

    gate0a{"CFS already<br/>Warm?"}:::nvca
    gate0a -- yes --> skipw["skip — drop annotation<br/>(dup trigger)"]:::nvca
    gate0a -- no --> gate0b

    gate0b{"usable capture<br/>exists in server?<br/>(content-addressed)"}:::nvca
    gate0b -- yes --> recover["recover: CFS=Warm<br/>NO re-capture (nvca#104)"]:::nvca
    gate0b -- no --> gate0c

    gate0c{"failure-backoff<br/>window elapsed?"}:::nvca
    gate0c -- no --> suppress["suppress (nvca#167)"]:::nvca
    gate0c -- yes --> claim

    claim{"capture-once CAS<br/>Cold → Capturing<br/>(nvca#189)"}:::nvca
    claim -- "lost (peer holds claim)" --> backoff["skip — another pod<br/>is capturing"]:::nvca
    claim -- "won" --> post["POST /v1/checkpoint<br/>→ nvsnap-server"]:::cp

    post --> agent["nvsnap-agent dumps"]:::agent
    agent --> path{"capture path<br/>(arch × GPU × backend)"}:::red
    path -- "x86 · 1 GPU · vLLM" --> criu["CRIU + cuda-checkpoint<br/>(process + GPU state)"]:::red
    path -- "multi-GPU / arm / Riva·Triton" --> rootfs["rootfs capture<br/>(on-disk model + cache)"]:::red

    criu --> promote
    rootfs --> promote
    promote["L2 promote (Promoter)<br/>rwx writer → durable rox"]:::store
    promote --> ready{"promote<br/>state = ready?"}:::store
    ready -- yes --> warm["CFS = Warm<br/>+ checkpointHash<br/>→ future pods restore"]:::nvca
    ready -- "failed / no-L2" --> cold["stay cold<br/>(L1/L3/L4 cascade still serves)"]:::store

    classDef agent fill:#d4e6f1,stroke:#2471a3,stroke-width:2px,color:#1b2631;
    classDef cp    fill:#d5f5e3,stroke:#1e8449,stroke-width:2px,color:#1b2631;
    classDef store fill:#fdebd0,stroke:#ca6f1e,stroke-width:2px,color:#1b2631;
    classDef nvca  fill:#e8daef,stroke:#7d3c98,stroke-width:2px,color:#1b2631;
    classDef ext   fill:#eaeded,stroke:#566573,stroke-width:2px,color:#1b2631;
    classDef red   fill:#fadbd8,stroke:#c0392b,stroke-width:2px,color:#1b2631;
```

The reconciler is a gated state machine: each gate (already-Warm,
recovery, failure-backoff) short-circuits cheaply before the **capture-
once CAS** that guarantees exactly one in-flight capture per function-
version (see §6). Only the CAS winner dumps; everyone else backs off.

---

## 3. Restore flow (NVCA Hook A → webhook → rox mount)

```mermaid
flowchart LR
    deploy(["function deploy<br/>(scale-out / cold)"]):::ext --> hookA

    subgraph adm["admission (NVCA webhook)"]
        hookA{"CFS Warm &&<br/>hash present?"}:::nvca
        hookA -- no --> coldstart["cold start<br/>(no stamp)"]:::ext
        hookA -- yes --> stamp["stamp pod:<br/>nvsnap.io/restore-from = hash<br/>+ inject rox mount<br/>+ nvsnap-l2-wait init<br/>+ restore-entrypoint"]:::nvca
    end

    stamp --> l2wait["init: nvsnap-l2-wait<br/>poll pvc_promote_state<br/>until ready"]:::agent
    l2wait --> mount["mount rox-&lt;hash&gt;<br/>ReadOnly at /opt/nvsnap"]:::store
    mount --> entry["restore-entrypoint<br/>CRIU restore / cachedir reuse<br/>io_uring · libuv · NCCL reinit"]:::red
    entry --> live(["pod Ready<br/>(warm, prebuilt kernels)"]):::ext

    classDef agent fill:#d4e6f1,stroke:#2471a3,stroke-width:2px,color:#1b2631;
    classDef store fill:#fdebd0,stroke:#ca6f1e,stroke-width:2px,color:#1b2631;
    classDef nvca  fill:#e8daef,stroke:#7d3c98,stroke-width:2px,color:#1b2631;
    classDef ext   fill:#eaeded,stroke:#566573,stroke-width:2px,color:#1b2631;
    classDef red   fill:#fadbd8,stroke:#c0392b,stroke-width:2px,color:#1b2631;
```

`nvsnap-l2-wait` is the gate that makes WaitForFirstConsumer binding work:
the webhook injects it as an init container so the engine container only
starts once the rox PVC is promote-ready, avoiding the
admission-needs-bound / bound-needs-pod deadlock.

---

## 4. L1–L4 data fabric (restore-side cascade)

```mermaid
flowchart TB
    req(["restore needs<br/>hash bytes"]):::ext --> l1
    l1{"L1: node-local<br/>NVMe cache?"}:::agent
    l1 -- hit --> done(["serve"]):::ext
    l1 -- miss --> l2
    l2{"L2: per-capture<br/>rox PVC (this cluster)?"}:::store
    l2 -- hit --> done
    l2 -- miss --> l3
    l3{"L3: nvsnap-blobstore<br/>(durable CAS)?"}:::store
    l3 -- hit --> done
    l3 -- miss --> l4
    l4{"L4: GCS bucket<br/>(cross-cluster)?"}:::store
    l4 -- hit --> pull["replicate → local L2<br/>(replay rox)"]:::store
    l4 -- miss --> coldc(["cold start"]):::ext
    pull --> done

    classDef agent fill:#d4e6f1,stroke:#2471a3,stroke-width:2px,color:#1b2631;
    classDef store fill:#fdebd0,stroke:#ca6f1e,stroke-width:2px,color:#1b2631;
    classDef ext   fill:#eaeded,stroke:#566573,stroke-width:2px,color:#1b2631;
```

Tiers are tried fastest-first. L2 is the fan-out hero — one capture, N
read-only mounts on the same cluster. L4 (GCS) bridges clusters: a miss
that hits L4 replicates into the local L2 so subsequent pods stay warm.

---

## 5. Storage-agnostic L2 promotion (nvsnap#171)

The L2 tier is split into storage-**agnostic** orchestration (lease,
single-copy write, detach barrier, `pvc_promote_state` machine, namespace
scoping) and a pluggable **Promoter** that owns the storage-**specific**
rwx→durable transition + the restore-side mount. The Promoter is selected
at agent startup from the L2 StorageClass.

```mermaid
flowchart TB
    sc(["read L2 StorageClass<br/>.provisioner + .parameters.type"]):::cp --> key["composite key<br/>provisioner[/type]"]:::cp
    key --> lookup{"resolve profile<br/>built-in table ⊕<br/>nvsnap-storage-profiles ConfigMap"}:::cp

    lookup -- "pd.csi…/hyperdisk-ml" --> snapROX["SnapshotClonePromoter<br/>VolumeSnapshot → clone<br/>shared ROX claim"]:::store
    lookup -- "pd.csi…/pd-ssd · ebs.csi" --> snapPP["SnapshotClonePromoter<br/>snapshot → per-pod RWO clone"]:::store
    lookup -- "nvmesh · efs · filestore" --> shared["SharedVolumePromoter<br/>primary PV → Retain<br/>static ROX secondary PV<br/>(zero copy, handle transform)"]:::store
    lookup -- "no match" --> nol2["disable L2<br/>(fall back to cascade)"]:::ext

    snapROX --> art(["durable artifact<br/>N pods mount RO"]):::store
    snapPP --> art
    shared --> art

    classDef cp    fill:#d5f5e3,stroke:#1e8449,stroke-width:2px,color:#1b2631;
    classDef store fill:#fdebd0,stroke:#ca6f1e,stroke-width:2px,color:#1b2631;
    classDef ext   fill:#eaeded,stroke:#566573,stroke-width:2px,color:#1b2631;
```

| Storage | provisioner[/type] | strategy | fan-out |
|---|---|---|---|
| Hyperdisk-ML | `pd.csi.storage.gke.io/hyperdisk-ml` | snapshot-clone | one shared **ROX** |
| Regular PD | `pd.csi.storage.gke.io/pd-ssd` | snapshot-clone | per-pod **RWO** clone |
| AWS EBS | `ebs.csi.aws.com` | snapshot-clone | per-pod **RWO** clone |
| NVMesh | `nvmesh-csi.excelero.com` | shared-volume | **zero-copy** ROX |
| EFS / Filestore | `efs.csi.aws.com` / `filestore.csi…` | shared-volume | **zero-copy** ROX |

A new/3rd-party backend is one `kubectl edit cm nvsnap-storage-profiles`
away — no agent rebuild. No match → L2 is disabled (logged), and restore
falls back to the L3/L4 cascade.

---

## 6. Capture-once: NvSnapFunctionState lifecycle (nvca#189)

When N pods of one function-version deploy cold at once, all N reconciles
would otherwise each fire a capture. The CFS state machine guarantees
exactly one, via a Kubernetes optimistic-concurrency CAS plus a lease.

```mermaid
stateDiagram-v2
    [*] --> Cold
    Cold --> Capturing : Cold→Capturing CAS<br/>(Update w/ live resourceVersion)<br/>exactly one winner; losers get 409
    Capturing --> Warm : capture + L2 promote ok<br/>(claim released)
    Capturing --> Cold : capture failed<br/>(claim released, attemptCount++)
    Capturing --> Capturing : same owner re-reconcile<br/>(lease refresh)
    Warm --> [*] : restore-from stamped<br/>on future pods
    note right of Capturing
        captureOwner + captureLeaseExpiry
        lease TTL = warmupBuffer + checkpointTimeout
                  + promotePollTimeout + margin
        crashed owner ⇒ lease expires ⇒ stealable
    end note
```

The lease TTL provably exceeds the longest legitimate hold, so a valid
in-flight capture is never stolen; only a crashed/hung capturer's claim
expires and becomes stealable. Losers bump
`nvca_nvsnap_checkpoint_attempt_skipped_inflight_total` (≈ N−1 per
capture) — the observable proof the herd was contained.

---

## 7. Capture-path matrix

Which dump path runs depends on arch × GPU count × backend (CLAUDE.md
rule 20):

| Arch | GPUs | Backend | Path |
|---|---|---|---|
| x86 | single | vLLM/SGLang/TRT-LLM/NIM-vLLM | **CRIU + cuda-checkpoint** |
| x86 | multi | any | **rootfs** (no NCCL state in cuda-checkpoint) |
| x86 | single | Riva / Triton | **rootfs** (host-pinned mem unregister aborts) |
| arm | any | any | **rootfs** (cuda-checkpoint unsupported) |

`cachedir` mode (canonical `/opt/nvsnap` cache path, identical at capture
+ restore) lets engines reuse prebuilt JIT/CUDA-graph kernels instead of
recompiling — the bulk of the warm-restore win on compile-heavy models.

---

## 8. Cross-references

- Storage-agnostic L2 design: `docs/design/STORAGE-AGNOSTIC-L2-PROMOTION.md`
- NVCA × NvSnap integration: see the NVCA integration guide (separate, optional component for NVIDIA Cloud Functions deployments)
- Capture-path matrix rationale: `CLAUDE.md` rule 20
