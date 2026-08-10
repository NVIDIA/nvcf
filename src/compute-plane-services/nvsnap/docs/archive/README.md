# `docs/archive/` — historical / superseded documents

This directory holds the markdown files that were checked into `docs/`
before the OSS-readiness pass on 2026-05-12 and the later 2026-06-21
post-rename cleanup. They are kept here (rather than deleted) so anyone
trying to understand the project's history can read them. **Treat them
as time-capsule snapshots, not current docs.**

Start with **[`HISTORY.md`](HISTORY.md)** — a narrative digest of the
design history (why paths were taken or abandoned, spike outcomes, where
the library forks came from) that indexes everything else here.

Anything still relevant has been folded into the canonical set at
`docs/` (entry point: `docs/architecture/NVSNAP-ARCHITECTURE.md`). If you
find yourself needing one of these files in current code, lift its
content into the appropriate canonical doc and update the canonical doc
in the same PR.

## Categories

- **Session journals** (`SESSION-STATUS-*.md`, `SESSION-SUMMARY-*.md`,
  `task[34]-summary.md`, `phase2-summary.md`) — daily progress notes
  from the build-out. Useful for tracing how a fix was found; git log
  + commit messages are the structured equivalent.

- **Status snapshots** (`STATUS.md`, `CHECKPOINT-RESTORE-STATUS.md`,
  `AGENT-DRIVEN-RESTORE-STATUS.md`, `BREAKTHROUGH-20260205.md`,
  `ARCHITECTURE-REVIEW-20260209.md`, `MILESTONES.md`, `ROADMAP.md`)
  — point-in-time summaries. GitHub issues and the `BENCHMARK.md`
  table are the live equivalents.

- **Work plans** (`*-PLAN.md`, `VERIFICATION-PLAN`, `VANILLA-CRIU-TEST-PLAN`,
  `REFACTORING.md`) — planning docs for completed work. The work itself
  is in the code.

- **Failed approaches** — Phase 5b (`PHASE5B-*`), Phase 5c
  (`PHASE5C-EROFS-COMPACTION`), `TRANSPARENT-COMPRESSION-DESIGN`,
  `LAZY-PAGES-SPIKE`, `UNIFIED-CAPTURE-STORAGE-DESIGN`. Useful to
  understand *why* we didn't go those directions; the active path
  is documented in `docs/MULTI-GPU-ROOTFS-FANOUT-DESIGN.md` and
  `docs/GENERIC-PYTHON-INJECTION-DESIGN.md`.

- **Bug post-mortems** — io_uring (`IO_URING_*`), CRIU patches
  (`CRIU_IO_URING_*`, `CRIU-FORK-CHANGES`, `CRIU-RESTORE-FIX-PLAN`),
  ZMQ restore (`ZMQ-RESTORE-FIX`, `zmq-*-api`, `zmq-state-mapping`).
  The fixes are in the code; commit messages tell the story.

- **Earlier / parallel architectures** — `PRODUCTION-ARCHITECTURE`,
  `DUAL-ARCHITECTURE`, `CONTROLLER-DESIGN`, `GPUCR-MASTER-DESIGN`,
  `GPU-HYPERVISOR-VISION`, `GPU-RESTORE-ARCHITECTURE`,
  `GPU-CHECKPOINT-RESTORE`, `DEMO-ARCHITECTURE`,
  `NVSNAP-CHANGES-FOR-RESTORE`, `NVSNAP-GPU-CHANGES-NEEDED`,
  `CROSS-POD-MOUNT-REPLAY-DESIGN`, `CROSS-POD-RESTORE-DESIGN`,
  `CUDA-INTERPOSITION-DESIGN`, `MULTI-GPU-CUDA-RESTORE`,
  `MULTI-GPU-PLAN`, `PYTHON-COMPATIBILITY`, `RENAME-WRAPCORE-TO-NVSNAP-DESIGN`.
  Mostly superseded by the canonical SDD (`docs/ARCHITECTURE.md`).

- **NCCL design** — `NCCL-CHECKPOINT.md`, `NCCL-MULTI-GPU-CHECKLIST.md`,
  `NCCL-QUIESCE-DESIGN.md`. Multi-GPU NCCL is still active design
  space; canonical NCCL coverage is in
  `docs/architecture/05-MULTI-PROCESS.md` and the `quiesce.c`
  source. These three are kept for the historical reasoning.

- **Phase 5d notes** — `PHASE5D-PEER-FANOUT-BLOB-STORE.md`. The
  feature shipped; commit messages on `feat(phase5d.*)` are the
  authoritative reference.

- **`architecture/` deep dives now archived** — `00-INDEX.md` (stale
  index), `02-MILESTONES.md` (overlap with status snapshots),
  `architecture/ARCHITECTURE.md` (duplicate of top-level),
  `architecture/NETWORK-IDENTITY.md` (mostly subsumed by interception
  layer + cross-node restore notes).

## Re-promotion policy

If you need one of these documents to make current code understandable,
that's a signal: the canonical doc is missing context. Move the
relevant content forward into the appropriate canonical doc, update
that doc's commit history, and leave a redirect note in the archived
file pointing at the new home.
