# Phase 5b: CRIU dump runs inside per-capture writer Job

Status: **Tracked here, not yet filed on GitHub** (gh CLI not authenticated when drafted).

## Goal

Single-write CRIU artifact creation, writing directly to the per-capture PVC. Eliminates the current "agent writes to local hostPath then promotes" two-pass.

## Today

Agent's `runCheckpoint` calls `a.criu.DumpRPC(ctx, opts)` which writes to `/var/lib/nvsnap/checkpoints/<id>` — the agent's hostPath, which on AWS is the EBS root volume (~190 MB/s baseline). Then phase-3 promotion copies the artifact into a per-capture PVC for cluster portability — second 90 GB write. Total wall-clock for a 90 GB NIM checkpoint: ~10 min on AWS.

## Proposed

Move the criu dump itself into the per-capture writer Job:

1. Agent does pre-dump prep (`cuda Lock+Checkpoint`, build externals, ExtMnt mappings). Same as today.
2. Agent calls `Backend.Put` with two sources:
   - `{Kind: rootfs, SrcPath: <helperDir>, DstSubpath: "helpers"}` — small files (mapped, /dev/shm, metadata).
   - `{Kind: criu, CRIUTargetPID, CRIUExternals, CRIURPCOptionsJSON}` — full CRIU dump RPC options serialized as JSON.
3. Writer Job (`nvsnap-agent capture-write`):
   - Copy helpers via `treecopy.Copier` (fast, MB-class).
   - Start `criu service --address /tmp/criu.sock` locally.
   - Connect with go-criu, send `DumpRequest` reconstructed from the JSON options.
   - criu writes `/dest/criu/<id>` directly on the per-capture PVC.
4. Agent does post-dump (`cuda Restore+Unlock`).

Effect: 90 GB write happens once, on the per-capture PVC, at gp3-maxed (1 GB/s) → ~90 seconds.

## Risks

- CRIU options struct has many fields; agent + Job must agree on shape. Mitigated by reusing the existing `criu.DumpRPCOptions` Go type, JSON-marshalled.
- `ExtMnt` mappings are built relative to source pod's mount namespace. Job has `hostPID + privileged + /host bind`, so existing mount-resolution code paths work unchanged.
- criu plugins (cuda-checkpoint) must load from the Job container's filesystem. Agent image already ships `/criu-bundle/plugins/` — same image powers the Job.
- criu service needs `/var/run/criu` writable + a few kernel features. Verify in Job's privileged context.

## Out of scope

Multi-GPU CRIU is upstream-blocked (libcudart wall, see MEMORY.md). Phase 5b only changes WHERE the dump lands; not what runs in it.

## Estimate

~2-3 hours focused work. Single PR. Touches `cmd/agent/capture_write.go` (CRIU branch), `internal/agent/checkpoint.go` (replace `DumpRPC` call with `Backend.Put`), `internal/checkpointstore/store.go` (extend `CaptureSource` with `CRIURPCOptionsJSON`).

## Phase 5a (interim)

See accompanying commit. Until 5b lands, the agent writes to local hostPath at default speed (no improvement); phase-3 promotion still ships the artifact to per-capture PVC for cluster portability.
