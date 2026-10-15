# Phase 5c: EROFS Compaction for Per-Capture PVCs

**Status:** design — not yet implemented. Supersedes the writer-Job-runs-CRIU
approach attempted in Phase 5b (v0.18.0–v0.18.5) which regressed on
`vllm-small` post-restore inference for reasons we couldn't isolate
without a longer debugging cycle.

**Related docs:**
- `docs/PHASE5B-CRIU-IN-WRITER-JOB.md` — the design we attempted; phase 5c keeps
  the per-capture PVC + PV-flip plumbing it landed but reverts the
  writer-Job-runs-CRIU half.
- `docs/UNIFIED-CAPTURE-STORAGE-DESIGN.md` — overall storage tier framing.
- `docs/MULTI-GPU-CUDA-RESTORE.md` — orthogonal multi-GPU GPU-state work.

## Goal

Get the demo-time win we wanted from phase 5b — single fast write to the
per-capture PVC — without regressing post-restore behavior. We've
empirically observed that **changing the dumping process context
changes restore behavior in ways we can't yet isolate** (writer Job
mount NS, longer frozen window during PV-flip). Phase 5c keeps the
known-good legacy v0.17.13 dump path and adds a compaction layer that
both shrinks the artifact AND makes the second write fast.

```
Phase 5c flow:

1. Agent runs CRIU dump → local hostPath          (the proven v0.17.13 path)
2. Agent unfreezes source pod IMMEDIATELY         (frozen window stays ~150s)
3. Background: writer Job builds EROFS from hostPath → local tmpfs
4. Background: writer Job copies EROFS image (3-5× smaller) → per-capture PVC
5. PV-flip: writer PVC → reader PVC (RWO → ROX)
6. Restore-side: pod mounts reader PVC, kernel-mounts the EROFS file at /checkpoints
7. CRIU restore reads from EROFS-RO directly
```

## Why this beats Phase 5b

| Property                         | Legacy v0.17.13 | Phase 5b (writer-Job-CRIU) | Phase 5c (this design) |
|----------------------------------|------------------|------------------------------|--------------------------|
| Source-pod frozen window         | ~150 s           | ~250 s                       | ~150 s                   |
| Total time to portable artifact  | ~12 min          | ~5 min                       | ~6 min                   |
| Restore behavior matches main    | yes (baseline)   | **no** (regression)          | yes (same dump bytes)    |
| Implementation complexity        | baseline         | high (CRIU options serialize) | medium (mkfs.erofs + treecopy) |
| Storage cost at rest             | full size        | full size                    | **3-5× smaller** (compressed) |
| Cross-cluster transfer cost      | full size        | full size                    | **3-5× smaller**         |

Phase 5c dominates phase 5b on three axes (proven restore, simpler code,
smaller storage) and ties on speed.

## Open risk: CRIU-restore-from-EROFS

**The one untested assumption** of this design is that CRIU restore
works correctly when ImagesDir is a read-only EROFS mount. CRIU restore
reads images via `read()`, `mmap(MAP_PRIVATE)`, and `splice()` — all
standard VFS operations that EROFS supports. But we haven't validated
empirically.

### Specific concerns (in order of severity)

