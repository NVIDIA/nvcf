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

grep -Fq \
  'ARG KUBECTL_GO_IMAGE=golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406' \
  "$dockerfile" || fail "kubectl builder must pin the reviewed Go 1.26.6 multi-architecture manifest"
grep -Fq 'ARG KUBECTL_VERSION=v1.36.4' "$dockerfile" || \
  fail "kubectl must stay on the Kubernetes 1.36 compatibility line"
grep -Fq \
  'ARG KUBECTL_SOURCE_SHA256=3c28f11492472df48e658551bf268fd92938b127b0f9dcef7090ac800318c821' \
  "$dockerfile" || fail "kubectl source archive checksum is not pinned"
grep -Fq 'GOPROXY=off' "$build_script" || \
  fail "kubectl build must not resolve dependencies outside the source archive"
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
