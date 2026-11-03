#!/bin/bash
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

# Cross-build nvstor binaries for aarch64 nodes on an x86_64 Linux
# host, in a container, so the host needs only docker (or podman).
#
#   deploy/nfs-rdma/build-aarch64.sh [BIN...]
#
# BIN: binaries to build (default: nvstor-nfs-export), e.g.
#   deploy/nfs-rdma/build-aarch64.sh nvstor-nfs-export nvstor-server
# Output: $OUTDIR/BIN (default target/aarch64-unknown-linux-gnu/release/ in
# the repository), ready for install-binary.sh.
#
# Why a container: nvstor-nfs links librdmacm and libibverbs and compiles C
# (nvstor-rdma's cm.c) and C++ (RocksDB), so the build needs an aarch64 C/C++
# cross toolchain and aarch64 rdma-core headers and libraries. The builder
# image is Ubuntu 24.04 (glibc 2.39; the nodes need this glibc or newer, since a
# binary linked against a newer glibc than the node's cannot load there) with gcc-aarch64-linux-gnu and
# libibverbs-dev:arm64 + librdmacm-dev:arm64 from ports.ubuntu.com. The
# binary runs against the node's libibverbs.so.1/librdmacm.so.1 (rdma-core,
# or the OFED driver stack's).
#
# The image build downloads Ubuntu packages and the Rust toolchain; the cargo
# build downloads crates. Both are cached: the image by docker, crates and
# build artifacts under $NVSTOR_CROSS_CACHE (default target/cross-aarch64).
#
# Environment:
#   DOCKER               container CLI (default: docker, else podman)
#   OUTDIR               where to copy the binaries
#   NVSTOR_CROSS_CACHE   cargo registry and target directory for the build
#   RUST_TOOLCHAIN       Rust version in the builder image (default 1.94.1)
set -euo pipefail

