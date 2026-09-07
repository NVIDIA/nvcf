#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "${work_dir}"' EXIT HUP INT TERM

input_dir=${work_dir}/input
mkdir -p \
    "${input_dir}/META-INF/maven/com.zegelin.cassandra-exporter/agent" \
    "${input_dir}/META-INF/maven/com.zegelin.cassandra-exporter/common" \
    "${input_dir}/com/zegelin/cassandra/exporter" \
    "${input_dir}/io/netty/legacy"

printf '%s\n' \
    'Manifest-Version: 1.0' \
    'Premain-Class: com.zegelin.cassandra.exporter.Agent' \
    > "${input_dir}/META-INF/MANIFEST.MF"
printf '%s\n' 'preserve this exporter payload' \
    > "${input_dir}/com/zegelin/cassandra/exporter/Agent.class"
printf '%s\n' 'remove this old Netty payload' \
    > "${input_dir}/io/netty/legacy/Old.class"

for project in agent common; do
    printf '%s\n' \
        '<project>' \
        '  <dependency>' \
        '    <groupId>io.netty</groupId>' \
        '    <version>4.1.135.Final</version>' \
        '  </dependency>' \
        '</project>' \
        > "${input_dir}/META-INF/maven/com.zegelin.cassandra-exporter/${project}/pom.xml"
done

for artifact in \
    netty-buffer \
    netty-codec \
    netty-codec-http \
    netty-common \
    netty-handler \
    netty-resolver \
    netty-transport \
    netty-transport-native-unix-common; do
    metadata_dir=${input_dir}/META-INF/maven/io.netty/${artifact}
    mkdir -p "${metadata_dir}"
    printf '%s\n' "artifactId=${artifact}" 'groupId=io.netty' 'version=4.1.135.Final' \
        > "${metadata_dir}/pom.properties"
done

input_jar=${work_dir}/input.jar
output_jar=${work_dir}/output.jar
(
    cd "${input_dir}"
    find . -type f -print | LC_ALL=C sort | zip -Xq "${input_jar}" -@
)

"${script_dir}/repack-exporter-netty.sh" "${input_jar}" "${output_jar}"

original_payload=$(unzip -p "${input_jar}" com/zegelin/cassandra/exporter/Agent.class)
repacked_payload=$(unzip -p "${output_jar}" com/zegelin/cassandra/exporter/Agent.class)
[ "${original_payload}" = "${repacked_payload}" ]

original_manifest=$(unzip -p "${input_jar}" META-INF/MANIFEST.MF)
repacked_manifest=$(unzip -p "${output_jar}" META-INF/MANIFEST.MF)
[ "${original_manifest}" = "${repacked_manifest}" ]

if unzip -Z1 "${output_jar}" | grep -q '^io/netty/legacy/Old.class$'; then
    echo 'old Netty class remains in the repacked jar' >&2
    exit 1
fi
unzip -Z1 "${output_jar}" | grep -q '^io/netty/handler/codec/http/HttpRequest.class$'

module_count=$(unzip -Z1 "${output_jar}" |
    sed -n 's#^META-INF/maven/io\.netty/[^/]*/pom\.properties$#module#p' |
    wc -l | tr -d ' ')
[ "${module_count}" = 8 ]

for properties in $(unzip -Z1 "${output_jar}" | grep '^META-INF/maven/io.netty/.*/pom.properties$'); do
    [ "$(unzip -p "${output_jar}" "${properties}" | sed -n 's/^version=//p')" = '4.1.137.Final' ]
done

if unzip -p "${output_jar}" | grep -q '4.1.135.Final'; then
    echo 'old Netty version remains in the repacked jar' >&2
    exit 1
fi
echo 'repack-exporter-netty: PASS'
