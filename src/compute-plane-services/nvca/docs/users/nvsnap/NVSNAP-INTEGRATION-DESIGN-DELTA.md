# NVCA × NvSnap — design delta (post-implementation review)

**Status:** Proposed, 2026-05-29
**Branch:** `feat/nvsnap-integration`
**Builds on:** [`NVSNAP-INTEGRATION-DESIGN.md`](./NVSNAP-INTEGRATION-DESIGN.md)

This delta layers on top of the original design after reviewing the
landed implementation against a production QA scenario.

## Motivation — the QA scenario the original design under-served

QA deploys **10 different function-versions** of the same NIM/vLLM
image, same model, against the same H100 SKU. Each function-version has
**10 replica pods**. Total: 100 pods, all functionally identical from a
checkpoint standpoint.

Original design + landed code: each function-version is treated
independently (NvSnapFunctionState keyed by `functionVersionID`). All 10
function-versions cold-boot in parallel, each captures its own checkpoint,
each writes its own hash to NGC. Storage dedups via content-addressing
*after* the fact, but the 10× cold-boot work has already been spent — 10
model downloads, 10 engine compiles, 10 CRIU dumps.

QA's complaint is real: **the whole point of checkpoint/restore is that
identical workloads should pay the cold cost once.** This delta closes
that gap.

## Three gaps to close

| # | Gap | Fix |
|---|---|---|
| 1 | Impl drifted from original Q3 — landed code keys by `functionVersionID`, design said "use nvsnap's canonical pod-spec hash" | Rekey the dedup index by `canonicalHash` (see §1) |
| 2 | No protection against concurrent identical workloads ("thundering herd"). 10 function-versions with the same canonical hash all cold-boot simultaneously | Add `Capturing` state + init-container waiter (see §2) |
| 3 | Observability surface (original §H) is three counters. Insufficient for a production system that affects pod startup time | Full metrics/logging/alerts/tracing/audit surface (see §3) |

---

## §1 — Canonical-hash key (closes impl drift from original Q3)

### Current state in code

`pkg/nvca/nvsnap_hook.go` reads `NvSnapFunctionVersionIDAnnotation` and
looks up `NvSnapFunctionState` by that fvID. One NvSnapFunctionState
per fvID. Hash dedup happens at the nvsnap-blobstore CAS layer, not
at the NVCA decision layer.

### Proposed

Compute a **canonical identity hash** at pod-create time, using the
same algorithm nvsnap's `internal/rootfsonly/composer.go` uses (or a
thin Go wrapper that produces an identical hash). Inputs that
affect checkpoint validity:

```
canonicalHash = sha256(
  imageDigest                  // immutable digest, not :tag
  + literalEnvVars             // only env[].value entries; skip valueFrom (per-pod metadata)
  + sortedArgs
  + acceleratorType            // nvidia-h100-80gb
  + gpuCount                   // TP=1 vs TP=4 produce incompatible checkpoints
  + driverVersionMajor         // 555.x vs 560.x — GPU plugin state is driver-coupled
)
```

**Crucially, NOT in the hash:** anything from `valueFrom` env (those are
per-pod / per-function-version fieldRef injections like
`NVCF_FUNCTION_ID`, `INSTANCE_ID`, `NVCF_FUNCTION_VERSION_ID`). Including
them would re-fragment the index back to per-pod or per-fvID, defeating
the whole point. Verified against the live function pod on nvsnap-h100-a
(2026-05-29) — NVCA stamps 9 `valueFrom`-style env vars that would
otherwise poison the hash.

**Override**: pod annotation `nvsnap.io/identity-key: <string>` wins over
the derived hash. Escape hatch for cases where the auto-derivation is
wrong (e.g. function depends on a configMap whose change should invalidate
the checkpoint).

### NvSnapFunctionState becomes identity-keyed

