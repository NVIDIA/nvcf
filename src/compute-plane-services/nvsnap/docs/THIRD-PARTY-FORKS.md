# Third-Party Forks: Why We Patch What We Patch

NVSNAP depends on a stack of forked components, each carrying patches that
production checkpoint/restore of GPU workloads requires. This doc is the
reference for "what did you change in X and why?" — useful for security
review, upstream contribution discussions, and onboarding engineers who
hit a stale upstream and wonder why we maintain a fork.

For each fork: where it lives, the upstream we forked from, the
patches we carry, and what those patches enable in NVSNAP's
checkpoint or restore path.

## Summary at a glance

| Fork | Upstream | Why forked | Where it runs |
|---|---|---|---|
| `criu` | github.com/checkpoint-restore/criu @ criu-dev | NVIDIA character-device + io_uring + ghost-file fixes; CUDA-checkpoint integration | Agent (dump + restore) |
| `go-criu` | github.com/checkpoint-restore/go-criu | Pass new RPC fields (Stream, SkipMissingFds, Portable, SkipMntNs) to our criu fork | Agent + restore-entrypoint |
| `libzmq` | github.com/zeromq/libzmq | Detect CRIU restore via `/run/criu-restored` marker; rebuild epoll FD; signaler PID re-cache; convert ETERM to retry | vLLM / SGLang / any zmq-using workload (LD'd at runtime) |
| `libuv` | github.com/libuv/libuv | Detect CRIU restore in `uv__io_poll`; trigger `uv_loop_fork()`-equivalent re-arm | uvloop-based workloads (vLLM API server) |
| `uvloop` | github.com/MagicStack/uvloop | Bundle our patched libuv as vendor; CRIU marker check at runtime not import time | Python event-loop workloads (vLLM, SGLang) |
| `pyzmq` | github.com/zeromq/pyzmq | (No NVSNAP patches — vanilla, only kept as a fork to pin the build against our libzmq) | Python zmq users |

## Per-fork detail

### criu (`$HOME/personal/criu`, branch `criu-dev`)

**Upstream:** `https://github.com/checkpoint-restore/criu` ref `criu-dev`.
Tracking is by manual cherry-pick / rebase, not a long-running merge.

**Patches we carry** (commit prefixes are local; not yet upstreamed):

1. `082956b...d2f0c906f...082956b...df6749d5c` — **Generic character-device
   handling for NVIDIA**. Upstream's CRIU CUDA plugin only recognises a
   small set of nvidia device nodes. On modern multi-GPU nodes the
   driver creates per-GPU char devices with arbitrary major numbers
   (`/dev/nvidia0`, `/dev/nvidia-uvm`, `/dev/nvidia-uvm-tools`,
   `/dev/nvidia-caps/*`). Our patches detect them by major number rather
   than by device-name regex, save (major, minor) into
   `nvidia-files.img`, and `mknod` the missing nodes during restore.
   Without this, restore on a destination with a different GPU index
   fails with ENOENT on the nvidia char dev.
2. `7e854d171` — **io_uring VMA premap fix**. CRIU's stream restore
   path treats VMAs through a unified premap routine. io_uring rings
   appear as MAP_SHARED VMAs with a kernel-only fd (no on-disk file).
   Without this fix, premap rejects them with EINVAL because the fd
   is `-1`. The patch forces `MAP_ANONYMOUS` for VMAs flagged
   `VMA_AREA_IORING` when streaming with `pieok=false`. Required for
   any vLLM checkpoint (vLLM/uvloop uses io_uring extensively).
3. `0745b1717` — **EEXIST on shared ghost files**. When multiple
   processes share the same unlinked file (vLLM's worker/engine
   share `/dev/shm/sem.*`), CRIU's ghost-restore path calls `link`
   for each process and the second one races. Patch: tolerate
   EEXIST and treat as success.
4. `87d274ba7` — **`NVSNAP_SKIP_CUDA_CHECKPOINT` env var**. Lets us
   bypass `cuda-checkpoint` invocation in the CUDA plugin during
   testing or for the NvSnap CUDA-interposition path. Used by the
   `NVSNAP_CUDA_INTERCEPT=1` flow.
5. `acd7bdbf4` — **AIO + parallel memfd restore speedup** (merged
   from `dfeigin-nv`'s upstream PR). Restoring large checkpoints
   benefits from parallelising the page-image read/write; this brings
   ~2× restore throughput on multi-process dumps.
6. Various smaller fixes around extended attributes, mount-namespace
   compat-mode, and verbosity in error paths — see `git log
   upstream/criu-dev..HEAD` in the fork.

**How NVSNAP uses it:** the agent and the legacy restore-entrypoint
both shell out to `/criu-bundle/criu`, which is the build of this
fork. Patches above are required at both dump and restore time
(symmetric flag handling).

**Build:** `docker/phase2/` Dockerfile copies the source via
`git archive` (no submodule), see `scripts/build-agent.sh base`.

### go-criu (`$HOME/personal/go-criu`)

**Upstream:** `https://github.com/checkpoint-restore/go-criu` v8.

**Patches:**

- New RPC option fields wired through to the criu RPC: `Stream`
  (field 76, for compressed streaming), `SkipMissingFds`,
  `SkipMissingFiles`, `Portable`, `SkipMntNs`. Without these
  the Go side can't pass our criu fork's options.
- Module path adjusted to `github.com/balajinvda/go-criu/v8` so we can
  vendor it cleanly.

**How NVSNAP uses it:** legacy restore-entrypoint uses go-criu's
RPC mode for `notify` hooks (PostRestore writes
`/run/criu-restored` marker, PostResume sends SIGUSR2). Agent-driven
path doesn't currently use RPC notify; it shells out and writes the
marker via the C helper before exec — the result is equivalent.

### libzmq (`$HOME/personal/libzmq`)

**Upstream:** `https://github.com/zeromq/libzmq` v4.3.x.

**Patches** (commits `5b14d801` ← head):

1. **`/run/criu-restored` marker detection in `epoll.cpp::loop()`.**
   On every IO-thread loop iteration, `access("/run/criu-restored",
   F_OK)`. If present, `close(epoll_fd)` and recreate it,
   re-`EPOLL_CTL_ADD` every entry in `_entries`. This is what makes
   CRIU restore actually work for zmq-using workloads — the dumped
   epoll FD's kernel interest list is stale; without rebuild, every
   poll returns garbage events or errors.
2. **Signaler PID re-cache (`signaler.cpp::send`/`wait`).** zmq's
   internal signaler caches `getpid()` at startup to detect post-fork
   "wrong PID" cases. CRIU restore changes the process's PID
   (potentially); without our patch the signaler wrongly treats the
   restored process as a forked child and silently drops sends.
   Patch: when `pid != getpid()` AND `/run/criu-restored` exists,
   update the cached pid; otherwise behave as before (real fork).
3. **`thread.cpp` SIGUSR2-aware EINTR handling**. Comment in code:
   *"with EINTR, allowing the poller loop to detect /run/criu-restored"*.
   We send SIGUSR2 to wake IO threads from their epoll_wait so the
   loop iterates and the marker check fires.

**How NVSNAP uses it:** the patched libzmq is built as a
`libzmq-builder` image (`stg.nvcr.io/.../libzmq-builder:v4.3.6-criu-epoll-v12`)
and copied into the workload's `/usr/local/lib/libzmq.so.*` by the
init container + workload startup script. After CRIU restore, agent
writes `/run/criu-restored` (in helper, before CRIU resumes threads)
and sends SIGUSR2 to all restored threads — the patched libzmq's
loop sees the marker, rebuilds epoll, and the workload resumes
serving.

### libuv (`$HOME/personal/libuv`)

**Upstream:** `https://github.com/libuv/libuv`.

**Patches:**

1. **`fix: detect CRIU restore before epoll_pwait, not after`**
   (commit `ca03d0b`). Symmetric to our libzmq patch but for libuv's
   event loop in `uv__io_poll`. Checks `/run/criu-restored` at the
   top of the loop; rebuilds the epoll FD if found.

**How NVSNAP uses it:** built as `libuv-builder` image, copied into
the workload's `/usr/local/lib/libuv.so.1` by the init container.
uvloop (Python's libuv binding used by vLLM's API server) loads it
at runtime. Same marker mechanism as libzmq.

### uvloop (`$HOME/personal/uvloop`)

**Upstream:** `https://github.com/MagicStack/uvloop` v0.21+.

**Patches:**

1. **`feat: add CRIU checkpoint/restore support via uv_loop_fork()`**
   (commit `b9be977`). Adds Python-visible API for triggering libuv's
   loop-fork path from outside (via signal handler in
   libnvsnap_intercept).
2. **`fix: check CRIU marker at runtime, not import time`** (commit
   `49f52e0`). Earlier version cached the marker check at module
   import; meant a restored process never noticed the marker that
   appeared post-restore. Moved to per-call check.
3. **Vendor libuv submodule pointed at our patched libuv fork**.

**How NVSNAP uses it:** the `uvloop-builder` init container copies
the patched uvloop wheel into `/nvsnap-lib/site-packages/`, the
workload startup script installs over the stock uvloop in Python's
site-packages. vLLM's API server uses uvloop. After CRIU restore,
uvloop's first event-loop iteration sees the marker and triggers
`uv_loop_fork()`, which re-arms epoll + signal handlers + child
watchers in the new process.

### pyzmq (`$HOME/personal/pyzmq`)

**Upstream:** `https://github.com/zeromq/pyzmq`.

**Patches:** none today. We maintain the fork only because we pin
pyzmq's build against our patched `libzmq.so` for ABI compatibility
checking. The `pyzmq-builder` image was retired —
`scripts/build-agent.sh` no longer references it; pyzmq is now used
straight from the workload image and links our patched libzmq at
runtime.

### criu-image-streamer (no longer shipped)

The streamer binary is **no longer bundled** in the agent image — the
streaming compression path pre-loaded the entire decompressed
checkpoint into RAM before serving, which doesn't scale for >50 GB
workloads. The Go integration (`internal/streamer/`) is dormant and
slated for removal/redesign (GitHub #46).

## How the patches interact at runtime

Checkpoint flow:

1. Agent locks GPU via `cuda-checkpoint --action lock`.
2. Workload's `libnvsnap_intercept` quiesces io_uring + libuv + zmq
   state on SIGUSR1 from the agent.
3. CRIU dumps:
   - char devices via the nvidia generic-major patch (1).
   - shared ghost files tolerating EEXIST (3).
   - io_uring VMAs as MAP_ANONYMOUS in stream mode (2).
4. Agent saves the dump + the source pod's overlay upperdir mirror
   + metadata.json (incl. `StdoutPipeID`, `StderrPipeID`,
   `SourcePodIP` for restore).

Restore flow (legacy / agent-driven):

1. Agent (or restore-entrypoint) creates `/run/criu-restored` in
   the destination's mntns BEFORE CRIU resumes any thread.
2. CRIU restores the process tree:
   - char devices via mknod (1).
   - shared ghost files (3).
   - GPU plugin runs `cuda-checkpoint --action restore` inline.
3. CRIU resumes ("Tasks resumed" line in restore.log).
4. **First** iteration of every libzmq epoll loop, every libuv
   io_poll, every uvloop tick: marker check fires, the patched
   library rebuilds its epoll FD + signaler. Workload IO is alive.
5. Agent (or restore-entrypoint) sends SIGUSR2 to every thread of
   every descendant of the restored PID, with ptrace-unblock for
   threads that mask all signals (libzmq IO threads). This forces
   loops that haven't iterated yet to wake.

If any one of these patches is missing, you get a specific symptom:
- Without the libzmq epoll rebuild: `zmq.error.ZMQError: No such
  file or directory` in `process_input_sockets` on first request.
- Without the libuv equivalent: API server hangs on first HTTP
  request (uvloop blocked in `epoll_pwait` on stale FD).
- Without the libzmq signaler PID re-cache: zmq sends are silently
  dropped after restore (the most insidious failure).
- Without the criu nvidia-generic patch: restore on a different GPU
  index fails with ENOENT on `/dev/nvidia<N>`.
- Without the io_uring premap patch: streaming restore fails at
  page-pull time with EINVAL.

## Build provenance

All forks are checked into `$HOME/personal/<repo>` on the
build host. They're not git submodules of `gpucr`; the build
pipeline copies them in via `git archive` from
`scripts/versions.sh::NVSNAP_CRIU_SRC` etc. This avoids submodule
churn but means the dev box's checked-out commit is the source of
truth for each release. Every `build-agent.sh app` push captures
the current commit hash of each fork in the resulting image's
metadata (`/criu-bundle/forks.txt` — TODO if not already there).

## Upstream contribution status

None of these patches are upstreamed today. Concrete blockers:

- **CRIU**: NVIDIA-specific generic char-device handling is the
  kind of patch the upstream maintainers would accept, but it
  needs separating from the various smaller fixes that are
  NVSNAP-specific. PR-shaped work, ~1 week.
- **libzmq / libuv / uvloop**: the marker file path
  `/run/criu-restored` is NVSNAP-specific. Upstreamable form would
  expose a callback/env-var so users can configure the marker.
  Conversation with maintainers needed.
- **go-criu**: trivial to upstream once our CRIU fork's RPC fields
  are upstreamed.

For now, treat the forks as long-lived. When upstream picks up
fixes, rebase our remaining patches.
