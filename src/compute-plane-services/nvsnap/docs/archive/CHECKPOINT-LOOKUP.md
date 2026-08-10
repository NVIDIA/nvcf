# Checkpoint lookup — naming, storage layers, NVCA contract

**Audience:** NVCA implementers + nvsnap operators.
**Question this doc answers:** given a workload, how do we find its checkpoint — on the local node, in a PVC, or in the blobstore?

## TL;DR

Two flows exist in the codebase today:

| | **Flow 1 — CRIU-only** | **Flow 2 — rootfs-capture** |
|---|---|---|
| Trigger | `POST /api/v1/checkpoints` (today's only API path) | Agent flag `--rootfs-capture-cache-dir` set + same POST |
| Identity | pod-instance + timestamp | content-addressed hash (sha256) |
| Local on node | `/var/lib/nvsnap/checkpoints/<podName>__<ns>__<ts>/` | `<RootfsCapture.CacheDir>/<hash>/` |
| Per-capture PVC | none | `nvsnap-capture-w-<shortHash>` |
| Per-capture ConfigMap | none | `nvsnap-capture-<shortHash>` (holds the manifest) |
| Blobstore copy | `<podName>__<ns>__<ts>/...` under `captures/` | content-addressed blobs under `blobs/<2-char-prefix>/...` |
| Fanout cascade | none — restore reads blobstore directly | 3-tier (local → peer-HTTP → blobstore) |
| NVCA lookup key | checkpoint ID (the pod-keyed string) | hash (or shortHash) |

**Flow 2 is the path NVCA × NvSnap integration targets.** Flow 1 still exists but doesn't support the dedup/fanout story we need for the QA 100-pod scenario. Manual checkpoints triggered today via the API go through Flow 1 by default; we need to flip the agent into rootfs-capture mode for Flow 2 to fire.

## Identity (Flow 2 — the one NVCA cares about)

```go
// internal/checkpointstore/hash.go
type HashInput struct {
    ImageDigest          string   // immutable image digest, not :tag
    ModelID              string   // for NIM: NIM_TAGS_SELECTOR; vLLM/SGLang: --model
    EngineCompatFlags    []string // e.g. ["--tensor-parallel-size=2","--dtype=fp8"]
    CUDADriverMajor      int      // e.g. 580 for driver 580.95.05 — captures don't migrate across
    CaptureFormatVersion int      // on-disk schema version
}

hash      = sha256(canonical-json(HashInput))       // 64 hex chars
shortHash = hash[0:32]                              // K8s resource names use this
```

Order doesn't matter — `ComputeHash` sorts `EngineCompatFlags` before hashing, so two pods passing flags in different order produce the same hash. The hash is computed *before* the capture runs; same inputs in any cluster produce the same hash.

`shortHash` (32 hex = 128 bits) is what we use for every K8s resource name. Birthday-collision horizon is ~2^64 entries, well past anything we'd plausibly accumulate.

## Storage layers

Three layers, called L1 / L2 / L3:

```
L1: per-node hostPath cache
      <RootfsCapture.CacheDir>/<hash>/      ← e.g. /var/lib/nvsnap/rootfs-cache/abc123.../
      (in this agent: /var/lib/nvsnap/cache)
      Backed by a hostPath volume. NVMe-fast. Per-node. Not shared.

L2: per-capture PVC                          ← the fanout hero
      nvsnap-capture-w-<shortHash>             writer PVC (writer Job populates it)
      nvsnap-capture-r-<shortHash>             reader PVC (restore-side pods mount this; see "PVC access modes")
      Backing storage class is install-time configurable.

L3: nvsnap-blobstore (cluster-wide HTTP CAS)   ← the resilience floor
      Single Deployment + 1 RWO PVC `nvsnap-blobstore-data`.
      Layout inside the PVC:
        /data/blobs/<2-char-prefix>/<full-sha256>     content-addressed blobs (shared across captures)
        /data/captures/<capture-key>/manifest.json    capture manifest (pointing into /data/blobs/)
```

A "capture's bytes" is the union of L1, L2, and L3 entries for the same hash. Any one of them, if intact, is enough to restore. The fanout cascade is what makes that resilient at scale.

## NVCA lookup contract

NVCA's Hook A needs to answer: **"given a pod about to be created, is there a checkpoint I can restore from?"** The current answer chain:

```
1. Compute the canonical hash from the pod's spec (using ComputeHash above).

2. Read the per-capture ConfigMap to know "does a capture for this hash exist
   AND where does it live?":
      cmName    = "nvsnap-capture-" + shortHash             (cmregistry.go:CMNameFor)
      cmNs      = <agent's --rootfs-cm-namespace>          (today: nvsnap-system)
      cm.Data["manifest.json"] is the JSON-encoded Manifest

   If the ConfigMap is absent → no capture exists yet → cold-boot.

3. If present, deserialize the manifest:
      type Manifest struct {
          Hash                  string            // full sha256
          CapturedAt            time.Time
          SourcePodMeta         map[string]string // namespace, name, uid, image, image_digest, model_id, engine
          CaptureFormatVersion  int
          CapturedOnNodes       []string          // K8s node names that hold this capture
      }

   - CaptureFormatVersion mismatch → invalid; treat as no capture; cold-boot.
   - CapturedOnNodes empty AND no L3 reachable → unresolvable; cold-boot.
   - Otherwise: capture exists and the restore-side can resolve it.

4. Stamp the pod with the restore-from annotation:
      metadata.annotations["nvsnap.io/restore-from"] = hash

   NvSnap's `nvsnap-rootfs-restore` mutating webhook handles everything from here:
   injects nodeAffinity for CapturedOnNodes (so the pod lands somewhere the
   cascade can hit Tier 1 or Tier 2 fast), mounts the reader PVC, and wires
   the inference container's volumeMounts.

NVCA never has to read L1/L2/L3 directly.
```

The ConfigMap is the **lookup index**. The manifest is the **routing table**. The annotation is the **trigger**.

### Why ConfigMap, not a custom CRD or nvsnap-server API call?

- ConfigMap reads are local to the apiserver — sub-ms, no extra service hop.
- The webhook needs the same data anyway; one source of truth.
- Cluster-scoped via name (`nvsnap-capture-<shortHash>`); discoverable by `kubectl get configmap -l nvsnap.io/source-engine=vllm` for ops.
- Migrating to a CRD later is a wire-compat-safe drop-in (same JSON shape in `.status`).

### Discoverability labels on the ConfigMap

`cmregistry.go` stamps these labels on the ConfigMap so ops can list captures by source identity:

```
nvsnap.io/source-namespace=<ns of originating pod>
nvsnap.io/source-engine=<vllm|sglang|trtllm|nim>
nvsnap.io/source-image-base=<image-name minus registry/tag, DNS-sanitized>
```

These are derived from `Manifest.SourcePodMeta` — captures retain a back-pointer to where they originally came from, but only for audit, not for lookup.

## NvSnap-side cascade — how `nvsnap.io/restore-from: <hash>` resolves to bytes

The receiving agent (`internal/agent/capture_cascade.go:EnsureCaptureLocal`) runs this when the webhook calls into it:

```
Tier 1: same-node short-circuit
  if <RootfsCapture.CacheDir>/<hash>/ exists → done
    (zero copy, ~0ms)

Tier 2: peer fetch
  read the per-capture ConfigMap → Manifest.CapturedOnNodes
  for each node in CapturedOnNodes (except self):
    resolve the node's InternalIP via kube API
    GET http://<nodeIP>:<port>/v1/captures/<hash>/manifest    (flat file listing)
    GET http://<nodeIP>:<port>/v1/captures/<hash>/file?path=  (one file per request)
    write to local <CacheDir>/<hash>/
    first peer that returns 200 wins; failed peers don't block
  if any peer fetch succeeded → done

Tier 3: blobstore fallback
  if all peers fail (drained nodes, network partitions):
    GET http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000/v1/capture/<hash>/manifest.json
    for each blob the manifest references:
      GET http://nvsnap-blobstore.../v1/blob/<sha256>
    reassemble into <CacheDir>/<hash>/

After EnsureCaptureLocal returns, Backend.Mount(hash, vm) attaches the
reader PVC (or hostPath bind, depending on backend) onto the restore pod.
```

Tier 1 hits the moment a node has restored this hash even once — repeated restores on the same node are zero-copy. Tier 2 is the scale-out path: as more nodes restore, the set of viable peers grows, and the blobstore stops being a bottleneck. Tier 3 is "resilience floor" — keeps restore working when CapturedOnNodes is empty (source node drained) or unreachable.

## PVC access modes — the writer/reader split

The `-w-` and `-r-` naming reflects K8s lifecycle constraints:

- **`nvsnap-capture-w-<shortHash>`** — writer PVC. RWO (single-attach). The `capture-write` writer Job mounts this and copies the agent's local checkpoint into it. Once the Job exits, the PVC is "full" but still RWO.
- **`nvsnap-capture-r-<shortHash>`** — reader PVC. The PV underneath is the same content but bound through a reader-side claim; access mode depends on backend (see below). This is what restore pods mount.

The split exists because most cloud disks (GCP PD, AWS EBS) are RWO. To fan out to N readers, you need either:
- Multiple readers on the same node (RWO works fine for that).
- A backend that supports multiple readers cross-node (RWX / ROX), which means Filestore / EFS / a custom NFS / Longhorn / etc.

Today's deployed cluster uses `standard-rwo` for nvsnap-blobstore-data — which is fine because the blobstore is a single Deployment, not an N-way fan-out. The per-capture PVCs would need a different storage class to support cross-node ROX.

The `RootfsCapture.Backend` configuration selects:
- `Local` — per-node hostPath, no PVC. `CapturedOnNodes` is exactly one node.
- `GPDRox` — GCP PD reader PVC. ROX after writer detach; cross-node read works once.
- `Filestore` — RWX. Implicit cross-node; manifest leaves `CapturedOnNodes` empty (any node OK).

## Side-by-side example — the same workload, both flows

```
fastapi_echo_sample:0.0.1, pod 0-sr-9f501ca8-...-4717990b60a7 in nvcf-backend
```

**Flow 1 (today's deployed cluster, after our manual POST):**

```
L1 (local):    /var/lib/nvsnap/checkpoints/0-sr-9f501ca8-...__nvcf-backend__20260530-020747/
                 ├ rootfs-diff/...
                 ├ mounts/
                 ├ criu/
                 └ metadata.json

L2:            (none — no per-capture PVC was created)

L3 (blobstore):
  PVC:         nvsnap-blobstore-data  (RWO, 1 Ti, single Deployment-attached)
  inside:
                 /data/blobs/<2-char-prefix>/<sha>      ← content-addressed blobs (shared)
                 /data/captures/0-sr-9f501ca8-...__nvcf-backend__20260530-020747/
                                                          ↑ but the capture-key here is pod+ts

ConfigMap:     (none)

NVCA lookup:   not possible — no hash, no ConfigMap. Restore is by checkpoint-id only.
```

**Flow 2 (same workload, agent in `--rootfs-capture` mode):**

```
hash = sha256(image_digest=sha256:abc..., model_id=, engine_compat_flags=[], cuda_driver_major=580, capture_format_version=1)
     = abcdef...64hex
shortHash = abcdef...32hex

L1 (local):    /var/lib/nvsnap/cache/<hash>/
                 ├ rootfs-diff/...
                 ├ mounts/
                 ├ criu/
                 └ manifest.json

L2:
  Writer PVC:  nvsnap-capture-w-<shortHash>     RWO, populated by writer Job, then detached
  Reader PVC:  nvsnap-capture-r-<shortHash>     ROX (backend-dependent), restore pods mount this
  Manifest:    nvsnap-capture-<shortHash>       ConfigMap, holds {Hash, CapturedOnNodes, ...}

L3 (blobstore):
                 /data/blobs/<2-char-prefix>/<sha>   ← same as Flow 1 (already deduped)
                 /data/captures/<hash>/manifest.json ← but capture-key here IS the hash

NVCA lookup:   cmName = "nvsnap-capture-" + shortHash
               cm = corev1().ConfigMaps(rootfsCMNamespace).Get(cmName)
               manifest = Decode(cm.Data["manifest.json"])
               if manifest.CaptureFormatVersion == CAPTURE_FORMAT_VERSION:
                 pod.Annotations["nvsnap.io/restore-from"] = manifest.Hash
                 → restore-side cascade does the rest
```

## What's missing today (gaps to close before NVCA can lean on Flow 2)

| Gap | Status |
|---|---|
| Agent flag `--rootfs-capture-cache-dir` not set on deployed nvsnap-h100-a | needs DaemonSet config update |
| Agent flag `--rootfs-cm-namespace` not set | same |
| nvsnap-server's `POST /api/v1/checkpoints` doesn't route into the rootfs-capture path explicitly — flow is selected by agent config, not request body | acceptable; document the trigger condition |
| `nvsnap-capture-r-<shortHash>` (reader PVC) creation timing — verify Filestore-vs-GPDRox semantics on nvsnap-a | needs validation when we enable Flow 2 |
| L3 capture-keys still use pod+timestamp (`/data/captures/<podName>__<ns>__<ts>/`), not hash | flagged separately; not blocking NVCA (blobs themselves are already CAS) |

## Appendix — operator cheatsheet

```
# List all captures (by shortHash) in this cluster
kubectl get configmap -n nvsnap-system -l nvsnap.io/source-engine

# Find captures of one image family
kubectl get configmap -n nvsnap-system -l nvsnap.io/source-image-base=vllm-openai

# Find captures originating from one namespace
kubectl get configmap -n nvsnap-system -l nvsnap.io/source-namespace=nvcf-backend

# Inspect a capture's manifest
kubectl get configmap -n nvsnap-system nvsnap-capture-<shortHash> -o jsonpath='{.data.manifest\.json}' | jq

# Find which nodes have a capture cached locally
kubectl get configmap -n nvsnap-system nvsnap-capture-<shortHash> -o jsonpath='{.data.manifest\.json}' | jq -r '.captured_on_nodes[]'

# Force a re-capture (delete the ConfigMap; next eligible pod will cold-boot and re-capture)
kubectl delete configmap -n nvsnap-system nvsnap-capture-<shortHash>
```

## See also

- `internal/checkpointstore/hash.go` — canonical `HashInput`, `ComputeHash`, `ShortHash`
- `internal/checkpointstore/store.go` — `Manifest` struct
- `internal/checkpointstore/cmregistry.go` — ConfigMap naming + labels
- `internal/agent/capture_cascade.go` — 3-tier cascade implementation
- `internal/agent/capture_peer.go` — peer HTTP endpoints
- `docs/transport_architecture.md` (memory) — L1/L2/L3 design rationale
- `nvca-nvsnap/docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md` + `…-DELTA.md` — NVCA-side Hooks A/B + content-hash dedup proposal
