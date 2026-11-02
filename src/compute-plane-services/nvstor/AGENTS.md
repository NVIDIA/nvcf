# Working on nvstor

Rules for anyone changing this tree, people and coding agents alike. They
exist because this directory is published as open source.

## What may be published

- **No measured results.** Documents, comments, help text and test names
  carry no throughput, latency, run duration, speedup or other figure from
  a run on any hardware, and do not name the hardware a test ran on.
  State what a measurement showed qualitatively ("a copy is bounded by the
  destination's disk") and give the evidence level ("validated on RDMA
  hardware", "exercised with a real kernel client", "unit-tested only").
  Configuration values and protocol constants (ports, sizes, limits,
  timeouts) are not results.
- **No private references.** No internal hosts, trackers, ticket or merge
  request numbers, private registries or organisation ids, cluster or node
  names, personal paths or addresses, or names of people or teams. Describe
  a deployment by what it is ("nodes with several RoCE NICs"), never by
  where it ran. Cite only files that are in this tree.
- **Synthetic test data.** Addresses in tests and examples are synthetic:
  IPv6 from `fd00::/8` or `2001:db8::/32`, IPv4 from `192.0.2.0/24`,
  `198.51.100.0/24`, `203.0.113.0/24` or private ranges with simple host
  numbers, MACs with the locally administered prefix `02:00:00`.
- **License headers.** Every source file starts with the NVIDIA SPDX
  Apache-2.0 header (the full block, as in any `.rs` file here). Third-party
  files keep their own license header and are listed in `NOTICE`.
- **No binaries** except the rendered figures in `docs/figures/`.

`scripts/check-hygiene.py` enforces these mechanically; run it before every
commit. Do not work around a hit by moving the same text to another file.

## Dependencies

New crates must be under a license in `deny.toml` (`cargo deny check
licenses bans sources`). After any change to `Cargo.lock`, regenerate
`NOTICE` with `scripts/gen-notice.py` (`--check` verifies it).

## Engineering rules

- The fast path is the default: RDMA before TCP, io_uring before POSIX,
  parallel pipelines before a single thread, zero copy before a buffer copy.
- No `unwrap()` in production code: use `?`, `ok_or`, or
  `expect("invariant: <reason>")`.
- A feature is done when it compiles without warnings, has unit tests for
  its success and error paths, is exercised end to end by a real client
  (`mount -t nfs4`, `aws s3 cp`, a cross-node RDMA transfer), has its error
  paths tested, and is documented.
- Wire and on-disk formats are versioned. An incompatible change bumps the
  version so mismatched peers fail with a clear message.
- Documents describe the code as it is. When code changes behaviour, the
  documents that describe it change in the same commit.

## Checks

```bash
cargo fmt --all --check
cargo build --workspace
cargo test --workspace
cargo clippy -p nvstor-core -p nvstor-rdma -p nvstor-peer --all-targets -- -D warnings
cargo deny check licenses bans sources
scripts/gen-notice.py --check
scripts/check-hygiene.py
```

nvstor-peer, `nvstor-core` and `nvstor-rdma` also build with Bazel. A change
to their sources, dependencies or build script needs the Bazel build too
(from the NVIDIA/nvcf root):

```bash
bazel test //src/compute-plane-services/nvstor/...
```

A new source file is picked up by the `glob` in the crate's `BUILD.bazel`.
crate_universe (`nvstor_crates` in the root `MODULE.bazel`) resolves the
whole Cargo workspace and records a hash of every member's `Cargo.toml` and
of `Cargo.lock` in the root `MODULE.bazel.lock`: a change to any of them,
in any crate, needs the lock updated (a `bazel build` rewrites it) and
committed with the change. A binary that will be run or measured is built
with `--config=release`: the default, `fastbuild`, compiles Rust without
optimisation (the `nvstor-peer-image` target optimises its own binary).
rdma-core comes from the pinned packages in `bazel/rdma_core.bzl`; a change
of version updates every SHA-256 there. CI runs both builds: the `nvstor` row
of `.github/workflows/bazel.yml` and `.github/workflows/nvstor.yml`, which
also builds the image for both architectures.
