# Cross-Pod Restore: Filesystem State Transfer

**Status:** design — not implemented
**Branch:** `feat/crio-support`
**Author trail:** see commits `cd6186f`, `3df8c9e` for the agent-driven mntns
plumbing this design completes.

## Problem

CRIU dumps file paths and sizes for every file-backed VMA and open regular
file fd. At restore, CRIU stat()s those paths in the restored process's
mntns and refuses to map if the file is missing or its size differs.

Today's restore architecture (agent-driven, unprivileged placeholder) puts
the restored process in a placeholder pod that runs the same image as the
source but is otherwise fresh — no init scripts have run, nothing has been
written, no caches warmed. Any file the source container created at runtime
fails the CRIU stat check because it doesn't exist on the placeholder.

Concrete failures we've hit on `feat/crio-support` (vLLM small, OCI CRI-O):
- `Can't open file usr/bin/python3.12` (was actually a different bug — fixed
  by the `nvsnap-restore-helper` C binary; included for completeness because
  it surfaced the same architectural gap initially).
- `Can't open file root/.cache/flashinfer/0.5.2/90a/flashinfer_jit.log` —
  vLLM creates this at startup; placeholder hasn't run vLLM.
- `Can't bind id 0xfc … addr /var/run/vllm/<uuid>` — vLLM `mkdir -p
  /var/run/vllm` at startup; the dir doesn't exist on the placeholder.
- `File usr/local/lib/python3.12/dist-packages/uvloop/loop.cpython-312-…so
  has bad size 15952168 (expect 11322392)` — source pod copied the
  patched uvloop in over the stock one at startup; placeholder still has
  the stock one.

These are not vLLM-specific. Any non-trivial workload writes to its
container's writable layer at runtime: configs from env, JIT artefacts,
unix sockets, log files, framework caches, init-script output. Any of
those can be the file backing a VMA or fd at checkpoint time.

The ad-hoc fixes in `internal/agent/restore.go::ensurePlaceholderRuntimeFiles`
(touch flashinfer log, mkdir `/var/run/vllm`) violate the project's
runtime-agnostic / application-transparent principles in CLAUDE.md and
must be removed.

## Constraint we can rely on

With kernel overlayfs (every K8s GPU runtime we care about uses it):
every file CRIU records by path lives in **exactly one** of two places:

1. **A lower layer** — read-only, comes from the container image. Identical
   bytes between source pod and any pod using the same image (= the
   placeholder, by design).
2. **The upper diff layer** — the container's writable overlay. Everything
   the source container wrote at runtime: init-script output, runtime
   caches, IPC sockets, log files, JIT artefacts, downloaded models,
   anything.

The lower layer is taken care of automatically by deploying the placeholder
with the same image. **The entire surface area we have to deal with is the
upperdir.**

Both layers are visible in `/proc/<pid>/mountinfo` for the source container:

```
17554 14398 0:1070 / / rw,nodev,relatime - overlay overlay rw,
  lowerdir=…/l/SIUEV4…:…/l/WGAFZV…:…,
  upperdir=/var/lib/containers/storage/overlay/132e4d9f…/diff,
  workdir=/var/lib/containers/storage/overlay/132e4d9f…/work,…
```

`upperdir` is an absolute host path. Both the agent (with
`/var/lib/containers/storage` hostPath-mounted) and the source container
runtime can read/write it.

## Proposal: upperdir mirror

**At checkpoint time**, alongside the CRIU dump, persist the source
container's overlay upperdir as part of the checkpoint artefact:

```
/var/lib/nvsnap/checkpoints/<id>/
  ├── *.img             # CRIU dump
  ├── pages-*.img
  ├── metadata.json
  └── rootfs-diff/      # NEW: copy of source upperdir
      ├── usr/local/lib/python3.12/dist-packages/uvloop/loop.cpython-…so
      ├── root/.cache/flashinfer/0.5.2/90a/flashinfer_jit.log
      ├── var/run/vllm/                          # empty dir, mode preserved
      └── …
```

**At restore time**, before invoking CRIU, the agent rsync's
`rootfs-diff/` into the placeholder's overlay upperdir. After the rsync,
the placeholder's filesystem is byte-identical to the source's at dump
time. CRIU's existing path-based stat checks pass; no special handling
needed for any file class.