1. **`O_DIRECT` on `pages-*.img`.** Our v0.18.5 restore log showed
   `O_DIRECT enabled on pages fd 5`. EROFS doesn't support O_DIRECT
   (compressed extents aren't block-aligned). CRIU should fall back to
   buffered I/O on `EINVAL`, but verify it doesn't hard-fail.
2. **`fstatfs()` filesystem-type checks.** CRIU has hardcoded
   special-cases for tmpfs/btrfs/overlay. An unknown FS magic could
   trigger an error path. We'd need to grep CRIU for `f_type ==`
   patterns and confirm none reject EROFS.
3. **Compression ratio for CRIU dumps.** EROFS-LZ4 compresses
   text/headers well but raw process memory pages (`pages-*.img`) are
   high-entropy. Realistic compression: 1.8-2.5×, not the 3-5×
   benchmark cited for filesystem trees.

### Derisking experiment (30 min, must pass before implementation)

```bash
# 1. Get a known-good v0.17.13 CRIU dump on a dev node:
#    /var/lib/containerd/nvsnap-checkpoints/vllm-small__nvsnap-system__<id>/

# 2. Build an EROFS image from it:
mkfs.erofs -zlz4 -Enoinline_data \
  /tmp/test.erofs \
  /var/lib/containerd/nvsnap-checkpoints/vllm-small__nvsnap-system__<id>/

# 3. Loop-mount the EROFS image:
mkdir -p /tmp/erofs-test/<id>
mount -t erofs /tmp/test.erofs /tmp/erofs-test/<id>

# 4. Run CRIU restore with --images-dir /tmp/erofs-test/<id> + WorkDir=tmpfs
#    (or use restore-entrypoint with the /tmp/erofs-test path).

# 5. Observe restore succeeds + inference works.
```

**If pass:** commit to the design.
**If fail:** the failure mode tells us how to proceed:
- O_DIRECT issue → bind-mount pages-*.img separately on tmpfs as a copy.
- fstatfs rejection → patch CRIU to whitelist EROFS magic (`0xE0F5E1E2`).
- Other → fall back to **zstd-tarball** as compaction format (CRIU restore-from-extracted-tar always works since it produces a normal directory).

## Implementation plan

### Code changes

**Revert from v0.18.5:**
- `internal/agent/checkpoint.go` — remove `runPhase5bDump`, `stagePhase5bMetadata`, `phase5bMetadataInputs`, `phase5bDumpResult`. Restore the legacy `a.criu.DumpRPC(ctx, rpcOpts)` direct call. Keep `promoteCheckpointToBackend` as the post-dump promote path.
- `internal/checkpointstore/store.go` — remove `CRIURPCOptionsJSON` from `CaptureSource` (no longer needed since CRIU runs in agent).
- `cmd/agent/capture_write.go` — remove the `Kind=criu` branch from `runCRIUDump`. The writer Job goes back to being purely a rootfs/erofs-builder.
- `internal/criu/rpc_dump_json_test.go` — delete (no longer needed).
- `internal/agent/checkpoint_phase5b_test.go` — delete or rewrite for phase 5c.
- `cmd/agent/capture_write_test.go` — keep, simplify.
- `internal/checkpointstore/gpdrox/gpdrox_test.go` — drop `TestPut_RejectsCRIUWithoutRPCOptions`, keep the rest.

**Add for 5c:**
- `cmd/agent/capture_write.go` — new `Kind=erofs` source. Reads from `SrcPath` (host hostPath dir), runs `mkfs.erofs -zlz4 -Enoinline_data` to produce a compact `.erofs` image at `<DstSubpath>/checkpoint.erofs` on the PVC.
- `docker/agent/Dockerfile` — `apt install -y erofs-utils` (or equivalent for our base image). Verify `mkfs.erofs --version` works in the image.
- `internal/agent/checkpoint.go` — at promote time, build the source list with one `Kind=erofs` source pointing at the local hostPath checkpoint dir. The legacy `Kind=rootfs` user-data sources stay as-is.
- `scripts/test-e2e.sh` — restore manifest patch: instead of mounting the PVC at `/checkpoints` with subPath, mount the PVC at `/erofs-volume` and add an init container that loop-mounts `/erofs-volume/<id>/checkpoint.erofs` at `/checkpoints/<id>`. Loop-mount needs `privileged: true` (already on the restore container).

### New CaptureSource shape

```go
const SourceKindEROFS SourceKind = "erofs"

// CaptureSource extension:
//   Kind=erofs:
//     - SrcPath: absolute host path to a directory tree (the local CRIU dump dir)
//     - DstSubpath: relative path within the capture tree where the
//       resulting `.erofs` file should land (e.g., "criu/<id>")
//     - Excludes: relative paths within SrcPath to skip
```

Writer Job's `runCaptureWrite` dispatches `Kind=erofs` → `mkfs.erofs <srcdir> -o <fullDst>/checkpoint.erofs`.

### Restore-side mount

Two options for restore-pod mount:

**Option A: init container loop-mounts EROFS** (preferred)
```yaml
initContainers:
  - name: erofs-mount
    image: <agent-image-with-erofs-utils>
    securityContext:
      privileged: true   # needed for mount(2)
    command: ["sh","-c","mount -t erofs /erofs-pvc/criu/<id>/checkpoint.erofs /checkpoints"]
    volumeMounts:
      - name: erofs-pvc
        mountPath: /erofs-pvc
        readOnly: true
      - name: checkpoints
        mountPath: /checkpoints
        mountPropagation: Bidirectional   # so the main container sees the mount
```

**Option B: stratumd-style** — full daemonset that manages erofs mounts. Heavier, only worth it if we want cross-cluster fan-out via stratum.

Start with Option A.

### Frozen-window optimization

The proven legacy path freezes the source pod for the duration of the
`a.criu.DumpRPC()` call (~150s). Phase 5c keeps this frozen window
unchanged but moves the slow PVC write OFF the critical path:

```
T=0      agent: cuda Lock + Checkpoint + CRIU dump → local hostPath
T=150s   agent: cuda Restore + Unlock + SIGUSR2  ← source pod RESUMES
T=150s   agent: kicks off Backend.Put with Kind=erofs source
T=150s+  background: writer Job runs mkfs.erofs (~20s for 28GB)
T=170s+  background: writer Job copies ~10GB EROFS to PVC (~10s)
T=180s+  background: PV-flip (~180s)
T=360s   reader PVC available for cross-node restore
```

Total time to portable artifact: ~6 min. Source-pod frozen window:
~150s. Audience perception: source pod stops responding for ~2.5
min, then resumes. Cross-node restore available a few minutes later.

For the demo, the user can either:
- Show "checkpoint" as the 2.5 min freeze (matching legacy timing).
- Block on PV-flip if they want to demo cross-node restore in the same
  click — that's ~6 min total.
- Fire-and-forget: trigger checkpoint, do other stuff for 5 min, then
  trigger restore on another node.

### Storage savings

For vllm-small (28 GB): expect ~12-15 GB EROFS (1.8-2.4× compression).
Per-capture PVC sizes drop from `dump_size + 20%` to `compressed_size +
10%`. Over 100 retained captures = significant cost savings on EBS.

For sglang-small (78 GB): expect ~35-50 GB EROFS.

### CHANGELOG / migration

Existing v0.17.x captures on local hostPath remain readable (legacy
restore path). New captures land in EROFS format on PVC. No migration
needed — old captures expire via existing retention policy.

## Validation gates (in order)

1. **Derisking experiment** (manual, 30 min): CRIU restore from EROFS works on vllm-small.
2. **Single-GPU e2e suite**: `vllm-small`, `sglang-small`, `trtllm-small`, `nim-llama-8b` all pass `test-e2e.sh` end-to-end (including post-restore inference).
3. **Storage savings measured**: artifact-on-PVC size is ≤60% of CRIU dump size for at least 3 of 4 workloads.
4. **No regression on Friday's main-branch baseline**: legacy v0.17.13 vllm-small still passes (we don't break the existing path during phase 5c rollout).
5. **Cross-node restore**: capture on node A, restore on node B (different host entirely). Tests the cluster-portability claim.

