# nvstor-fuse

**Experimental.** A read-only FUSE filesystem that serves a directory over `/dev/fuse`, aimed at container image serving: unpacked image layers used as overlay lowerdirs without copying them.

## Overview

`nvstor-fuse` has two modes, both in the `nvstor-mount` binary:

- **Local passthrough** — serves an existing directory tree as a read-only FUSE mount.
- **Remote** — fetches files on first access from an `nvstor-fs-serve` process on another node and caches them on local disk.

Key properties:

- **Read-only** — every write operation returns `EROFS`.
- **Negative lookup cache** — repeated probes for files that do not exist are answered from memory.
- **Kernel page cache** — `FOPEN_KEEP_CACHE` for file data, long attribute and entry TTLs.
- **POSIX attributes** — uid, gid, mode, rdev, nlink, blocks, device nodes, symlinks (reported as symlinks via `lstat` and `readlink`).
- **Extended attributes** — pass-through via `lgetxattr`/`llistxattr`.
- **Per-operation metrics** — atomic counters and latency totals.

Requests are handled one at a time: `fuser::mount2` runs the FUSE session loop on the mounting thread (see [`docs/design/threading-model.md`](../../docs/design/threading-model.md)).

## Prerequisites

```bash
# Ubuntu/Debian
sudo apt install fuse3 libfuse3-3

# For building from source, also need:
sudo apt install libfuse3-dev
```

Kernel requirement: Linux with FUSE support.

## Building

```bash
cargo build --release --bin nvstor-mount --bin nvstor-fs-serve
ls -lh target/release/nvstor-mount target/release/nvstor-fs-serve
```

The only runtime dependency is libfuse3.

## Quick Start

```bash
# Serve a directory
nvstor-mount /path/to/source /path/to/mountpoint

# Verify
ls -la /path/to/mountpoint
cat /path/to/mountpoint/some-file

# Unmount
fusermount3 -u /path/to/mountpoint
```

## Usage

```text
nvstor-mount [options] <source_dir> <mountpoint>          # local passthrough
nvstor-mount --remote <host:port> [options] <mountpoint>  # remote

Options:
  --remote HOST:PORT   Fetch from a remote nvstor-fs-serve
  --cache-dir DIR      Local cache directory, remote mode (default: /var/cache/nvstor)
  --cache-size SIZE    Max cache size, remote mode: 1G, 512M, ... (default: 10G)
  --allow-other        Allow other users to access the mount (default: enabled)
  --no-allow-other     Restrict to mounting user only
  --attr-timeout N     Attribute cache timeout in seconds (default: 3600)
  --entry-timeout N    Directory entry cache timeout in seconds (default: 3600)

Environment:
  RUST_LOG=debug     Debug logging (lookups, negative-cache hits, forgets)
  RUST_LOG=info      Standard logging (lifecycle events only)
```

```text
nvstor-fs-serve [--port PORT] <directory>    # default port 9879
```

### Remote mode security

`nvstor-fs-serve` has **no authentication and no address filtering**: it listens on `0.0.0.0` and serves the directory to anyone who can reach the port. Its default port, 9879, is also the default S3-over-TCP port of `nvstor-server`, so do not run both on one host with defaults. Use it only on a trusted, isolated network, or restrict the port with a firewall.

## Deployment

### Single Binary Deploy

```bash
# Build on a machine with a Rust toolchain
cargo build --release --bin nvstor-mount

# Copy to target nodes (no Rust needed on targets)
scp target/release/nvstor-mount node:/usr/local/bin/

# Only runtime dependency is libfuse3
ssh node 'apt install -y fuse3 libfuse3-3'
```

### Container Image Serving

**Scenario:** node B has an unpacked container image. Node C needs to run a container from it without copying the image first.

**Option A: remote FUSE (cross-node)**

```bash
# Node B (source)
nvstor-fs-serve /var/lib/containers/committed/sha256:abc123

# Node C: fetch on demand, cache locally
nvstor-mount --remote node-b:9879 /mnt/layer-abc123

# Node C: use as overlay lowerdir
mount -t overlay overlay \
  -o lowerdir=/mnt/layer-abc123,upperdir=/tmp/upper,workdir=/tmp/work \
  /mnt/rootfs
```

**Option B: local FUSE (same node)**

```bash
# Source node serves the layer directly
nvstor-mount --allow-other \
  /var/lib/containers/committed/sha256:abc123 \
  /mnt/layer-abc123

# The container runtime / snapshotter uses /mnt/layer-abc123 as overlay lowerdir
# No network — local FUSE with kernel page caching
```

**Option C: spawned by a snapshotter**

A containerd snapshotter can start `nvstor-mount` as a subprocess per layer:

```go
cmd := exec.Command("nvstor-mount",
    "/var/lib/containers/committed/"+chainID,
    mountpoint)
cmd.Start()
```

### Systemd Service

```ini
# /etc/systemd/system/nvstor-mount@.service
[Unit]
Description=nvstor FUSE mount for %i
After=local-fs.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nvstor-mount /data/committed/%i /mnt/nvstor/%i
ExecStop=/usr/bin/fusermount3 -u /mnt/nvstor/%i
Restart=on-failure
RestartSec=5

# Security
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/mnt/nvstor

[Install]
WantedBy=multi-user.target
```

