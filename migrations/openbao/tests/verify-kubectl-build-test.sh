#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
openbao_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
dockerfile="$openbao_dir/Dockerfile"
build_script="$openbao_dir/scripts/build-kubectl.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

grep -Eq '^ARG KUBECTL_GO_IMAGE=golang:[0-9.]+-alpine[0-9.]+@sha256:[0-9a-f]{64}$' "$dockerfile" || \
  fail "kubectl builder must pin a multi-architecture Go manifest"
grep -Eq '^ARG KUBECTL_VERSION=v1\.36\.[0-9]+$' "$dockerfile" || \
  fail "kubectl must stay on the Kubernetes 1.36 compatibility line"
grep -Eq '^ARG KUBECTL_SOURCE_SHA256=[0-9a-f]{64}$' "$dockerfile" || \
  fail "kubectl source archive checksum is not pinned"
grep -Fq 'GOPROXY=off' "$build_script" || \
  fail "kubectl compilation must use the verified vendor tree offline"
grep -Fq -- '-mod=vendor' "$build_script" || \
  fail "kubectl build must use the release archive's vendored dependencies"
if TARGETARCH=ppc64le "$build_script" >/dev/null 2>&1; then
  fail "kubectl build accepted an unsupported architecture"
fi

archive=$(mktemp)
trap 'rm -f "$archive"' EXIT INT TERM
printf 'not Kubernetes source\n' >"$archive"
if TARGETARCH=amd64 \
  KUBECTL_SOURCE_URL="file://$archive" \
  KUBECTL_SOURCE_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
  "$build_script" >/dev/null 2>&1; then
  fail "kubectl build accepted a source archive with the wrong checksum"
fi

echo "kubectl source-build contract verified"