Phase 5c is shippable when all 5 pass. None of them require multi-GPU
work — that stays in `docs/MULTI-GPU-CUDA-RESTORE.md` as a separate track.

## What we keep from the phase 5b work

Even though we revert the writer-Job-runs-CRIU half, these phase 5b
contributions stay:

- `internal/checkpointstore/gpdrox/` — per-capture PVC + PV-flip + auto-detect CSI access modes. Phase 5c's writer Job uses the same plumbing.
- `internal/agent/checkpoint.go::checkpointHostPath` — the in-container → host path translator. Phase 5c needs this for the EROFS source's SrcPath translation.
- `scripts/test-e2e.sh` — the restore-manifest patching logic. Phase 5c reuses the structure, just changes what the volume looks like (PVC mount + erofs init container vs PVC subPath).
- `docs/MULTI-GPU-CUDA-RESTORE.md` — orthogonal future work, unchanged.
- `scripts/sync-versions.sh` — broadened to cover internal/server/manifests too. Keep.

## Out of scope

- Multi-GPU restore (`docs/MULTI-GPU-CUDA-RESTORE.md`).
- Cross-cluster artifact transfer (S3/GCS export of EROFS images — issue #18, separate feature).
- Compaction algorithm tuning (zstd-vs-LZ4 trade-offs — measure with real workloads first).
- Stratum-style daemon management of EROFS mounts (Option B above).