`NvSnapFunctionState` rekeys from `name: <fvID>` to
`name: nvsnap-id-<canonicalHash[0:16]>`. One NvSnapFunctionState per
identity, not per fvID. **N fvIDs that produce the same canonical hash
share one NvSnapFunctionState** — that's the dedup.

Migration: existing CRs (per-fvID) are left in place but ignored by
the new code paths. A one-time backfill job (or just letting them
expire via TTL) cleans them up.

### Failure mode

If two `valueFrom` env vars *should* have been included (e.g. a config
that legitimately differs between functions) and the user didn't catch
it, restores will misbehave at runtime. Mitigations:

- Auto-disable restore for any pod whose annotated `nvsnap.io/identity-key`
  doesn't match the runtime-computed hash → cold-boot fallback, alert
  fires.
- Document the override clearly. Use the `nvsnap.io/identity-key`
  annotation for explicit equivalence.

---

## §2 — Thundering-herd protection (3-state model + waiter)

### State machine on NvSnapFunctionState

| State | Meaning | Hook A behavior | Hook B behavior |
|---|---|---|---|
| `NoCheckpoint` | first pod with this identity | normal admit (no restore-from) | first pod to acquire lease becomes checkpointer |
| `Capturing` | someone holds the lease, is checkpointing | inject `nvsnap-wait` init container | other pods skip (lease denied) |
| `Warm` | checkpoint exists, ready to restore | inject `restore-from` annotation | no-op (already exists) |
| `Failed` | last capture failed | inject `nvsnap-wait` init container that bypasses to cold after backoff | retry from any pod after backoff window |

Transitions:
- `NoCheckpoint → Capturing` — first pod calls `POST /identities/<hash>/lease`, gets `acquired`
- `Capturing → Warm` — nvsnap-server records hash via existing flow, NVCA reconciler updates status
- `Capturing → Failed` — lease TTL expired without success, or checkpointer reported failure
- `Warm → Capturing` — re-checkpoint window (existing `nvsnap_refresh_interval`)
- `Failed → Capturing` — backoff window elapsed, next pod retries

Lease TTL: `WarmupTimeoutSeconds + CheckpointBudgetSeconds` (default ~20 min).

### `nvsnap-wait` init container

Injected by Hook A when state is `Capturing` or `Failed`. Long-polls
nvsnap-server until terminal:

```bash
# pseudocode
identity=<from-pod-annotation>
deadline=$(date +%s) + WaitTimeoutSeconds   # default 600s
while [ $(date +%s) -lt $deadline ]; do
  state=$(curl -fsSL nvsnap-server/identities/$identity)
  case $state in
    Warm)        echo "[nvsnap-wait] checkpoint ready, restore will fire"; exit 0 ;;
    Capturing)   sleep 5 ;;                          # poll every 5s
    NoCheckpoint|Failed)
      echo "[nvsnap-wait] no checkpoint available, cold-booting"
      exit 0    # let inference cold-boot; this pod becomes a candidate checkpointer
      ;;
  esac
done
# timeout — fall back to cold
echo "[nvsnap-wait] timed out after ${WaitTimeoutSeconds}s, cold-booting"
exit 0
```

Notes:
- Always `exit 0` — never block the pod permanently. Worst case, fall
  back to cold-boot and emit metrics.
- Image: bundled with NVCA's existing utils image (curl + bash) so no
  new container pull.
- Resource footprint: 50m CPU, 64Mi RAM — same shape as other init
  containers in the function pod.
- The inference container's restore-entrypoint then either picks up the
  `restore-from` annotation (if state went Warm during wait) or
  cold-boots normally.

### Failure-mode reasoning

- **Checkpointer dies mid-capture** — lease TTL expires, next pod
  acquires lease, state goes `Failed → Capturing`. Waiters time out and
  cold-boot. Eventually catches up.
- **Network partition between waiter and nvsnap-server** — `curl -fsSL`
  returns non-zero, `nvsnap-wait` exits 0 (cold-boot fallback). Don't fail
  the pod over an observability dependency.
