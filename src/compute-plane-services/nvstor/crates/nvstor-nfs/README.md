# nvstor-nfs

NFS v4.2 server implementation used by `nvstor-nfs-export`, a standalone
binary that serves any local directory over NFS without an nvstor storage
backend. Pure passthrough — the intended deployment for shared container
image layers.

Protocol core (XDR, ONC RPC, NFSv4 COMPOUND processing) is also used by the
broader nvstor server when a synthetic VFS is wanted.

## Status

**Experimental.** The standalone export and NFS over RDMA have been
exercised end to end with the Linux kernel client (TCP and RDMA, NFS v4.2
by default), but have not been run in production.

What's in place:

- The NFSv4.0/4.1/4.2 COMPOUND operations a Linux client uses, unit-tested;
  a real-kernel-client smoketest (`mount -t nfs4` with no version override,
  `ls`, `cat`, `stat`, nested lookup, a 4 MiB sha256 round-trip, a mid-file
  pread, a `/metrics` scrape).
- Library tests, protocol integration tests, binary smoke tests and a
  protocol exerciser (`cargo test -p nvstor-nfs`).
- Prometheus exporter on a configurable port (default 9101) with op
  counters, per-category latency histograms, and connection/session
  gauges; Grafana dashboard JSON at `deploy/dashboards/nvstor-nfs.json`.
- Access control: a required client address list (`--allow-client`), and
  per-request credentials: every operation on the passthrough export checks
  the caller's AUTH_SYS uid, gid and supplementary gids against the inode's
  owner, group and mode; root is squashed by default; created inodes belong
  to the caller (see "Run" below).
- Namespace safety: every inode is opened beneath the export root with
  `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` or from its kernel
  handle; symlinks are never followed; FIFOs and devices are never opened
  for I/O; caller context is per COMPOUND, never shared between requests.
- HMAC-sealed filehandles carrying the inode generation (`--fh-key`), a
  bounded inode cache, READDIR cookies that are the filesystem's `d_off`
  with a per-directory cookie verifier.
- Durability: COMMIT and stable WRITEs really fsync; a failed fsync is
  reported, not lost.
- Status codes as Linux nfsd: NFS4ERR_SYMLINK where a directory or regular
  file is needed and the object is a symlink (OPEN of a directory is
  NFS4ERR_ISDIR); PUTFH tells BADHANDLE (layout never issued) from STALE;
  READDIR answers NOT_SAME / BAD_COOKIE.
- Request size caps, idle and write timeouts, a bounded session reply
  cache, positional I/O on a blocking pool.
- Zero warnings with `RUSTFLAGS="-D warnings"`. No `unwrap()` in
  production code; invariants use `expect` with a reason.

NFS over RDMA (RPC-over-RDMA v1) has been exercised with the Linux kernel
client across hosts, over one NIC and over several rails with session
trunking, including a `kill -9` server restart with `--fh-key`. See the
[NFS/RDMA guide](../../docs/nfs-rdma.md) for configuration, multi-rail
mounts and limits.

Not yet: Kerberos (AUTH_SYS and AUTH_NONE only), synthetic-VFS inode
sharding (irrelevant to passthrough). Server-side
COPY / SEEK / ALLOCATE / DEALLOCATE are implemented and unit-tested but not
yet exercised end-to-end by a real kernel client.

## Build

```text
cargo build --release -p nvstor-nfs --bin nvstor-nfs-export
```

The binary lands at `target/release/nvstor-nfs-export`.

Default builds require `libibverbs-dev` and `librdmacm-dev`. Use
`--no-default-features` for a standalone TCP-only build.

## Run

```text
# Privileged port (2049 is NFS's well-known port).
sudo nvstor-nfs-export --allow-client 10.0.0.0/8 --fh-key /var/lib/nvstor/fh.key \
    /var/lib/containers/committed/sha256:abc

# Unprivileged high port for dev / non-root setups.
nvstor-nfs-export --allow-client 127.0.0.1 --port 12049 /tmp/my-export

# --allow-client (repeatable, required) names the clients, over TCP and RDMA;
# each prefix admits only its own address family. --fh-key keeps filehandles
# valid across restarts (see docs/nfs-rdma.md).

# Root squashing is on: uid 0 (and gid 0) act as 65534 unless changed with
# --anon-uid / --anon-gid, or turned off with --no-root-squash.
nvstor-nfs-export --allow-client 10.0.0.0/8 --anon-uid 65534 --anon-gid 65534 /srv/data
# NFS/TCP on chosen addresses only, or no TCP at all (RDMA only).
nvstor-nfs-export --allow-client 10.0.0.0/8 --bind 10.0.0.5 --bind fd00::5 /data
nvstor-nfs-export --allow-client fd12:3456::/32 --no-tcp --rdma-port 20049 /models
```