This is one mechanism that handles every failure class above
(missing-file, missing-dir, size-mismatch) uniformly, for any workload,
any image, any framework.

## Why mirror over CRIU patch (alternative B)

The narrower alternative is to teach our CRIU fork to dump upperdir-resident
file paths as ghost (content embedded in the dump) instead of recording
by path. The ghost code path already exists for unlinked files; we'd
generalise the predicate to "in upperdir". I considered this and
rejected it for now:

- **Doesn't cover empty directories.** The `/var/run/vllm` parent-dir
  failure is for a directory, not a file. CRIU's regular-file ghost
  path doesn't help. Unix-socket bind needs the parent dir to exist.
  We'd need parallel logic to dump and recreate empty upperdir dirs,
  duplicating filesystem traversal that rsync already does.
- **Ghost limits.** CRIU has a default 1 MB per-file ghost limit; lifting
  it for legitimately large upperdir files (downloaded models, large logs)
  bloats the dump and complicates streaming.
- **Couples C/R to the CRIU fork.** Customers' restore-side correctness
  depends on a non-upstream CRIU patch. Filesystem mirror is plain
  userspace.
- **Doesn't reuse.** A correct CRIU patch needs to reimplement
  hardlink/sparse-file handling that rsync already has.

Mirror is more bytes on disk for the checkpoint artefact, but those bytes
are actually needed at the destination — there's no way to restore a
process that opens `/root/.cache/flashinfer/.../jit.log` without that file
appearing on the destination filesystem somehow.

## Implementation

Three pieces. All in the agent (Go); no CRIU changes; no customer pod
changes.

### 1. Identify the upperdir at dump time

In `internal/agent/checkpoint_plan_a.go` (alongside `buildDumpExtMnt`):

```go
// resolveUpperdir parses /proc/<pid>/mountinfo, finds the entry whose
// mountpoint is "/" and fstype is "overlay", and returns the upperdir
// absolute path from the super options.
func resolveUpperdir(pid int) (string, error) { … }
```

This already exists in spirit at `internal/runtime/crio/client.go`
(commit `e4356a3`); pull the parser out into a shared helper under
`internal/criu/mountinfo/` (or extend the existing one).

### 2. Mirror upperdir into the checkpoint at dump time

In `internal/agent/checkpoint.go::Checkpoint`, after the CRIU dump
succeeds:

```go
upper, err := resolveUpperdir(int(containerInfo.PID))
if err == nil && upper != "" {
    dst := filepath.Join(checkpointDir, "rootfs-diff")
    if err := mirrorUpperdir(upper, dst); err != nil { … }
}
```

`mirrorUpperdir` is a single rsync invocation:

```
rsync -aHAX --numeric-ids --sparse --delete <upper>/ <dst>/
```

Flags:
- `-a` — archive (preserves perms, ownership, timestamps, symlinks).
- `-H` — preserve hardlinks (overlay upperdir can have them).
- `-A -X` — ACLs and xattrs (xattrs matter for overlayfs whiteouts; see
  edge cases below).
- `--numeric-ids` — uid/gid stay as numbers (different image's
  /etc/passwd at restore must not change ownership).
- `--sparse` — large sparse files (model caches sometimes are).
- `--delete` — irrelevant for first checkpoint, idempotent on
  re-checkpoint.

Record `rootfs-diff` size in `metadata.json` for accounting.

### 3. Apply mirror into placeholder's upperdir at restore time

In `internal/agent/restore.go::Restore`, after locating the placeholder
container and **before** calling `criu.Restore`:

```go
plUpper, err := resolveUpperdir(int(placeholderInfo.PID))
if err != nil { return … }
src := filepath.Join(checkpointDir, "rootfs-diff")
if err := mirrorUpperdir(src, plUpper); err != nil { … }
```

Same rsync invocation, reversed direction. The placeholder is frozen
in `sleep infinity` and isn't writing to its overlay; the rsync can't
race anything.

After rsync:
- Patched uvloop is at the right path with the right size.
- `/root/.cache/flashinfer/.../jit.log` exists.
- `/var/run/vllm/` exists.
- Whatever other surprise files the workload creates also exist.