- **Capture succeeds but blob upload to L3 (S3/GCS) fails** —
  NvSnapFunctionState stays at `Capturing` until upload completes or
  TTL expires. Tradeoff: we wait for upload before flipping to Warm so
  cross-cluster restore actually works. Acceptable.

### Numbers — QA scenario after this delta

100 pods, same canonical hash:
- 1 pod cold-boots (~2-3 min warmup), captures (~1-2 min)
- 99 pods init-container-wait (~3-5 min idle), restore (~30-60s each, parallel)
- **Total wall time ~5 min** vs. ~30 min today
- **Total GPU-minutes wasted on warmup: 1 vs. 100**

---

## §3 — Observability surface

### Metrics (Prometheus, NVCA-side)

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `nvca_nvsnap_checkpoint_attempts_total` | counter | `result`, `reason` | success / failure / skipped (lease denied) / aborted |
| `nvca_nvsnap_checkpoint_duration_seconds` | histogram | — | from lease-acquire to Warm |
| `nvca_nvsnap_checkpoint_in_progress` | gauge | — | identities currently in `Capturing` |
| `nvca_nvsnap_restore_attempts_total` | counter | `result`, `reason` | success / failure / fallback-cold |
| `nvca_nvsnap_restore_duration_seconds` | histogram | — | pod-create → container Ready (via restore path) |
| `nvca_nvsnap_restore_silent_fallback_total` | counter | `reason` | **critical**: annotation stamped, restore failed silently, cold-booted |
| `nvca_nvsnap_warmup_duration_seconds` | histogram | — | PodReady → first `/health` 200 |
| `nvca_nvsnap_lease_wait_seconds` | histogram | `outcome` | init-container `nvsnap-wait` time, outcome=warm/cold/timeout |
| `nvca_nvsnap_reconciler_queue_depth` | gauge | — | back-pressure indicator |
| `nvca_nvsnap_identities_tracked` | gauge | — | cardinality of distinct identities |
| `nvca_nvsnap_server_request_errors_total` | counter | `endpoint` | network/4xx/5xx talking to nvsnap-server |

**Cardinality discipline**: do NOT label by `fvID`, `podName`, or full
`canonicalHash`. Use `reason` enums and `outcome` enums only. If
per-identity debugging is needed, drop to logs/traces/audit, not metrics.

### Logging (structured JSON via existing logrus)

Every line carries: `identity` (16-char hash prefix), `fvID`, `pod`,
`namespace`, `clusterID`, `reason`.

Levels:
- `ERROR` — any checkpoint/restore failure, with nvsnap-server response body
- `WARN` — lease-timeout fallbacks, silent fallbacks, reconciler retries
- `INFO` — state transitions (`NoCheckpoint → Capturing → Warm`), lease acquire/release
- `DEBUG` — poll-loop ticks, network round-trips

### Alerts (PrometheusRule, ships with operator chart)

| Alert | Expression | Severity |
|---|---|---|
| `NvSnapCheckpointFailureRate` | `rate(nvca_nvsnap_checkpoint_attempts_total{result="failure"}[5m]) > 0.1 * rate(nvca_nvsnap_checkpoint_attempts_total[5m])` | page |
| `NvSnapRestoreSilentFallback` | `rate(nvca_nvsnap_restore_silent_fallback_total[5m]) > 0` | page (this is the "customer thinks it's working but it isn't" case) |
| `NvSnapRestoreFallbackRate` | `rate(nvca_nvsnap_restore_attempts_total{result="fallback-cold"}[5m]) > 0.5 * rate(nvca_nvsnap_restore_attempts_total[5m])` | page |
| `NvSnapServerUnreachable` | `rate(nvca_nvsnap_server_request_errors_total[2m]) > 0.5` | page |
| `NvSnapLeaseStuck` | `nvca_nvsnap_checkpoint_in_progress > 0 unless on() rate(...) for 2 * WarmupTimeout` | warn |
| `NvSnapReconcilerBackpressure` | `nvca_nvsnap_reconciler_queue_depth > 100` | warn |
| `NvSnapIdentityCardinalitySpike` | `delta(nvca_nvsnap_identities_tracked[1h]) > 1000` | warn (catches identity-hash bugs) |

