# Whole-rootfs restore — replace curated extract_paths catalog (nvsnap#88)

## Problem

Today the rootfs path captures the entire container rootfs upperdir into
`tree/rootfs/`, but the restore-side webhook only re-mounts a curated
set of subpaths defined in `internal/rootfsonly/extract_paths.go`:

```
/root/.cache/huggingface
/root/.triton
/root/.cache/torch
/root/.cache/tensorrt_llm
/root/.nv/ComputeCache
/opt/nim/.cache
```

Engine-specific caches outside this list are **captured but never
exposed** on restore. Confirmed empirically 2026-06-06 on
DeepSeek-V4-Flash + sglang TP=8:

```
$ ls tree/rootfs/root/.cache/
deep_gemm       ← present, not exposed → sglang re-JIT compiles for ~15 min
flashinfer      ← present, not exposed
huggingface     ← present, exposed (in catalog)
torch_extensions
tvm-ffi
```

The user-visible symptom: warm-recapture restore runs at the same
speed as cold-start because the cached JIT artifacts are on disk but
the restored pod doesn't see them. The catalog approach asks us to
chase every new engine version's cache location forever, and we'll
always be one engine release behind.

## Proposal

Replace the curated catalog with **dynamic enumeration**: at admission
time, the webhook walks the captured `tree/rootfs/` and emits one
OverlayFS-wrapped writable mount per top-level subdir that has content
above a noise threshold. Every cache the source pod warmed becomes
visible to the restored pod, regardless of which directory the engine
chose.

### Why this is safe

The original catalog-based design was defensive against two real risks
that no longer apply post-nvsnap#194:

- **Cross-pod aliasing**: a path like `/tmp/torchinductor_<pid>`
  embeds the source pod's PID, so sharing it across restored pods
  would cause collisions. With nvsnap#194's per-pod OverlayFS upper,
  every restored pod gets isolated writes — even an aliased path is
  copy-on-write from the lower (captured) into per-pod upper. No
  cross-pod corruption.
- **Per-pod state leak**: paths like `/run/secrets/kubernetes.io/...`,
  `/etc/hostname`, `/etc/hosts`, `/etc/resolv.conf` are
  kubelet-injected and would be wrong if shared. **Fix: exclude
  these at capture time, not at restore time.** Removing them from
  the captured tree means there's nothing to leak.

### Concrete numbers (DeepSeek-V4-Flash, nvsnap-h100-a, 2026-06-06)

Captured `tree/rootfs/` (304 GB total — 149 GB is HF cache duplicated
with the hostPath volume; see content-addressed dedupe doc for the
fix to that):

| Top-level subdir | Size  | Exposed today? | After this MR? |
|------------------|------:|----------------|----------------|
| `/root`          | 149 G | partial (hf-cache only) | full |
| `/usr`           | 136 M | no | yes (deep_gemm/flashinfer cubins in dist-packages) |
| `/sgl-workspace` |  18 M | no | yes (sglang Python source — useful for any in-place edits) |
| `/tmp`           |  23 M | no | **excluded (tmpfs)** |
| `/etc`           |  72 K | no | excluded (per-pod files) |
| `/run`           |  16 K | no | **excluded (kubelet-injected)** |
| `/var`           |  64 K | no | excluded (per-pod log/state) |

The 155 MB of `/usr` + `/sgl-workspace` + non-hf-cache `/root` is
exactly what holds the JIT cache that makes warm-restore fast. Today
that 155 MB is captured but not used; this MR makes it usable.

## Design

### Capture-side exclusions

Add to `internal/rootfsonly/capture.go` (or the treecopy.Copier
exclude list passed in from the orchestrator) a precise EXCLUDE
glob list applied as the rootfs upperdir is copied into `tree/rootfs/`:

```
/tmp                              # tmpfs — ephemeral by spec
/run                              # kubelet-injected mounts (secrets, sa token)
/proc                             # synthetic
/sys                              # synthetic
/dev                              # synthetic
/etc/hostname                     # kubelet-set per pod
/etc/hosts                        # kubelet-set per pod
/etc/resolv.conf                  # kubelet-set per pod
/etc/mtab                         # mount table — stale on restore
/var/run                          # alias of /run on some distros
/var/log                          # log files — per-pod
```

These are the K8s + OCI runtime per-pod paths. The rest of the
rootfs upperdir is the workload's own writes and is what we want to
replay.

### Restore-side enumeration

In `internal/webhook/mutate.go`, after fetching the capture manifest,
walk `tree/rootfs/` on disk and synthesize one `VolumeMeta` per
top-level subdir that:

1. Exists with content >= `MinSubdirBytes` (default 100 KiB — noise
   filter; trivial empty dirs aren't worth a mount).
2. Is not in a small SKIP set of cosmetic-only paths (e.g.
   `/lost+found`, `/proc`, `/sys`, `/dev` — defense in depth even
   though capture-side already excludes them).

Each synthesized VolumeMeta goes through the existing OverlayFS-wrap
helper (`tryOverlayMount` in mutate.go, landed in MR #65) — so every
mount is per-pod writable.

### Granularity: top-level vs. deeper

First iteration: mount at **top-level subdirs**
(`/root`, `/usr`, `/sgl-workspace`, etc.). Simpler enumeration, fewer
mounts, kubelet binds in <100 ms each. Tradeoff: masks the base
image's top-level dirs entirely — but the captured tree only contains
dirs the source pod *wrote to*, and the source pod's writes are
either (a) overlay upperdir writes that we want, or (b) base-image
files that the source pod modified — also what we want.

Optimization for later: walk one level deeper for paths that have
huge subtrees (like `/root/.cache/*`) to avoid masking `/root/.ssh`
etc. Defer.

### Migration

- Phase 1 (this MR): land the dynamic enumeration as a NEW code path
  behind a feature flag `--whole-rootfs-restore` (default off).
  Existing catalog stays. Both code paths coexist; a deploy can roll
  back instantly by flipping the flag.
- Phase 2 (next MR): flip the default to on in production after
  validation on all bench workloads (vllm-small, vllm-8b, sglang-*,
  trtllm-small, e5-mistral, NIM, gpt-oss-120b, DeepSeek-V4-Flash).
- Phase 3: delete `extract_paths.go` entirely + the catalog tests.

### Non-goals

- **Whole-rootfs at the container's `/` via OverlayFS at the runtime
  layer.** That would require modifying containerd / CRI-O, which we
  said we wouldn't do (we operate at the K8s API surface, not the
  runtime). Top-level subdir mounts via volumeMounts achieve 95% of
  the same outcome without runtime changes.
- **Content-addressed dedupe** across the captured tree and hostPath
  volumes. Separate concern (see follow-up `capture-dedupe.md`); this
  MR doesn't try to solve the 304 GB → 152 GB compression. The two
  changes compose cleanly.
- **Files at root level**: `/etc/ld.so.cache` (72 K) is captured today
  but we won't bother mounting individual files — the cost of a
  hostPath mount per file is not worth the speedup of an already-fast
  runtime linker cache regeneration.

## Implementation outline

1. **Capture exclusions**: `internal/rootfsonly/capture.go` —
   pass the exclude list to `treecopy.Copier` when walking the
   source pod's rootfs upperdir. Add a unit test that captures a
   source tree with `/tmp/foo`, `/run/x`, `/etc/hostname` and
   asserts none make it into the captured manifest.
2. **Restore enumeration**: `internal/rootfsonly/enumerate.go` (new
   file) — a `ScanCapturedSubdirs(rootfsDir string) []ExtractPath`
   function that walks one level under `rootfsDir`, returns one
   entry per dir with `size >= MinSubdirBytes`. Used by the webhook.
3. **Webhook wiring**: `internal/webhook/mutate.go` — when the
   `--whole-rootfs-restore` flag is on, call `ScanCapturedSubdirs`
   instead of (or in addition to) reading `manifest.RootfsExtractPaths`.
   Each result becomes a synthetic VolumeMeta and flows through the
   existing `tryOverlayMount` helper.
4. **Tests**:
   - Unit: capture a fake rootfs with `deep_gemm/`, `flashinfer/`,
     `huggingface/` under `/root/.cache/`. Assert all three get
     mounted on restore even though only `huggingface` is in the
     legacy catalog.
   - Unit: capture with `/tmp/foo` + `/run/secrets/x` present in
     source rootfs. Assert neither makes it to `tree/rootfs/` (the
     capture-side exclude filter works).
   - Integration: same as the agent's existing
     `TestPrepare_RealOverlay`, but with multiple captured subdirs
     to verify the per-subdir mount semantics.
5. **Flag plumbing**: `cmd/agent/main.go` adds
   `--whole-rootfs-restore` (default false); `internal/agent/agent.go`
   Config grows a bool; webhook reads it.

## Validation plan

After this MR lands on nvsnap-h100-a:

1. Re-run the DeepSeek-V4-Flash warm-recapture bench from
   2026-06-06. Cold baseline: 1290 s. Warm-with-catalog: ~1126 s (no
   speedup because deep_gemm/flashinfer not exposed). **Target with
   this MR: <600 s** — should skip both the DeepGEMM JIT compile and
   the flashinfer kernel JIT, since both caches will be visible.
2. Re-bench every other workload in `scripts/test-bench.sh` to
   confirm no regression — the existing catalog paths should still
   work because they're a subset of what the enumeration produces.

## Open questions

- **Should the SKIP set be configurable per-cluster?** A customer
  cluster might have additional kubelet-injected mounts we don't
  know about. Probably yes, via helm values, but defer until we hit
  a concrete case.
- **`/lib`, `/lib64`, `/usr/lib`**: these are typically read-only in
  the base image, but if the source pod did `pip install --user` or
  similar, we'd capture the user-installed bits under
  `/usr/local/lib/python*/dist-packages/`. We want to expose those.
  The current top-level approach mounts `/usr` whole, which works.
- **Conflict with hostPath user-data volumes**: section 1 of the
  webhook already handles those. The whole-rootfs enumeration could
  in principle duplicate-mount the same path (e.g., source had
  hostPath at `/data` AND `/data` is in the captured rootfs upperdir
  too). De-dupe via the existing `customerMountPaths` set in mutate.go.
