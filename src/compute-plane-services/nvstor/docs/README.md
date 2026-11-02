# nvstor documentation

Start with [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Overview

- [`ARCHITECTURE.md`](ARCHITECTURE.md): crates, `PmStore` and `ChunkManager`, write durability, every protocol, the RDMA bootstrap, `nvstor-peer`, cluster mode, security model, deployment and open items.
- [`figures/README.md`](figures/README.md): the architecture figures and how to regenerate them.

## Running it

- [`RDMA_SETUP.md`](RDMA_SETUP.md): RDMA fabric and RoCE setup, and troubleshooting.
- [`nfs-rdma.md`](nfs-rdma.md): NFS over RDMA: configuration, kernel-client use, transport limits.
- [`../crates/nvstor-nfs/README.md`](../crates/nvstor-nfs/README.md): `nvstor-nfs-export` flags, behaviour, systemd unit, troubleshooting.
- [`../deploy/nfs-rdma/README.md`](../deploy/nfs-rdma/README.md): deploying NFS over RDMA and `nvstor-peer` fan-out on a multi-rail RoCE fabric.
- [`peer-copy.md`](peer-copy.md): `nvstor-peer`, copying a directory node to node over RDMA.
- [`model-hydration.md`](model-hydration.md): `s3_hydrate`, copying model shards node to node over TCP or RDMA.
- [`../crates/nvstor-fuse/README.md`](../crates/nvstor-fuse/README.md): the FUSE filesystem.

## Design notes

- [`design/fetch-gpu.md`](design/fetch-gpu.md): `nvstor-peer fetch --gpu`, from the source's NVMe into GPU memory with the disk copy in the background.
- [`design/state-model.md`](design/state-model.md): where state lives in the NFS code: per server, per connection, per request.
- [`design/threading-model.md`](design/threading-model.md): the threading model.
- [`design/conformance-strategy.md`](design/conformance-strategy.md): how protocol conformance is tested with real clients.
