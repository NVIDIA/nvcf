# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# syntax=docker/dockerfile:1.7
#
# nvstor container image — builds nvstor-server, nvstor-csi, and
# the s3_tcp_bench example into one Ubuntu 22.04 runtime image.
# The same image runs on Ubuntu 22.04 and 24.04 hosts without a
# rebuild: the libc symbol level it links against (glibc 2.35 from
# Ubuntu 22.04) is forward-compatible with 24.04.
#
# Multi-stage:
#   1. builder   — rust:1.94 on Debian bookworm + protoc + libclang
#                  + libibverbs-dev. Compiles the whole workspace
#                  with nvstor-storage's io-uring feature, which
#                  nvstor-server pulls in unconditionally.
#   2. runtime   — Ubuntu 22.04 + libibverbs1 + librdmacm1 +
#                  nfs-common + ibverbs-utils. Holds just the
#                  binaries.
#
# ENTRYPOINT defaults to nvstor-server. Override with
#   docker run ... --entrypoint /usr/local/bin/nvstor-csi
# for the CSI driver, or
#   docker run ... --entrypoint /usr/local/bin/s3_tcp_bench
# for the bench.

# ---------------------------------------------------------------------
# Builder
# ---------------------------------------------------------------------
FROM rust:1.94-bookworm AS builder

# Build deps:
#   pkg-config + libssl-dev: rustls and friends pull these in.
#   libclang-dev:            librocksdb-sys's bindgen step needs clang.
#   libibverbs-dev,
#   librdmacm-dev:           nvstor-rdma::build.rs links -libverbs;
#                            the runtime stage gets the .so but the
#                            build needs the headers + link target.
#   protobuf-compiler:       nvstor-csi's tonic build.rs runs protoc.
#   cmake, make, build-essential: rocksdb + zstd compile chain.
RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config \
    libssl-dev \
    libclang-dev \
    libibverbs-dev \
    librdmacm-dev \
    protobuf-compiler \
    libprotobuf-dev \
    cmake \
    make \
    build-essential \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# tonic-build / prost-build don't auto-discover the well-known
# proto files (google/protobuf/{descriptor,timestamp,wrappers}.proto)
# unless PROTOC_INCLUDE points at the dir libprotobuf-dev installs
# them to. Without this, nvstor-csi's build.rs fails with
# "google/protobuf/descriptor.proto: File not found."
ENV PROTOC_INCLUDE=/usr/include

WORKDIR /build

# Copy the full workspace. We could shave build time with the
# cargo-chef recipe pattern, but that adds Dockerfile complexity
# we don't want during the active-development phase. Plain copy
# keeps the layer set readable; CI caching can recover the lost
# incremental builds when this image is built often.
COPY . .

# Build the binaries we ship. Three artifacts:
#   * nvstor-server   — multi-protocol storage server (S3, NFS,
#                       NBD, s3-rdma, s3-tcp).
#   * nvstor-csi      — k8s CSI plugin.
#   * s3_tcp_bench    — example we use for cluster benches; small
#                       enough to roll into the same image so
#                       the bench client pod can `kubectl exec`
#                       without a separate image.
RUN cargo build --release --locked \
        --bin nvstor-server \
        --bin nvstor-csi \
 && cargo build --release --locked \
        --example s3_tcp_bench -p nvstor-rdma \
 && strip /build/target/release/nvstor-server \
          /build/target/release/nvstor-csi \
          /build/target/release/examples/s3_tcp_bench

# ---------------------------------------------------------------------
# Runtime
# ---------------------------------------------------------------------
FROM ubuntu:22.04 AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libssl3 \
    libibverbs1 \
    librdmacm1 \
    ibverbs-utils \
    nfs-common \
    iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Non-root user. UID 1000 matches the default "ubuntu" user on
# Ubuntu nodes, so hostPath data dirs owned by it are readable.
RUN groupadd -r nvstor --gid=1000 \
    && useradd -r -g nvstor --uid=1000 --home-dir=/data --create-home nvstor

COPY --from=builder /build/target/release/nvstor-server      /usr/local/bin/nvstor-server
COPY --from=builder /build/target/release/nvstor-csi          /usr/local/bin/nvstor-csi
COPY --from=builder /build/target/release/examples/s3_tcp_bench /usr/local/bin/s3_tcp_bench

# Data + config dirs the binaries expect. /config gets a config
# file mounted at deploy time; /data/nvstor holds RocksDB +
# PmStore.
RUN mkdir -p /data/nvstor /config \
    && chown -R nvstor:nvstor /data /config

USER nvstor
WORKDIR /data

ENV RUST_LOG=nvstor_server=info,nvstor_rdma=info
ENV RUST_BACKTRACE=1

# nvstor ports at their defaults — exposed for documentation; the actual
# listeners depend on the nvstor.toml mounted at /config/nvstor.toml.
#   9000  S3 HTTP gateway
#   9100  native wire protocol
#   9101  health / metrics
#   9102  Raft
#   9877  RDMA transport (+ rpc on 9878)
#   9879  s3-over-TCP
#   10809 NBD
#   2049  NFS
EXPOSE 2049 9000 9100 9101 9102 9877 9878 9879 10809

ENTRYPOINT ["/usr/local/bin/nvstor-server"]
CMD ["--config=/config/nvstor.toml"]