### Tracing (OTel — propagate existing pattern)

NvSnap-agent and restore-entrypoint already emit OTel spans (issue #50).
Extend the trace from NVCA's side:

| Span | When | Parent |
|---|---|---|
| `nvca.nvsnap.hook_a.admit` | pod create webhook | root |
| `nvca.nvsnap.hook_a.identity_compute` | derive canonical hash | hook_a.admit |
| `nvca.nvsnap.hook_a.lease_check` | query NvSnapFunctionState | hook_a.admit |
| `nvca.nvsnap.hook_b.warmup_poll` | Ready → /health 200 | new root |
| `nvca.nvsnap.hook_b.checkpoint_request` | POST to nvsnap-server | hook_b.warmup_poll |
| `nvca.nvsnap.lease.wait` | inside `nvsnap-wait` init container | new root |

Propagation: NVCA pod-mutator stamps `OTEL_TRACE_PARENT` on the
inference container's env, same mechanism the restore-entrypoint
already uses. NvSnap-agent picks it up and emits checkpoint-side spans
under the same trace. **Result: one trace from pod-admission through
checkpoint completion**, viewable end-to-end in Jaeger.

### Audit (nvsnap-server side, existing `audit_log` table from #45)

nvsnap-server already has an audit table. Add identity-keyed entries:

| Action | Resource | Details |
|---|---|---|
| `lease.acquire` | `identity:<hash>` | who, when, TTL |
| `lease.release` | `identity:<hash>` | success/failure |
| `state.transition` | `identity:<hash>` | from → to |
| `restore.fallback` | `identity:<hash>`, `pod:<name>` | reason |

Queryable via existing `/api/v1/audit?resource=identity:<hash>` —
gives a per-identity timeline across all pods/clusters that used it.

---

## Sequencing — what lands first

1. **§3 metrics counters** (mechanically safe — observability for the
   current broken behavior, helps quantify pain before fixing it)
2. **§1 canonical hash key** (the dedup fix, decoupled from waiter mechanics)
3. **§2 lease + `nvsnap-wait` init container** (depends on §1 being live)
4. **§3 alerts** (depends on metrics being live and gathering baseline data)

Each is its own MR. Per gpucr CLAUDE.md rule #5 (>3 files = break into
smaller tasks), this is the natural split.

## Open questions

1. **Where does the canonical-hash compute live?** Either NVCA imports
   nvsnap's `composer.go` directly (creates a build-time dep on nvsnap),
   OR NVCA POSTs the pod spec to nvsnap-server `POST /identities/compute`
   and gets the hash back (adds a network hop per pod admission, but
   keeps nvsnap as a service boundary). Original design (Q2) decided
   "NVCA pushes, doesn't depend on nvsnap internals" — implies the
   network-hop variant. Cost: ~10ms per admission. Tolerable?

2. **Init-container injection — webhook order**: nvsnap's own mutating
   webhook also injects volumes for the restore path. NVCA's
   `nvsnap-wait` init container must run BEFORE the inference container,
   which is the default ordering, but if nvsnap's webhook reorders
   things this could break. Verify ordering in nvsnap's `nvsnap-rootfs-restore`
   webhook before landing §2.

3. **WaitTimeoutSeconds default**: how long should a waiter sit? 10
   minutes feels right for 70B models (whose cold-boot is ~13min). For
   small models 10 min is far too generous. Could derive from
   warm-baseline metric, but for v1 just take a config knob with default
   600s.
