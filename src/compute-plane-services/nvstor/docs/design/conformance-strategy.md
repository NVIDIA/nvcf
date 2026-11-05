# Conformance Strategy

How nvstor proves it speaks the protocols it claims to. Required reading
before adding a new client-facing protocol or claiming a feature is
"done."

This document exists because several protocol-correctness bugs were
found that unit tests never caught: server and test code agreed on the
same wrong opcode numbers, the same wrong wire layout, the same
hardcoded auth flavor list. Only running a real RFC-conformant client
(the Linux kernel NFSv4 client) found them. Off-the-shelf conformance
suites (`pynfs`, `s3-tests`) exist for exactly this purpose; we should be
running them.

All the front ends in the matrix below (NFS, S3 gateway, NBD, FUSE, CSI,
Raft) are **experimental**.

## Rule

Each protocol nvstor exposes MUST be covered by:

1. **A conformance suite** — an external, RFC-grounded test harness we
   did NOT write, run against the running server.
2. **A real-client smoke test** — at least one canonical client tool
   (kernel `mount`, AWS CLI, `fio`, kubelet) successfully completes a
   round-trip operation.
3. **Per-PR regression tests** in our own test suite, locking in any
   bug surfaced by either of the above.

A feature is NOT "done" until 1 and 2 are passing in addition to our
internal tests.

## Per-protocol matrix

| Protocol | Conformance suite | Real-client smoke | Status |
| -------- | ----------------- | ----------------- | ------ |
| NFS v4.0 | `pynfs/nfs4.0` | `mount -t nfs4 -o vers=4.0` | not running yet |
| NFS v4.1 | `pynfs/nfs4.1` | `mount -t nfs4 -o vers=4.1` | not running yet |
| NFS v4.2 | `pynfs/nfs4.2` | `mount -t nfs4 -o vers=4.2` | pynfs run manually, not automated; kernel mount covered by `crates/nvstor-nfs/scripts/mount-smoketest.sh` |
| S3 | [s3-tests](https://github.com/ceph/s3-tests) | `aws s3 cp` / `mc` | not running yet |
| NBD | `nbd-client` builtin tests | `nbd-client` from `nbd-utils` | not running yet |
| FUSE | none (FUSE is a kernel interface, not a protocol) | `ls`/`cat`/`stat` on the mount | manual |
| CSI | [csi-test](https://github.com/kubernetes-csi/csi-test) | kubelet provisioning | `scripts/csi-sanity.sh` (manual, not in CI) |
| Raft | [Jepsen](https://jepsen.io/) | n/a (intra-cluster) | not running yet |

## Where to run them

### Tier 1: per-PR (CI)

- Internal unit + integration tests (`cargo test --workspace`).
- `nvstor-nfs-export` binary smoke tests
  (`crates/nvstor-nfs/tests/binary_smoke.rs`) — CLI surface, bind and
  TCP accept.

The checks are listed in `AGENTS.md` and run in the NVIDIA/nvcf
repository's CI. Tests that need RDMA hardware are not run in CI.

### Tier 2: nightly (not yet set up)

- `pynfs` against `nvstor-nfs-export`. Pass-rate threshold to be
  established at first run; treat as a tracking metric. Failures are
  filed as issues.
- `s3-tests` against `nvstor-server`'s S3 gateway. Same threshold
  policy.
- `csi-sanity` against `nvstor-csi` (`scripts/csi-sanity.sh`).

### Tier 3: pre-release

- Real kernel client smoke (`mount -t nfs4` from a fresh box).
- `fio` workloads for any performance statement made in the release
  notes; the numbers are measured on the release build, not carried
  over.
- Failure-injection: kill a node mid-write, partition the cluster, fill
  a disk (see "Failure-injection sketch" below).

## Setting up `pynfs`

```bash
# Ubuntu/Debian
sudo apt install python3-gssapi python3-krb5 swig

git clone git://git.linux-nfs.org/projects/bfields/pynfs.git
cd pynfs
./setup.py build
cd nfs4.1
./testserver.py 127.0.0.1:12049/ --maketree   # populates the export
./testserver.py 127.0.0.1:12049/ all          # runs every test
```

pynfs has separate `nfs4.0` and `nfs4.1` suites; the 4.1 suite also
exercises the session machinery 4.2 builds on.

A run produces a list of test names with their result. Track the pass
count per commit and treat regressions as release blockers.

## Setting up `s3-tests`

```bash
sudo apt install python3-virtualenv
git clone https://github.com/ceph/s3-tests.git
cd s3-tests
./bootstrap

cat > config.yaml <<EOF
s3:
  default:
    host: 127.0.0.1
    port: 9000
    is_secure: False
    access_key: <access key>
    secret_key: <secret key>
    bucket: s3-tests-{random}
EOF

S3TEST_CONF=config.yaml ./virtualenv/bin/nosetests s3tests
```

## Setting up `csi-test`

```bash
go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest
scripts/csi-sanity.sh
```

## Failure-injection sketch

For nvstor's distributed claims to mean anything, we must run:

- **Network partition.** `iptables -A INPUT -s <peer> -j DROP` between
  Raft nodes; verify quorum, fence, recover. Repeat with one drop on
  the leader, one on a follower.
- **Mid-write crash.** `kill -9` the nvstor-server during a large
  WRITE, then restart and verify the partial state is recovered or
  cleanly rejected (not corrupted).
- **Disk full.** `truncate -s` a backing file to small size, exhaust,
  verify ENOSPC propagates, no in-memory state thinks the write
  succeeded.
- **Corrupt chunk.** Flip a byte in an erasure-coded shard; verify
  read reconstructs from parity and reports the corruption.
- **Slow disk.** `setpci`/`tc` to inject latency; verify the blocking
  pool absorbs it and async workers stay responsive.

`scripts/cluster-chaos.sh` covers part of the mid-write crash case: a
sustained NBD write workload, SIGKILL of a non-leader node, survivors
checked for health and data, and the killed node's rejoin. Partitions,
slow disks, leader kill and corrupt shards are not covered.

## Open work

- [ ] CI job: nightly pynfs (one box, fresh server per run).
- [ ] CI job: nightly s3-tests.
- [ ] CI job: csi-sanity.
- [ ] Capture the first pass-rate baseline for each suite and file
  per-test issues for the gaps.
- [ ] Write concrete failure-injection scenarios for partitions, slow
  disks, leader kill and corrupt shards.