die() { echo "build-aarch64: error: $*" >&2; exit 1; }
log() { echo "build-aarch64: $*" >&2; }

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
[ -f "$REPO/Cargo.toml" ] && [ -f "$REPO/Cargo.lock" ] || die "$REPO is not the nvstor repository (no Cargo.toml/Cargo.lock)"
OUTDIR=${OUTDIR:-$REPO/target/aarch64-unknown-linux-gnu/release}
CACHE=${NVSTOR_CROSS_CACHE:-$REPO/target/cross-aarch64}
RUST_TOOLCHAIN=${RUST_TOOLCHAIN:-1.94.1}
[[ $RUST_TOOLCHAIN =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "RUST_TOOLCHAIN '$RUST_TOOLCHAIN' is not a version like 1.94.1"
IMAGE=nvstor-aarch64-builder:rust-$RUST_TOOLCHAIN
BINS=("$@")
[ ${#BINS[@]} -gt 0 ] || BINS=(nvstor-nfs-export)

# --- prerequisites -------------------------------------------------------------
[ "$(uname -s)" = Linux ] || die "run this on a Linux host (found $(uname -s))"
case "$(uname -m)" in
  x86_64) ;;
  aarch64|arm64)
    die "this host is already aarch64: build natively instead (needs libibverbs-dev, librdmacm-dev, clang, cmake):
  cargo build --release --locked ${BINS[*]/#/--bin }" ;;
  *) die "unsupported host architecture $(uname -m); this script cross-builds from x86_64" ;;
esac
if [ -z "${DOCKER:-}" ]; then
  if command -v docker >/dev/null; then DOCKER=docker
  elif command -v podman >/dev/null; then DOCKER=podman
  else die "neither docker nor podman is installed (or set DOCKER)"; fi
fi
command -v "$DOCKER" >/dev/null || die "DOCKER=$DOCKER is not on PATH"
"$DOCKER" info >/dev/null 2>&1 \
  || die "'$DOCKER info' failed: is the daemon running, and may $(id -un) use it (docker group)?"
command -v file >/dev/null || die "'file' is not installed (needed to check the output architecture)"
for b in "${BINS[@]}"; do
  [[ $b =~ ^[A-Za-z0-9_-]+$ ]] || die "'$b' is not a binary name"
  grep -rqs --include=Cargo.toml -E "^name = \"$b\"" "$REPO/crates" \
    || die "no [[bin]] or package named '$b' under crates/"
done

# --- builder image --------------------------------------------------------------
# Rust lives in /opt so the build can run as the calling user (no root-owned
# files in the cache or the output directory).
log "building image $IMAGE (cached after the first run)"
"$DOCKER" build -t "$IMAGE" --build-arg "RUST_TOOLCHAIN=$RUST_TOOLCHAIN" - <<'DOCKERFILE' \
  || die "image build failed (it needs network access to archive.ubuntu.com, ports.ubuntu.com and sh.rustup.rs)"
FROM ubuntu:24.04
ARG RUST_TOOLCHAIN
ENV DEBIAN_FRONTEND=noninteractive
RUN sed -i 's/^Types: deb$/Types: deb\nArchitectures: amd64/' /etc/apt/sources.list.d/ubuntu.sources \
 && printf 'Types: deb\nURIs: http://ports.ubuntu.com/ubuntu-ports\nSuites: noble noble-updates noble-security\nComponents: main universe\nArchitectures: arm64\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n' \
      > /etc/apt/sources.list.d/ports.sources \
 && dpkg --add-architecture arm64 && apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl build-essential pkg-config cmake clang libclang-dev \
      gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross \
      libibverbs-dev:arm64 librdmacm-dev:arm64 \
 && rm -rf /var/lib/apt/lists/*
ENV RUSTUP_HOME=/opt/rustup CARGO_HOME=/opt/cargo PATH=/opt/cargo/bin:$PATH
RUN curl -sSf https://sh.rustup.rs | sh -s -- -y --no-modify-path --profile minimal \
      --default-toolchain "$RUST_TOOLCHAIN" --target aarch64-unknown-linux-gnu \
 && chmod -R a+rX /opt/rustup /opt/cargo
ENV CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc \
    CXX_aarch64_unknown_linux_gnu=aarch64-linux-gnu-g++ \
    AR_aarch64_unknown_linux_gnu=aarch64-linux-gnu-ar \
    CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc \
    PKG_CONFIG_ALLOW_CROSS=1 \
    PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig \
    BINDGEN_EXTRA_CLANG_ARGS_aarch64_unknown_linux_gnu=--sysroot=/usr/aarch64-linux-gnu
WORKDIR /src
DOCKERFILE

# --- build ------------------------------------------------------------------------
mkdir -p "$CACHE/target" "$CACHE/cargo-home" "$OUTDIR" || die "cannot create $CACHE or $OUTDIR"
# The commit nvstor-peer --version reports: the container sees the
# checkout without a usable .git (a worktree's .git names a host path).
# -dirty when the build inputs have changes, untracked files included,
# or when git cannot say (a failing status counts as dirty). Outside git
# nothing is passed and the binary says "unknown".
COMMIT_ENV=()
if C=$(git -C "$REPO" rev-parse HEAD 2>/dev/null); then
  if ! S=$(git -C "$REPO" status --porcelain -- Cargo.toml Cargo.lock crates bins .cargo 'rust-toolchain*' 2>/dev/null) \
     || [ -n "$S" ]; then
    C="$C-dirty"
  fi
  COMMIT_ENV=(-e "NVSTOR_PEER_COMMIT=$C")
fi
log "commit ${C:-unknown}"
args=()
for b in "${BINS[@]}"; do args+=(--bin "$b"); done
log "cargo build --release --locked --target aarch64-unknown-linux-gnu ${args[*]}"
# CARGO_HOME is the cache (registry, git); cargo and rustc come from /opt.
"$DOCKER" run --rm \
  --user "$(id -u):$(id -g)" \
  -v "$REPO":/src:ro \
  -v "$CACHE/target":/target \
  -v "$CACHE/cargo-home":/cargo-home \
  -e HOME=/tmp -e CARGO_HOME=/cargo-home -e CARGO_TARGET_DIR=/target \
  ${COMMIT_ENV[@]+"${COMMIT_ENV[@]}"} \
  "$IMAGE" \
  /opt/cargo/bin/cargo build --release --locked --target aarch64-unknown-linux-gnu "${args[@]}" \
  || die "cargo build failed (output above)"

for b in "${BINS[@]}"; do
  src=$CACHE/target/aarch64-unknown-linux-gnu/release/$b
  [ -f "$src" ] || die "cargo reported success but $src is missing"
  file -b "$src" | grep -q "ARM aarch64" || die "$src is not an aarch64 binary: $(file -b "$src")"
  { cp "$src" "$OUTDIR/$b.tmp" && mv "$OUTDIR/$b.tmp" "$OUTDIR/$b"; } || die "cannot copy $b to $OUTDIR"
  log "$OUTDIR/$b"
done
