#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

image=${1:?Usage: test-image.sh IMAGE}
subtree_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

# Exercise the shipped tools as the image's default unprivileged user.
docker run --rm --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" -eq 999
  test "$(id -g)" -eq 999
  for tool in timeout envsubst python3 cqlsh migrate; do
    command -v "$tool"
  done
  cqlsh --version
  migrate -version
  python3 -c "from urllib.parse import quote; assert quote(\"p@ss\", safe=\"\") == \"p%40ss\""
  test ! -e /usr/local/bin/gosu
  dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' perl-base)" ge 5.40.1-6+deb13u1
  dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' libpcre2-8-0)" ge 10.46-1~deb13u2
  for package in python3.13 python3.13-minimal libpython3.13-minimal libpython3.13-stdlib; do
    dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' "$package")" ge 3.13.5-2+deb13u5
  done
'

sh "$subtree_dir/../../infra/cassandra/scripts/test-java-libraries.sh" \
  "$image" "$subtree_dir/java-libraries.lock"