CRIU restore then proceeds as it does today — the stat checks pass
because the files actually exist.

## Edge cases

- **Overlayfs whiteouts.** Upperdir uses character devices (mode 0,
  rdev 0) to mark files deleted in lower layers. rsync with `-X`
  preserves the trusted.overlay xattrs that mark them. The placeholder's
  upperdir applying these whiteouts hides the same lower-layer files
  the source had hidden. Correct behaviour.
- **Hardlinks split across layers.** If source has a hardlink in
  upperdir that points (via overlay copy-up) to data also referenced
  in a lower layer, rsync preserves the upperdir-side hardlink graph.
  CRIU records by path, so the path resolves correctly post-rsync.
- **Sparse files.** Use `--sparse`. Model caches often are.
- **Size cap.** Real deployments must cap dump size; an integration
  point but not a correctness issue. For workloads that put GB-class
  artefacts in upperdir (e.g. HF hub model downloads), recommend a
  PVC-mounted cache so the model stays in a lower-layer-equivalent
  shared mount, out of upperdir. Document.
- **Restore on a different node.** rsync source path is in the
  checkpoint directory, which is hostPath-mounted on every agent
  daemonset pod (same node-local path on every node). For cross-node
  restore (future), the checkpoint store gains an object-storage
  layer; the restore agent fetches the diff alongside the CRIU images.
- **Re-checkpoint of the same pod.** `--delete` on the dump-side rsync
  keeps `rootfs-diff/` consistent. If the workload deletes a runtime
  file between checkpoints, the next dump's mirror reflects that.
- **Unix sockets, fifos, special files.** rsync handles these with
  `-a`. CRIU's socket/fifo restore is a separate path that doesn't
  rely on the path's pre-existence — it just needs the parent dir,
  which the mirror provides.

## What this lets us delete

After this lands:
- `internal/agent/restore.go::ensurePlaceholderRuntimeFiles` —
  delete entirely. It hardcodes `/root/.cache/flashinfer/...` and
  `/var/run/vllm`. The mirror covers both.
- The placeholder pod stays `command: ["/bin/sleep", "infinity"]`,
  unprivileged, no init containers (other than what's already there
  for the bundle copy-out).
- No nsenter `cp /nvsnap-lib/... $SITE` from the agent.

## What stays

- `nvsnap-restore-helper` (`open_tree` + `setns` + `move_mount` +
  exec criu) — generic mntns plumbing, unrelated to filesystem
  state. Keep.
- `addSourcePodIPToPlaceholderLo` — network identity, not filesystem.
  Keep.
- `shouldSkipMount` skip list — runtime/CDI mount machinery, not
  workload code. Keep.
- `--tcp-established` flag — generic CRIU symmetric flag.
  Keep.

## Test plan

1. **Unit:** mountinfo parser returns correct upperdir for fixture
   inputs (overlay, fuse-overlayfs reject, no-overlay reject).
2. **Local:** rsync round-trip on a synthetic upperdir tree
   (whiteouts, hardlinks, sparse files, sockets) preserves bytes.
3. **e2e on OCI CRI-O H100, vllm-small:**
   - delete + recreate vllm-small + checkpoint.
   - inspect `<id>/rootfs-diff/` contents.
   - apply placeholder, restore, verify inference (`curl -X POST
     /v1/completions ...`).
4. **e2e on GCP containerd, vllm-small:** regression. Ensures the
   same agent code path works under containerd's overlay layout.
5. **e2e: workload we haven't tested before** (e.g. SGLang) without
   adding any framework-specific code.

## Rollback path

If the mirror approach proves insufficient (e.g. some file class CRIU
records that doesn't live in upperdir at all — none known today, but
unknown unknowns exist), the alternative is the CRIU-fork ghost
predicate. The mirror is plain userspace and removable; replacing it
with the CRIU patch is a strictly contained change in
`internal/criu/manager.go`.

## Out of scope here

- Compression of `rootfs-diff/` — handled by the existing
  `NVSNAP_STREAM_CHECKPOINT` plumbing (today disabled by default).
- GC of orphaned `rootfs-diff/` directories — covered by retention
  policies (`internal/server/retention.go`).
- Cross-node `rootfs-diff/` distribution — covered by the future
  S3/GCS checkpoint store (#18).