Environment:

| Variable     | Default | Purpose                                  |
| ------------ | ------- | ---------------------------------------- |
| `RUST_LOG`   | `info`  | tracing-subscriber filter (`debug`, etc) |

Process behavior:

- Binds `0.0.0.0:<port>` (IPv4) for TCP, or each `--bind ADDR` (repeatable;
  IPv6 addresses IPv6-only); `--no-tcp` binds no TCP listener and then
  requires NFS/RDMA. The NFS/RDMA listener is dual-stack. Exit 1 if a bind
  fails (usually means the port is in use, or `--port 2049` without root).
- A failed TCP accept (out of descriptors, a reset connection) is logged
  and retried with backoff; it never stops the server.
- A fatal NFS/RDMA listener error (device removal) never takes NFS/TCP
  down: it is logged, `nvstor_nfs_rdma_listener_up` drops to 0, and the
  listener is re-created with backoff (1 s to 30 s). With `--no-tcp` the
  process exits 1 instead, so Kubernetes restarts it.
- Refuses to start without at least one `--allow-client`.
- The export's NFS path is `/` (the root of the directory being served).
- Default policy: read-only, `root_squash=true` (anonymous uid/gid
  65534), `sec=["sys"]`.
- Access control is the client address list plus each request's
  credentials. The server opens files with its own privileges (root in the
  deployed form; `open_by_handle_at` needs `CAP_DAC_READ_SEARCH`), so it
  makes the permission decisions itself, against the inode it opened, with
  POSIX rules on the caller's AUTH_SYS uid, gid and supplementary gids:
  - LOOKUP and LOOKUPP need search (x) on the directory, READDIR read (r).
  - READ needs r (or x on a regular file, so programs can be run), WRITE
    w; the file's owner always passes (as Linux nfsd, so a file created
    read-only stays writable by its creator). OPEN checks r/w for the
    requested share access without that override.
  - CREATE, OPEN(CREATE), mkdir, symlink, link, REMOVE and RENAME need w
    and x on the directory; in a sticky directory removing or renaming
    away needs ownership of the entry or the directory; moving a directory
    to a new parent needs w on it. Hard links follow
    `fs.protected_hardlinks=1`.
  - SETATTR: size needs write access, mode and client-set times the owner
    (or root); owner changes only root, group changes the owner to one of
    its groups. A chmod by a caller outside the file's group drops
    set-group-id; a data change by anyone but root drops set-user-id.
  - ACCESS reports exactly what the rules allow; on a read-only export no
    modify, extend, delete or xattr-write bit is granted.
  - xattrs are confined to the `user.` namespace (as RFC 8276 and Linux
    nfsd): getting needs r, setting and removing w.
  - uid 0 is squashed to `--anon-uid`/`--anon-gid` (and every gid 0 to the
    anonymous gid) unless `--no-root-squash`; AUTH_NONE callers run as the
    anonymous ids. With `--no-root-squash`, uid 0 reads and writes every
    file, as root does locally.
- With `--read-write`, files, directories and symlinks a client creates
  belong to the caller (`fchown` on the new inode, the group of a
  set-group-id parent inherited). Giving an inode to another uid needs
  `CAP_CHOWN`: a non-root server refuses the create (NFS4ERR_PERM) and
  removes the entry rather than leave it owned by the server's uid, so a
  non-root export only accepts writers with its own uid.
- Without `--fh-key`, or without `CAP_DAC_READ_SEARCH`, filehandles go
  stale when the server restarts (it logs a warning at startup).
- Logs connection accept/close, COMPOUND status, and slow-op warnings.

## Client

From a Linux box:

```text
sudo mount -t nfs4 -o port=12049 server:/ /mnt/target
# or, on port 2049:
sudo mount -t nfs4 server:/ /mnt/target
```

The kernel client negotiates NFS v4.2 by default. v4.0 and v4.1 also
work via `-o vers=4.0` / `-o vers=4.1` if the client requires them.

Requires `nfs-common` (Debian/Ubuntu) or `nfs-utils` (RHEL-family) on the
client for `mount.nfs4`.

### What's supported

Every op a kernel client needs for read workloads:

- PUTROOTFH, PUTFH, GETFH, SAVEFH, RESTOREFH, LOOKUP, LOOKUPP
- GETATTR, ACCESS, READLINK, READDIR, READ
- NFS v4.1 session ops: EXCHANGE_ID, CREATE_SESSION, SEQUENCE,
  DESTROY_SESSION, RECLAIM_COMPLETE, SECINFO_NO_NAME, BIND_CONN_TO_SESSION

Write / mutation ops (OPEN with CREATE, CREATE, REMOVE, RENAME, LINK,
SYMLINK, SETATTR, WRITE, COMMIT, LOCK/LOCKU, ALLOCATE, DEALLOCATE, COPY,
CLONE) work when the export's `read_only` flag is off.

### Limits

| Limit                                 | Value     |
| ------------------------------------- | --------- |
| MAX_COMPOUND_OPS                      | 256       |
| MAX_OPAQUE_LEN (XDR)                  | 64 MiB    |
| MAX_READ_COUNT (single READ op)       | 64 MiB    |
| MAX_FRAGMENT_LEN                      | 64 MiB    |
| MAX_RECORD_LEN (stitched fragments)   | 128 MiB   |
| MAX_SESSION_REPLY_CACHE_BYTES         | 64 KiB    |
| Default max_connections               | 1000 (binary uses 1024) |
| Connections per client address        | min(64, max_connections / 4), TCP + RDMA |
| Session maxrequestsize / maxresponsesize | min(client, 1 MiB + 64 KiB) |
| Session maxoperations                 | min(client, MAX_COMPOUND_OPS) |
| Session slots (maxrequests)           | min(client, 64); SEQUENCE reports the same table |
| Default idle_timeout                  | 300 s     |
| Default write_timeout                 | 60 s      |
| BufStream buffers                     | 1 MiB each |

## Configuration (binary defaults)

The shipped `nvstor-nfs-export` builds this export policy:

```rust
NfsExport {
    path: "/",
    allowed_clients: /* the --allow-client values; required */,
    read_only: true,          // false with --read-write
    root_squash: true,        // false with --no-root-squash
    anon_uid: 65534,          // --anon-uid
    anon_gid: 65534,          // --anon-gid
    sec: ["sys"],
}
```

For custom config, use the `nvstor-nfs` crate as a library and build
your own binary around `NfsServer::new(processor, config)`.

## Metrics

`nvstor-nfs-export` exposes Prometheus metrics on a configurable port
(default `9101`, set with `--metrics-port`, `0` disables):

```text
curl http://server:9101/metrics
curl http://server:9101/healthz
```

Series include:

| Name | Type | Notes |
| ---- | ---- | ----- |
| `nvstor_nfs_compound_total` / `_errors_total` | counter | Per-COMPOUND aggregate. |
| `nvstor_nfs_{read,write,copy}_bytes_total` | counter | Bytes moved. |
| `nvstor_nfs_{read,write,open,lookup,getattr,create,copy,lock}_ops_total` | counter | Per-op counts. |
| `nvstor_nfs_active_{connections,sessions,clients}` | gauge | Current state. |
| `nvstor_nfs_op_duration_seconds` | histogram | `category` label: `read`/`write`/`metadata`/`session`/`other`. Buckets: 10 µs through 5 s. |

Scrape config snippet for Prometheus:

```yaml
scrape_configs:
  - job_name: nvstor-nfs
    static_configs:
      - targets: ["host1:9101", "host2:9101", "host3:9101"]
    scrape_interval: 15s
```

Grafana dashboard JSON: `deploy/dashboards/nvstor-nfs.json`. Import via
*Dashboards → New → Import → Upload JSON*. Pin a Prometheus datasource
and pick instances via the `instance` template variable.

