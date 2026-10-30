#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu
cd "$(dirname "$0")"
script="$PWD/scripts/build-kubectl.sh"
if TARGETARCH=ppc64le "$script" >/dev/null 2>&1; then
    echo 'kubectl build accepted an unsupported architecture' >&2
    exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'invalid source archive\n' > "$work/source.tar.gz"
if TARGETARCH=amd64 OUTPUT_DIR="$work/out" \
    KUBECTL_SOURCE_URL="file://$work/source.tar.gz" \
    KUBECTL_SOURCE_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
    "$script" >"$work/build.log" 2>&1; then
    echo 'kubectl build accepted an invalid source checksum' >&2
    exit 1
fi
grep -q 'FAILED' "$work/build.log"
test ! -e "$work/out/kubectl"
echo 'PASS: kubectl build rejects unsupported targets and invalid source bytes'
