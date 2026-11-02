#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

binary=${KUBECTL_BINARY:?KUBECTL_BINARY must be set}
version=${KUBECTL_VERSION:?KUBECTL_VERSION must be set}
metadata=$("$binary" version --client -o json)
release=${version#v}
major=${release%%.*}
minor_patch=${release#*.}
minor=${minor_patch%%.*}
for expected in \
  "\"major\": \"${major}\"" \
  "\"minor\": \"${minor}\"" \
  "\"gitVersion\": \"${version}\"" \
  "\"gitCommit\": \"${KUBECTL_SOURCE_COMMIT:?}\"" \
  '"gitTreeState": "dirty"' \
  "\"buildDate\": \"${KUBECTL_BUILD_DATE:?}\"" \
  "\"goVersion\": \"${KUBECTL_GO_VERSION:?}\"" \
  "\"platform\": \"linux/${TARGETARCH:?}\""; do
  printf '%s\n' "$metadata" | grep -Fq "$expected" || {
    echo "$binary: missing expected identity $expected" >&2
    exit 1
  }
done
printf '%s\n' "$metadata"