## systemd unit

```ini
# /etc/systemd/system/nvstor-nfs-export.service
[Unit]
Description=nvstor NFS v4.2 passthrough export
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nvstor-nfs-export --allow-client 10.0.0.0/8 --fh-key /var/lib/nvstor/fh.key /var/lib/containers/committed
Restart=on-failure
RestartSec=5
Environment=RUST_LOG=info

# Harden the process — it reads its export, binds its port, and creates
# its filehandle key once under /var/lib/nvstor (StateDirectory).
NoNewPrivileges=yes
ProtectSystem=strict
ReadOnlyPaths=/var/lib/containers/committed
StateDirectory=nvstor
StateDirectoryMode=0700
ProtectHome=yes
PrivateTmp=yes
# CAP_DAC_READ_SEARCH lets the server decode filehandles after a restart
# (open_by_handle_at); without it clients get stale handles on restart.
# It also bypasses file read permissions, so every file under the export
# becomes readable by admitted clients; drop it if that is not wanted.
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_DAC_READ_SEARCH
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
```

```text
sudo systemctl daemon-reload
sudo systemctl enable --now nvstor-nfs-export
sudo journalctl -u nvstor-nfs-export -f
```

## Smoke test

Automated (no privileges needed):

```text
cargo test -p nvstor-nfs
```

Real kernel-client (needs sudo + `nfs-common`):

```text
bash crates/nvstor-nfs/scripts/mount-smoketest.sh
```

Exits 0 on success. Covers `mount`, `ls`, `cat`, `stat`, a 4 MiB sha256
round-trip, and a pread at a mid-file offset.

## Troubleshooting

- **"Error binding 0.0.0.0:2049: Permission denied"** — port 2049 is
  privileged. Run with `sudo` or pass `--port 12049` and mount with
  `-o port=12049`.
- **`mount.nfs4: access denied by server`** (or the connection is
  dropped at once) — the client's address is not covered by any
  `--allow-client`. Each prefix admits only its own address family: an
  IPv6-only client needs an IPv6 prefix. Otherwise, check for a firewall
  between client and server.
- **`Permission denied` on files the client's user can read locally** —
  the export checks the uid and groups the client sends. A client running
  as root is squashed to 65534 unless the server runs with
  `--no-root-squash`; uids must match between client and server (AUTH_SYS
  carries numbers, not names).
- **`mount.nfs4: Operation not permitted`** — kernel's NFS v4.2 not
  available. Check `cat /proc/filesystems | grep nfs`. Ubuntu/Debian
  needs `nfs-common`; RHEL/Fedora needs `nfs-utils`.
- **`mount.nfs4: server ... requires stronger authentication`** —
  client sent AUTH_NONE and the export's `sec=["sys"]` rejects it.
  Linux clients default to AUTH_SYS, so this indicates something unusual
  like a `sec=none` mount option or a misconfigured client.
- **Stalled reads on large files** — check that the blocking pool has
  capacity. Tokio defaults to 512 blocking threads; if you run more than
  ~512 simultaneous slow reads, some will queue. Either raise the pool
  size with `tokio::runtime::Builder::max_blocking_threads` in a custom
  embedding, or lower client concurrency.

## Design notes

- **Passthrough maps inode 1 → export root.** NFS's PUTROOTFH always uses
  filehandle for inode 1 regardless of the underlying filesystem's root
  inode.
- **Per-COMPOUND caller context** is stack-local (`CallerCtx`), never
  stored on the shared processor. Prevents concurrent-request
  contamination.
- **Blocking I/O isolation**: COMPOUND dispatch runs on tokio's blocking
  pool so a slow disk throttles one request, not the whole server.
- **Positional I/O**: READ / WRITE use `read_at` / `write_at` against
  the backing file, never slurping the whole thing.
- **Mutation gating**: `read_only=true` on the export returns NFS4ERR_ROFS
  for WRITE, CREATE, REMOVE, RENAME, LINK, SETATTR, ALLOCATE,
  DEALLOCATE, COPY, CLONE and OPEN with create — before anything
  reaches the VFS. OPEN without create, CLOSE and LOCK/LOCKU still work,
  since a read-only mount issues them.