```bash
# Enable for a specific chain
systemctl start nvstor-mount@sha256:abc123
systemctl status nvstor-mount@sha256:abc123
```

## Caching

### Kernel Page Cache

For a read-only filesystem the kernel page cache does most of the work:

- First read: FUSE round-trip → `pread(2)` on the source file → data returned.
- Later reads: the kernel serves from the page cache without a FUSE round-trip.
- `attr_timeout=3600` means `stat()` results are cached for an hour.
- `entry_timeout=3600` means `lookup()` results are cached for an hour.

### Negative Lookup Cache

Container-runtime CDI hooks probe for many files that do not exist (device libraries, plugin paths, accelerator drivers). Without negative caching, each probe triggers a `stat()` on the source filesystem.

nvstor-fuse caches `ENOENT` results in memory for the entry TTL, so a repeated probe for the same missing path is answered without touching the filesystem.

### Remote mode caches

Remote mode keeps fetched file contents in an on-disk cache under `--cache-dir`, bounded by `--cache-size`, plus bounded in-memory stat and negative caches. Its inode map is not bounded: inodes are never evicted while the kernel may still reference them; a warning is logged once the map is very large, and remounting clears it.

### Metrics

On unmount, the passthrough filesystem logs operation counts:

```text
INFO FUSE filesystem destroyed lookups=<n> reads=<n> bytes=<n> errors=<n>
```

For per-lookup tracing, set `RUST_LOG=debug`:

```text
DEBUG lookup parent=<ino> name="usr" ino=<ino>
DEBUG lookup: negative cache hit parent=<ino> name="libfoo.so"
```

## Architecture

```text
Container Runtime (runc/crun)
    │
    ▼
Overlay filesystem (kernel)
    │
    ├── upperdir: /tmp/upper (writable, per-container)
    │
    └── lowerdir: /mnt/nvstor/sha256:abc123 (read-only)
                      │
                      ▼
                nvstor-mount (FUSE)
                      │
                      ▼
                /var/lib/containers/committed/sha256:abc123
                (local filesystem)
```

### FUSE Operations Implemented

| Operation | Description | CVMFS equivalent |
|-----------|-------------|-----------------|
| `init` | Lifecycle start, log config | `cvmfs_init` |
| `destroy` | Lifecycle end, dump metrics | `cvmfs_destroy` |
| `lookup` | Resolve name → inode + attrs | `cvmfs_lookup` |
| `forget` | Kernel done with inode (refcount) | `cvmfs_forget` |
| `getattr` | File attributes (stat) | `cvmfs_getattr` |
| `readlink` | Symlink target | `cvmfs_readlink` |
| `open` | Open file (FOPEN_KEEP_CACHE) | `cvmfs_open` |
| `release` | Close file handle | `cvmfs_release` |
| `read` | Read file data (pread) | `cvmfs_read` |
| `opendir` | Open dir + cache listing | `cvmfs_opendir` |
| `readdir` | Serve cached dir listing | `cvmfs_readdir` |
| `releasedir` | Free cached listing | `cvmfs_releasedir` |
| `access` | Permission check (kernel delegates) | N/A |
| `statfs` | Filesystem stats (statvfs) | `cvmfs_statfs` |
| `getxattr` | Extended attribute value | `cvmfs_getxattr` |
| `listxattr` | List extended attribute names | `cvmfs_listxattr` |
| Write ops | All return `EROFS` | N/A |

The mount uses the kernel's `default_permissions`, so access checks are done by the kernel against the reported mode and owner.

## Library API

For embedding in other Rust projects:

```rust
use nvstor_fuse::passthrough::{PassthroughFs, MountOptions};
use std::path::Path;

// Create filesystem
let fs = PassthroughFs::new("/path/to/source".into())?
    .with_ttl(3600, 3600);  // 1 hour cache

// Mount (blocks until unmounted)
fs.mount(Path::new("/mnt/target"), MountOptions::default())?;
```

## Comparison with CVMFS

| Aspect | CVMFS | nvstor-fuse |
|--------|-------|-------------|
| Language | C++ | Rust |
| Data source | Remote catalog + HTTP fetch | Local directory, or `nvstor-fs-serve` over TCP |
| Chunking | Content-addressed chunks | Whole files |
| Auth | VOMS/X.509 | Kernel permissions; remote mode has none |
| Cache | Custom chunk cache | Kernel page cache; on-disk file cache in remote mode |
| Dir listing | Pre-serialized buffer | Cached entry list |
| Neg cache | Yes | Yes |
| xattr | From catalog | Pass-through |
| Device nodes | Yes | Yes |
| Metrics | perf counters + histograms | Atomic counters + latency totals |

## Troubleshooting

**Mount fails with "Permission denied"**

```bash
# Check /etc/fuse.conf has user_allow_other
echo "user_allow_other" | sudo tee -a /etc/fuse.conf
```

**Mount fails with "fusermount3: option allow_other only allowed if 'user_allow_other' is set"**

```bash
sudo sed -i 's/#user_allow_other/user_allow_other/' /etc/fuse.conf
```

**Mount hangs**

```bash
# Check FUSE kernel module
lsmod | grep fuse
# If missing:
sudo modprobe fuse
```

**Stale mount after crash**

```bash
fusermount3 -u /mnt/target  # or
sudo umount -l /mnt/target
```
