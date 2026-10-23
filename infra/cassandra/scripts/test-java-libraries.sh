#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

image=${1:?Usage: test-java-libraries.sh IMAGE LOCK}
lock=${2:?Usage: test-java-libraries.sh IMAGE LOCK}
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

# Verify bytes and reject older copies that would remain on the classpath.
sed '/^#/d; /^[[:space:]]*$/d' "$lock" |
  while read -r checksum url; do
    printf '%s  /opt/cassandra/lib/%s\n' "$checksum" "${url##*/}"
  done > "$test_dir/checksums"
docker run --rm -i --entrypoint sha256sum "$image" -c - < "$test_dir/checksums"
docker run --rm -i --entrypoint python3 "$image" -c '
import pathlib, sys
expected = {pathlib.Path(line.split()[1]).name for line in sys.stdin}
for family in ("jackson-annotations", "jackson-core", "jackson-databind",
               "jackson-datatype-jsr310", "logback-core", "logback-classic", "lz4-java"):
    actual = {p.name for p in pathlib.Path("/opt/cassandra/lib").glob(family + "-*.jar")}
    locked = {name for name in expected if name.startswith(family + "-")}
    assert len(locked) == 1 and actual == locked, (family, actual, locked)
' < "$test_dir/checksums"

# Compile against the shipped libraries; execute against the image JRE and JNI.
mkdir "$test_dir/lib" "$test_dir/classes"
docker run --rm --entrypoint tar "$image" -C /opt/cassandra/lib -cf - . > "$test_dir/lib.tar"
tar -xf "$test_dir/lib.tar" -C "$test_dir/lib"
find "$test_dir/lib" -type f -size 0 -delete
javac -J-XX:ActiveProcessorCount=2 --release 11 -cp "$test_dir/lib/*" -d "$test_dir/classes" "$script_dir/RuntimeLibrariesSmoke.java"
for config in logback.xml logback-tools.xml; do
  docker run --rm --entrypoint java \
    --mount "type=bind,src=$test_dir/classes,dst=/tmp/smoke,readonly" \
    "$image" -XX:ActiveProcessorCount=2 -Xmx128m -Dcassandra.logdir=/tmp/logs \
    "-Dlogback.configurationFile=/etc/cassandra/$config" \
    -cp '/tmp/smoke:/opt/cassandra/lib/*' RuntimeLibrariesSmoke
done
