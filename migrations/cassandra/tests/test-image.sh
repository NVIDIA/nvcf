#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

image=${1:?Usage: test-image.sh IMAGE}
subtree_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

# Exercise the shipped tools as the image's default unprivileged user.
docker run --rm --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" -gt 0
  test "$(id -g)" -gt 0
  command -v timeout envsubst python3 cqlsh migrate
  cqlsh --version
  migrate -version
  python3 -c "from urllib.parse import quote; assert quote(\"p@ss\", safe=\"\") == \"p%40ss\""
  test ! -e /usr/local/bin/gosu
'

# Check the complete overlay inside the built image against its source lock.
sed '/^#/d; /^[[:space:]]*$/d' "$subtree_dir/java-libraries.lock" |
  while read -r checksum url; do
    printf '%s  /opt/cassandra/lib/%s\n' "$checksum" "${url##*/}"
  done |
  docker run --rm -i --entrypoint sha256sum "$image" -c -
