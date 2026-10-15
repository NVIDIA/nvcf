# Restore-side mount prep: move work from admission webhook to init container

**Issue**: TBD (file as nvsnap#202)
**Status**: design, not implemented
**Date**: 2026-06-06
**Author**: balaji + claude

## Problem

The current restore path does all OverlayFS mount work **inside a K8s
mutating admission webhook**. For workloads that produce many extract
paths — DeepSeek-V4-Flash captures 355 — this fails in production:

| Constraint | Reality | Result |
|------------|---------|--------|
| K8s webhook timeoutSeconds: max 30 (default 10) | 355 mount(2) over peer HTTP takes ~70 s sequential, ~5-10 s parallelized | Apiserver closes TCP, webhook fails to encode response |
| `failurePolicy: Ignore` (current) | On timeout, pod admits with NO patches | Pod comes up bare. Restore silently degrades to cold start. |
| `failurePolicy: Fail` (alternate) | On timeout, pod admission rejected | Pod fails to admit. NVCA sees it. But every flaky admission rejects the pod — worse for SLO. |

Observed 2026-06-06 on GCP-H100-a, restoring DeepSeek-V4-Flash:

```
04:13:44  pod applied
04:14:55  webhook computed 363 patches (71 s wall)
04:14:55  error: encode AdmissionReview response: write tcp ...: i/o timeout
          → pod admitted without patches, sglang cold-starts from scratch
```

Three tunings already applied (bumped timeout 5s → 30s; parallelized
the prep loop with 16 workers; agent's mount syscall is fast at kernel
level). None of them solve the architectural mismatch: **kernel mount
work doesn't belong in an admission webhook**.

NVCA orchestrates production function pods. It cannot rely on
"admission usually works." Failure modes must be visible (`kubectl
describe pod` shows real status) and timeout-tolerant (large captures
must not race the K8s admission budget).

## Proposed design

Move mount work out of the admission webhook and into a per-pod
**init container** that runs as part of the pod's normal lifecycle.

### Sequence (new)

```
T+0      apiserver --AdmissionReview-->  webhook
                                          |
                                          | (<100ms: read manifest,
                                          |  emit JSON patches only)
                                          v
T+0+ε    apiserver  <--patches--
         pod accepted

T+0+ε    kubelet starts pod
T+~1s    nvsnap-mount-prep init container starts
              |
              v  POST agent /v1/restore/prep { podUID, hash }
              |        → agent kicks off async overlay prep
              |  GET   agent /v1/restore/prep/{podUID}/status
              |        → poll until "ready" (typical ~3-10 s)
              v
T+~10s   init container exits 0
         kubelet bind-mounts now-ready hostPath volumes into main container
         main container starts
```

### What the webhook still does (admission side)

| Patch | Why |
|-------|-----|
| Add `nvsnap-mount-prep` init container | runs the polling client |
| Add hostPath volumes for ALL captured paths (Volume + VolumeMount in main container) | kubelet WILL try to bind these at main container start; agent must have mounted them by then |
| Add nodeAffinity (manifest.CapturedOnNodes) | unchanged from today |
| Add restore-bundle hostPath + entrypoint override | unchanged from today |

Webhook does **no** kernel work, no peer HTTP, no waiting. Total
admission time target: **<200 ms**. Patches are computed from
in-memory manifest only.

### What the agent does (async side)

New HTTP endpoints on the agent's existing `:8081` server:

```
POST /v1/restore/prep
  body: { podUID: string, hash: string, captureNode: string }
  semantics:
    - idempotent: if already in progress, return 202 with existing job ID
    - if not started: spawn goroutine that prepares ALL overlays for this hash
    - returns 202 immediately with { jobID, totalMounts }
    - prep job calls existing PrepareOverlay for each captured path
    - results stored in jobs map keyed by podUID

GET /v1/restore/prep/{podUID}/status
  returns: { state: "preparing"|"ready"|"failed",
             prepared: int, total: int,
             failures: [{ path, error }],
             startedAt, completedAt }
  semantics:
    - polled by nvsnap-mount-prep init container every ~250ms
    - state="ready" → init container exits 0
    - state="failed" → init container exits 1 with descriptive logs
    - cap polling with timeout (15 min) so a stuck agent doesn't hang pod forever

DELETE /v1/restore/prep/{podUID}
  semantics:
    - cleans up the per-pod overlay scratch + jobs map entry
    - called by the existing overlay-cleanup pod-DELETE watcher
```

### What the init container does

A tiny Go binary, ~150 LoC. Lives in the existing `nvsnap-init` image
(no new image to maintain).

```go
// cmd/nvsnap-mount-prep/main.go (pseudocode)
func main() {
    podUID := os.Getenv("NVSNAP_POD_UID")  // populated via downward API
    hash   := os.Getenv("NVSNAP_RESTORE_HASH")
    agent  := os.Getenv("NVSNAP_AGENT_URL") // typically http://<host-ip>:8081

    // Start prep (idempotent)
    resp := POST(agent + "/v1/restore/prep",
                 {podUID, hash, captureNode: ENV[NVSNAP_CAPTURE_NODE]})

    // Poll status
    deadline := time.Now().Add(15 * time.Minute)
    for time.Now().Before(deadline) {
        s := GET(agent + "/v1/restore/prep/" + podUID + "/status")
        switch s.state {
        case "ready":
            log("mounts ready: %d/%d", s.prepared, s.total)
            os.Exit(0)
        case "failed":
            log("FAILED: %v", s.failures)
            os.Exit(1)
        }
        log("preparing: %d/%d", s.prepared, s.total)
        time.Sleep(250 * time.Millisecond)
    }
    log("timeout after 15min")
    os.Exit(2)
}
```

Resource footprint: <10 MB binary, ~5 MB RAM at runtime, runs for
~5-15 s typically.

## Why this is the right shape

1. **Admission is fast and side-effect-free.** Matches K8s expectations.
   Webhook never does work that could plausibly exceed 1 s.
2. **Slow work is in the pod lifecycle.** Init containers can run as
   long as needed. Failures surface in `kubectl describe pod` exactly
   like any other init container failure.
3. **NVCA sees failures.** If mount prep fails, the init container
   exits non-zero, the pod goes to `Init:Error`, NVCA's pod-readiness
   probe handles it like any other init failure.
4. **No race with K8s timeout budgets.** The init container's timeout
   is set by us (15 min default), independent of webhook config.
5. **Parallelizes naturally with image pull.** Init container runs
   concurrent with later containers' image pulls — currently those
   serialize.
6. **Agent restart is safe.** If agent restarts mid-prep, init
   container's POST re-kicks the job; idempotent semantics handle it.
7. **Same binary in nvsnap-init**. No new image, no new build pipeline.

## Alternatives considered

### A. Just bump K8s webhook timeout to 30s (status quo + tuning)
- Already tried. Even with parallel prep, 355 mounts × cross-node peer
  HTTP overhead exceeded 30 s in observed runs.
- More fundamentally: a webhook that takes 30 s is an SLO landmine.
  Any blip (cluster network, agent restart, peer node pressure)
  cascades into pod-admission failures across the cluster.

### B. Collapse mounts (mount /root/.cache once instead of 5 leaves)
- Reduces 355 → ~20 mounts. Webhook work drops to <1 s.
- BUT: masks base-image content under the mount path. For
  `/root/.cache` that's fine; for `/usr/local/lib/python3.12/dist-packages`
  the base image's `.py` files would be hidden. Need per-path heuristic.
- Doesn't solve the structural problem — still doing kernel work in
  webhook, just less. Next workload that produces 100 mounts of `/usr`
  subdirs falls off the same cliff.
- Probably worth doing AS WELL, to keep the agent's work small. But
  not a substitute for moving the work out of admission.

### C. CSI driver for lazy mount on first access
- Most K8s-idiomatic for "expensive volume setup": ship a CSI driver
  that does the overlay mount lazily.
- Multi-week implementation; CSI is a heavyweight contract.
- Doesn't compose well with our peer-HTTP capture distribution.
- Defer to v2 if NVCA outgrows the init-container model.

### D. Pre-mount at agent startup, not at restore time
- Agent watches manifest CMs; pre-mounts overlay LOWER once per hash.
- Per-pod UPPER is still per-pod-at-restore (can't pre-allocate).
- Saves the lower-side mount cost (small, ~5%); doesn't solve the
  per-pod work that's the bulk of today's 70 s.

## Migration

This is a hot-path change but failure-mode-clean.

1. **Phase 1 (this MR)**: implement the init container + agent API.
   Add a webhook gate flag `--restore-prep-strategy=inline|init-container`.
   Default `inline` (current behavior); operators opt in.
2. **Phase 2**: bench on DeepSeek-V4-Flash with `init-container`. Confirm
   admission <200 ms; restore Ready time within prediction (~13 min for
   this workload).
3. **Phase 3**: flip default to `init-container` after one week of
   validation across all bench workloads.
4. **Phase 4**: delete the inline path + the parallel-prep tuning
   (no longer needed; admission carries zero mount work).

NVCA-side: nothing to change in Phase 1-3. Phase 4 just makes
admission visibly faster — no API change for NVCA.

## Failure modes (new)

| Failure | Init container behavior | NVCA-visible? |
|---------|------------------------|---------------|
| Agent unreachable from pod (network) | POST retries with backoff; eventually exit 2 (timeout) | yes — `Init:Error` |
| Agent crashes mid-prep | re-POST re-kicks job (idempotent); state recovers | usually transparent |
| Peer-node prep fails (cross-node) | agent surfaces in `failures[]`; init container exits 1 | yes — kubectl logs init container shows path |
| OverlayFS mount returns EINVAL | same as above; init exits 1 | yes |
| Disk full on agent | same; agent's prep error includes path + errno | yes |
| Pod deleted mid-prep | existing watcher's DELETE handler cleans up; agent's job map entry GCed | n/a |

Compare to today's silent admission failure → pod cold-starts with no
indication: every failure mode above is **strictly more debuggable**.

## Implementation outline

| Step | Where | LoC est |
|------|-------|---------|
| 1. Add `internal/agent/restore_prep.go` — async prep job manager (sync.Map of podUID → job) | agent | ~150 |
| 2. Add `POST/GET/DELETE /v1/restore/prep` endpoints | agent | ~80 |
| 3. Add `cmd/nvsnap-mount-prep` Go binary | new | ~150 |
| 4. Bundle binary into existing `nvsnap-init` image | docker/init/Dockerfile | ~5 |
| 5. Webhook: switch from inline `PrepareOverlay` to init-container patch | webhook/mutate.go | -100 (deleting), +60 (init container patch) |
| 6. Webhook flag `--restore-prep-strategy=inline\|init-container` | webhook/agent config | ~10 |
| 7. Helm value `nvsnap.webhook.restorePrepStrategy` | helm values | ~5 |
| 8. Webhook MutatingWebhookConfiguration: drop timeout bump (no longer needed) | helm template | -1 line |
| 9. Tests | webhook + agent | ~200 |

Total: ~700 lines added/changed, ~100 removed. Single-day MR.

## Open questions

- **Init container image registry pull**: nvsnap-init image must be
  cached on the node already (it is, the DaemonSet uses it). For
  cold nodes, first pod's init container would pull. Worst-case
  pull adds ~5 s to restore. Acceptable.
- **Agent API authentication**: today the agent's `:8081` is in-cluster
  only (hostNetwork + node-local). Init container hits `localhost:8081`
  via host IP downward API. No new auth needed; same trust boundary
  as the existing peer-HTTP endpoints.
- **What happens if the init container's pod is deleted while polling?**
  init container goroutine dies normally; agent's per-pod overlay
  cleanup fires via the existing pod-DELETE watcher.
- **Concurrent restores of the same hash**: each gets its own
  per-pod UPPER. Agent's job map keys on podUID so parallel restores
  don't collide. The shared LOWER (captured tree) is read-only.
- **NVCA bench impact**: today NVCA's Hook B measures pod-Ready time.
  With init-container path, the admission part drops from ~10 s
  (today, when it works) to <200 ms — that gain is visible in NVCA's
  trace. The mount work moves into pod-init time. End-to-end Ready
  time should be NEUTRAL or BETTER (init container runs concurrent
  with image pull, which today is sequential).
