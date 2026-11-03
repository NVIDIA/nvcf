#!/usr/bin/env bash
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
#
# Unpack the rdma-core files nvstor links against (libibverbs: the headers
# and the shared library) for one Debian architecture, without root, from
# pinned Ubuntu 24.04 packages checked by SHA-256.
#
#   scripts/fetch-rdma-core.sh ARCH DEST     ARCH: amd64 or arm64
#
# Then build with RDMA_CORE_PREFIX=DEST/usr: nvstor-rdma's build.rs searches
# DEST/usr/lib/<target arch>-linux-gnu. With Bazel, pass it to the build
# script as --action_env=RDMA_CORE_PREFIX=DEST/usr; this is how an arm64
# binary is cross-built on an x86_64 host:
#
#   scripts/fetch-rdma-core.sh arm64 /tmp/rdma-arm64
#   bazel build --platforms=//platforms:linux_arm64 \
#     --action_env=RDMA_CORE_PREFIX=/tmp/rdma-arm64/usr \
#     //src/compute-plane-services/nvstor/crates/nvstor-peer:nvstor-peer
#
# The release pocket's packages stay in the archive's pool for the life of
# the release, so these URLs do not go away when an update is published.
set -euo pipefail

VERSION=50.0-2build2
declare -A SHA256=(
    [libibverbs-dev_amd64]=3a327a9d984f0acd6aee20f7b48b2d647322e2afaf370436af9502cf393085dc
    [libibverbs1_amd64]=731e9d88e498b1a8b03a4eaf6e3ed6227f1c4646b9931256f7e234b42ae618dd
    [libibverbs-dev_arm64]=ad765b2f80e19a9a5839d2840e40cd0e68d02199e1292de62bf495afd4c68d66
    [libibverbs1_arm64]=d5cf6b37c02ccc9609f96ac46b3a823ba087088e98a5f17ff823156c9a765eb0
)

die() { echo "fetch-rdma-core: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: $0 amd64|arm64 DEST"
arch=$1
dest=$2
case "$arch" in
    amd64) mirror=http://archive.ubuntu.com/ubuntu ;;
    arm64) mirror=http://ports.ubuntu.com/ubuntu-ports ;;
    *) die "unsupported architecture '$arch' (amd64 or arm64)" ;;
esac
command -v dpkg-deb >/dev/null || die "dpkg-deb is required"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$dest"
for pkg in libibverbs-dev libibverbs1; do
    deb="${pkg}_${VERSION}_${arch}.deb"
    curl -fsSL --retry 3 -o "$work/$deb" "$mirror/pool/main/r/rdma-core/$deb" \
        || die "download of $deb failed"
    echo "${SHA256[${pkg}_${arch}]}  $work/$deb" | sha256sum -c --quiet - \
        || die "$deb does not match its pinned SHA-256"
    dpkg-deb -x "$work/$deb" "$dest"
done
echo "rdma-core $VERSION ($arch) in $dest/usr"
