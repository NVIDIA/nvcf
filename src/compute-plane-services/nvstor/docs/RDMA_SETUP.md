# RDMA Setup Guide

Configuring RDMA (Remote Direct Memory Access) for nvstor's data path.
nvstor uses RDMA as the primary transport; TCP is the fallback.

## Hardware requirements

- Mellanox ConnectX-4 Lx or newer (or any OFED-compatible NIC).
- Two or more nodes with RoCE- or InfiniBand-capable NICs on the same
  fabric (same L2 subnet for RoCE v1, same routable IP fabric for
  RoCE v2).
- Switch with PFC (Priority Flow Control) enabled on the RoCE traffic
  class. For lossy RoCE (ECN-based), PFC is optional but recommended.

## Software

Ubuntu / Debian:

```bash
sudo apt install libibverbs-dev librdmacm-dev ibverbs-utils rdma-core
```

RHEL / Fedora:

```bash
sudo dnf install libibverbs-devel librdmacm-devel rdma-core
```

Kernel modules expected to be loaded:

```text
ib_core, ib_uverbs, ib_cm, rdma_cm, mlx5_core, mlx5_ib
```

Verify with `lsmod | grep -E 'ib_|mlx'`.

## RoCE v1 vs RoCE v2

| | RoCE v1 | RoCE v2 |
|---|---|---|
| Addressing | Ethernet MAC (L2) | IP (L3) |
| Routing | Same L2 subnet only | Routable across subnets |
| GID | Link-local `fe80::…` | Includes IP address |
| Typical use | Single rack, same switch | Multi-rack, routed fabric |

For new deployments, prefer RoCE v2. nvstor does not negotiate the GID
type: each side picks its own GID index (`RdmaDevice::roce_gid_index`),
preferring a RoCE v2 IPv4-mapped GID, then another routable RoCE v2 GID,
and uses a link-local GID only when nothing else exists.

## Verifying the fabric

On each node:

```bash
# List RDMA devices and ports.
ibv_devices
ibv_devinfo

# List GID table entries (find RoCE v2 with your IP).
for d in $(ls /sys/class/infiniband/); do
    for p in $(ls /sys/class/infiniband/$d/ports/); do
        echo "=== $d port $p ==="
        for g in $(ls /sys/class/infiniband/$d/ports/$p/gids/); do
            gid=$(cat /sys/class/infiniband/$d/ports/$p/gids/$g 2>/dev/null)
            type=$(cat /sys/class/infiniband/$d/ports/$p/gid_attrs/types/$g 2>/dev/null)
            echo "  $g: $gid  [$type]"
        done
    done
done
```

Use `ibv_rc_pingpong` between two nodes to confirm the fabric works
before pointing nvstor at it:

```bash
# Node A
ibv_rc_pingpong -d mlx5_0 -g <roce_v2_gid_index>

# Node B
ibv_rc_pingpong -d mlx5_0 -g <roce_v2_gid_index> <nodeA_ip>
```

Expected: "1000000 iters in X seconds = Y us/iter".

## Several NICs per node (rails)

nvstor opens one transport per NIC whose port is ACTIVE and pairs each
connection with the local NIC that reaches the remote NIC (see
[model-hydration.md](model-hydration.md), "Multiple NICs"). The fabric still
has to deliver each connection on the NIC it was meant for:

- **Equal default routes.** When every rail has an equal default route (for
  example IPv6 router advertisements on each rail), the kernel sends all
  traffic to a remote address out of one NIC. nvstor's own transports bind
  the RoCE lookup to the chosen NIC's GID; the kernel NFS client cannot, so
  NFS over several rails needs a host route per server rail address
  ([nfs-rdma.md](nfs-rdma.md), "Mounting over several rails").
- **Strict reverse-path filtering.** With `rp_filter=1` on a rail, replies
  that arrive on a NIC other than the one the route table prefers are
  dropped. `nvstor-peer` warns at startup when a rail's netdev has strict
  `rp_filter`; set it to 2 (loose) on rail netdevs, or add per-rail routes.
- **Late addresses.** Rail addresses assigned by SLAAC can appear seconds
  after a pod or host starts. When it opens the NICs of its own network
  namespace, `nvstor-peer` waits up to `NVSTOR_RDMA_RAIL_WAIT_SECS` seconds
  for each rail's routable GID; a rail still without one is kept and used
  once its address appears.
- **Firewall.** Open the TCP bootstrap port of every rail (rail 0 on the
  configured port, the others above it), not just the first.

## Common troubleshooting

**Hangs between nodes (no error, just silence).** Usually PFC mismatch
or RoCE v2 requires explicit IP routing. Check:

- `ethtool -a <nic>` — Pause frame status on both ends.
- `sudo tc qdisc show dev <nic>` — Priority / DCB queues.
- `mlnx_qos -i <nic>` — DCB / PFC / ETS configuration on Mellanox NICs.

**`perftest` fails with "Completion with status: 10"** — usually a
network/firewall issue. nvstor's QP exchange runs over TCP on the
configured control port; make sure that port is open.

**RoCE v2 GID is `fe80::…` instead of your IP** — GID cache hasn't
picked up the interface's IP yet. Restart `openibd` (`sudo systemctl
restart openibd`) or reload `mlx5_ib`.

## nvstor RDMA integration

nvstor has two RDMA paths:

- **Object transports and replication** (`crates/nvstor-rdma/`): one
  transport per NIC whose port is ACTIVE ("rails", `rail.rs`). Each
  connection starts with a TCP bootstrap on the transport's port
  (`bootstrap.rs`, protocol v2): the connector proves a credential in a
  challenge-response handshake before the server creates any QP, and the
  QP numbers and GIDs are exchanged in that handshake (no RDMA-CM). Servers
  never advertise their memory; GET is a server RDMA WRITE into the
  client's buffer, PUT and replication a server RDMA READ from it. Open the
  bootstrap port of every rail in the firewall. `nvstor-server` enables
  this when it finds an active device: `[network] rdma_port` (9877) is
  rail 0, more NICs from `rdma_rail_port_base` (default `rdma_port + 16`).
  Replication falls back to TCP RPC when RDMA to a peer fails.
- **NFS over RDMA** (`crates/nvstor-nfs/src/rdma_server.rs`): RPC-over-RDMA
  v1 with RDMA-CM connection setup, for the Linux kernel client.
  `[network] nfs_rdma_port` (auto 20049 when an active device exists, 0
  disables). See [nfs-rdma.md](nfs-rdma.md). NFS over RDMA, like
  `nvstor-server`, is experimental.

`nvstor-peer` uses the object transport between two nodes
([peer-copy.md](peer-copy.md)).
