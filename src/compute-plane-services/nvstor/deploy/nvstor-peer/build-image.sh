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

# Build the nvstor-peer runtime image for aarch64 from this checkout's
# HEAD, and optionally push it.
#
#   deploy/nvstor-peer/build-image.sh REGISTRY/REPO [--push]
#
# The binary is cross-built by deploy/nfs-rdma/build-aarch64.sh (Docker, no
# emulation); the image is deploy/nvstor-peer/Dockerfile. Refuses a tree
# with uncommitted or untracked changes to anything that goes into the
# image, so the org.opencontainers.image.revision label names exactly what
# is in it. --push also requires HEAD to be on origin/main (published
# nvstor-peer only) and refuses to overwrite a tag that already exists.
# The image is tagged REGISTRY/REPO/nvstor-peer:<version>-<commit12>.
# Before any push the exported image is checked: merged-/usr symlinks,
# the binary's interpreter, and every shared library the binary,
# libibverbs, librdmacm, the mlx5 provider and ip link.
# Prints the tag, commit, binary sha256 and (after --push) the digest.
# Registry credentials come from the caller's Docker config.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$HERE/../.." && pwd)
[ $# -ge 1 ] || { sed -n '5p' "$0" | sed 's/^# //' >&2; exit 2; }
DEST=${1%/}; PUSH=0
[ "${2:-}" = --push ] && PUSH=1
cd "$REPO_ROOT"
INPUTS=(crates bins Cargo.toml Cargo.lock deploy/nvstor-peer deploy/nfs-rdma/build-aarch64.sh)
if ! git diff --quiet HEAD -- "${INPUTS[@]}" \
   || [ -n "$(git status --porcelain --untracked-files=normal -- "${INPUTS[@]}")" ]; then
  echo "uncommitted or untracked changes in: ${INPUTS[*]}; commit them first" >&2
  exit 1
fi
COMMIT=$(git rev-parse HEAD)
if [ "$PUSH" = 1 ]; then
  git fetch -q origin main
  git merge-base --is-ancestor HEAD origin/main \
    || { echo "HEAD $COMMIT is not on origin/main; push it first" >&2; exit 1; }
fi
# The crate takes its version from the workspace.
VERSION=$(sed -n '/^\[workspace.package\]/,/^\[/s/^version *= *"\(.*\)"/\1/p' Cargo.toml | head -1)
[ -n "$VERSION" ] || { echo "cannot read the nvstor-peer version" >&2; exit 1; }
TAG="$DEST/nvstor-peer:$VERSION-${COMMIT:0:12}"
if [ "$PUSH" = 1 ] && docker buildx imagetools inspect "$TAG" >/dev/null 2>&1; then
  echo "$TAG already exists in the registry; not overwriting it" >&2
  exit 1
fi

CTX=$(mktemp -d)
trap 'rm -rf -- "$CTX"' EXIT
OUTDIR="$CTX/out" bash "$REPO_ROOT/deploy/nfs-rdma/build-aarch64.sh" nvstor-peer >/dev/null
cp "$CTX/out/nvstor-peer" "$HERE/Dockerfile" "$CTX/"
file "$CTX/nvstor-peer" | grep -q 'ARM aarch64' || { echo "binary is not aarch64" >&2; exit 1; }

docker buildx build --builder default --platform linux/arm64 \
  --build-arg SOURCE_COMMIT="$COMMIT" --build-arg VERSION="$VERSION" \
  --provenance=false -t "$TAG" --load "$CTX"

# Check the image before anything leaves this host, from the exported
# filesystem (no aarch64 emulation needed).
ROOT="$CTX/root"; mkdir "$ROOT"
cid=$(docker create --platform linux/arm64 "$TAG" /usr/local/bin/nvstor-peer)
docker export "$cid" | tar -x -C "$ROOT"
docker rm "$cid" >/dev/null
libdir="$ROOT/usr/lib/aarch64-linux-gnu"
missing=0
fail() { echo "image check: $*" >&2; missing=1; }
for d in bin sbin lib; do [ -L "$ROOT/$d" ] || fail "/$d is no longer a symlink into /usr"; done
interp=$(readelf -l "$ROOT/usr/local/bin/nvstor-peer" | sed -n 's/.*Requesting program interpreter: \(.*\)\]/\1/p')
[ -e "$ROOT/usr${interp#/usr}" ] || [ -e "$libdir/${interp##*/}" ] || fail "interpreter $interp missing"
for f in "$ROOT/usr/local/bin/nvstor-peer" "$libdir/libibverbs.so.1" "$libdir/librdmacm.so.1" \
         "$libdir/libibverbs/libmlx5-rdmav34.so" "$ROOT/usr/bin/ip"; do
  [ -e "$f" ] || { fail "${f#"$ROOT"} missing"; continue; }
  for n in $(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do
    [ -e "$libdir/$n" ] || fail "${f#"$ROOT"} needs $n, not in the image"
  done
done
[ -e "$ROOT/etc/libibverbs.d/mlx5.driver" ] || fail "no mlx5 provider config"
grep -q '^rdma:' "$ROOT/etc/group" || fail "no rdma group"
[ -s "$ROOT/var/lib/dpkg/info/libibverbs1:arm64.list" ] || fail "dpkg file list for libibverbs1:arm64 missing"
[ "$missing" = 0 ] || { echo "image incomplete; not pushed" >&2; exit 1; }
echo "image checked: symlinks, interpreter, linked libraries, provider config, dpkg records"

echo "tag      $TAG"
echo "commit   $COMMIT"
echo "binary   $(sha256sum "$CTX/nvstor-peer" | cut -d' ' -f1)"
echo "packages $(tr '\n' ';' < "$ROOT/usr/share/nvstor-peer/packages.txt")"
if [ "$PUSH" = 1 ]; then
  docker push -q "$TAG" >/dev/null
  digest=$(docker buildx imagetools inspect "$TAG" --format '{{json .Manifest.Digest}}' | tr -d '"')
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "pushed, but cannot read the digest of $TAG" >&2; exit 1; }
  echo "digest   $digest"
  echo "pull     $DEST/nvstor-peer@$digest"
fi
