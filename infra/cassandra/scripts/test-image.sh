#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

image=${1:?Usage: test-image.sh IMAGE}
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
docker run --rm --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" -eq 999
  test "$(id -g)" -eq 999
  cqlsh --version
  yq --version
  dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' perl-base)" ge 5.40.1-6+deb13u1
  dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' libpcre2-8-0)" ge 10.46-1~deb13u2
  for package in python3.13 python3.13-minimal libpython3.13-minimal libpython3.13-stdlib; do
    dpkg --compare-versions "$(dpkg-query -W -f='"'"'${Version}'"'"' "$package")" ge 3.13.5-2+deb13u5
  done
'
docker run --rm --user 0:0 --entrypoint /bin/sh "$image" -ec '
  gosu --version
  test "$(gosu nobody id -u)" -eq 65534
  test "$(gosu 999:999 id -g)" -eq 999
'
sh "$script_dir/test-java-libraries.sh" "$image" "$script_dir/../java-libraries.lock"
