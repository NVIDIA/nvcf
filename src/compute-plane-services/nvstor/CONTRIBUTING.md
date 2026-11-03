# Contributing to nvstor

nvstor is Apache-2.0 licensed. Contributions (bug reports, patches,
documentation, conformance runs) are welcome.

nvstor is developed in the NVIDIA/nvcf repository
(<https://github.com/NVIDIA/nvcf>) under `src/compute-plane-services/nvstor`.
Issues and pull requests go there.

## Before you start

- Read the [README](README.md) for an overview and
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for how the system works.
- Search the [open issues](https://github.com/NVIDIA/nvcf/issues) before
  filing a new one.

## Reporting bugs

Open an issue in NVIDIA/nvcf. Include:

- the nvstor commit hash,
- OS and kernel (`uname -a`),
- exact steps to reproduce,
- what you expected and what happened,
- server logs (`RUST_LOG=nvstor=debug`) and, for a protocol bug, a packet
  capture (`tcpdump -i lo -w /tmp/bug.pcap "port <port>"`).

## Security issues

Do not report security issues in public issues or pull requests. Report them
as described in [SECURITY.md](SECURITY.md).

## Pull requests

1. **Fork and open a pull request** against `main` of NVIDIA/nvcf.
2. **Reference a public issue** in the pull request description. Open one
   first if none exists.
3. **Title the pull request with a Conventional Commit**, scoped to nvstor,
   for example `feat(nvstor): ...` or `fix(nvstor): ...`.
4. **Sign off every commit** (`git commit -s`), certifying the
   [Developer Certificate of Origin](https://developercertificate.org/).
   Every commit needs a `Signed-off-by:` line.
5. **Keep it small and focused.** One feature or one fix per pull request;
   rebase on `main` to stay current.
6. **Pass `cargo test --workspace`** before pushing, and for a change to
   nvstor-peer, `nvstor-core` or `nvstor-rdma`, also
   `bazel test //src/compute-plane-services/nvstor/...` from the repository
   root (see the README's Bazel section).
7. **No warnings.** `RUSTFLAGS="-D warnings" cargo build --workspace
   --all-targets` must be clean in the files you touch.
8. **No `unwrap()` in production code.** Use `?`, `.ok_or(...)`,
   `.unwrap_or(...)`, or `.expect("invariant: <reason>")`.
9. **Add tests.** Production code changes need unit tests for the new
   behaviour, happy path and error path, and where feasible an integration
   test that drives the same path with a real client (`mount -t nfs4`,
   `aws s3 cp`, `nbd-client`, `fio`). RDMA changes should be run on RDMA
   hardware; tests that need a device are marked `#[ignore]` and run with
   `-- --ignored`.
10. **Update the documentation** (README, CLI help, `docs/`) when behaviour
    changes.
11. **Commit messages explain why**, not only what.

## Development setup

```bash
git clone https://github.com/NVIDIA/nvcf.git
cd nvcf/src/compute-plane-services/nvstor
cargo build
cargo test --workspace
```

Rust 1.87 or newer, on Linux. The build links rdma-core (`libibverbs`,
`librdmacm`); see [`docs/RDMA_SETUP.md`](docs/RDMA_SETUP.md) for the
packages, and set `RDMA_CORE_PREFIX` if they are installed in a custom
prefix. A recent kernel is recommended for io_uring and the NFS client.
`nvstor-metadata` builds RocksDB, so the first build takes a few minutes.

## Code style

- `cargo fmt` with the default rustfmt configuration.
- Small functions.
- Comments explain why, not what.
- Error types per crate in `src/error.rs`; convert at crate boundaries.

## Licensing

By contributing you agree that your contribution is licensed under the
Apache License 2.0, the license of the project. See [LICENSE](LICENSE).

## Code of conduct

Be respectful. Disagree on technical merits, not with the person.
Harassment, discrimination and personal attacks are not tolerated.
