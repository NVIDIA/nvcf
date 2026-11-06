#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

image=${1:?Usage: test-image.sh IMAGE}
subtree_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
expected_version=$(sed -n 's/^FROM cassandra:\([^@[:space:]]*\)@sha256:[[:xdigit:]]\{64\}$/\1/p' "$subtree_dir/Dockerfile")
test -n "$expected_version" || { echo 'missing pinned Cassandra base image' >&2; exit 1; }

actual_version=$(docker run --rm --entrypoint cassandra "$image" -v | tail -n 1)
test "$actual_version" = "$expected_version" || {
  echo "Cassandra version mismatch: Dockerfile expects $expected_version, image has $actual_version" >&2
  exit 1
}
docker run --rm -i --entrypoint cmp "$image" - /usr/share/doc/cacerts-NOTICE < "$subtree_dir/NOTICE"

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
  # ca-certificates 20250419+deb13u1 distrusts these roots.
  for root in emSign_ECC_Root_CA_-_G3 Comodo_AAA_Services_root; do
    test ! -e "/usr/share/ca-certificates/mozilla/$root.crt"
    test ! -e "/etc/ssl/certs/$root.pem"
  done
'

java_version=$(docker run --rm --entrypoint java "$image" -version 2>&1)
printf '%s\n' "$java_version" | grep -E '^openjdk version "17\.'

certs=$(docker run --rm --entrypoint keytool "$image" -list -v -cacerts -storepass changeit)
count=$(printf '%s\n' "$certs" | grep -c 'Entry type: trustedCertEntry')
test "$count" -ge 100
for fingerprint in \
  '86:A1:EC:BA:08:9C:4A:8D:3B:BE:27:34:C6:12:BA:34:1D:81:3E:04:3C:F9:E8:A8:62:CD:5C:57:A3:6B:BE:6B' \
  'D7:A7:A0:FB:5D:7E:27:31:D7:71:E9:48:4E:BC:DE:F7:1D:5F:0C:3E:0A:29:48:78:2B:C8:3E:E0:EA:69:9E:F4'; do
  if printf '%s\n' "$certs" | grep -Fq "$fingerprint"; then
    echo "affected root remains in JDK cacerts: $fingerprint" >&2
    exit 1
  fi
done

# Check the complete overlay inside the built image against its source lock.
sed '/^#/d; /^[[:space:]]*$/d' "$subtree_dir/java-libraries.lock" |
  while read -r checksum url; do
    printf '%s  /opt/cassandra/lib/%s\n' "$checksum" "${url##*/}"
  done |
  docker run --rm -i --entrypoint sha256sum "$image" -c -
