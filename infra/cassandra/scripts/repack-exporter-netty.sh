#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

NETTY_VERSION=4.1.137.Final
MAVEN_CENTRAL_URL=https://repo1.maven.org/maven2/io/netty
NETTY_ARTIFACTS='netty-buffer f474b14c7734f15e0540394cb6f39d67777b7581a42919e4ac89d253d4efd929
netty-codec 9987b6a660b0a6b1f0d791485dae33180b3d1c63687c006fe6d3fd025e9e3798
netty-codec-http 0535bb5a736472bef5c948d15eb273c4ab9f796656fc7c5d6b982ad92bddbd49
netty-common d31926b01adcc07af86f5e27b81b6d6c115df17d366e835d1fc3f5a1924e7e52
netty-handler d0e4c6ee4779f59f6ab2fb5d388e4f57147c82270164b37945764bb9bda96a44
netty-resolver b4cf2aeedd9fc7c8c439bbfe574f63cfe5b83392e88bbc07ca0e8424b7cff955
netty-transport 6251adc2a2921572382732a2db188d4f4f2251fd6ebb49c5d44bbf33d6bfb1a7
netty-transport-native-unix-common 8056e7637f9948f953314894cf995ab664711b66c73c4cd5b593ae84f0b1048c'

usage() {
    echo "usage: $0 <input-exporter.jar> <output-exporter.jar>" >&2
    exit 2
}

[ "$#" -eq 2 ] || usage
input=$1
output=$2

for tool in awk basename cp curl dirname find grep mkdir mktemp mv rm sed sha256sum sort touch unzip zip; do
    command -v "${tool}" >/dev/null 2>&1 || {
        echo "missing required command: ${tool}" >&2
        exit 1
    }
done
# Sibling of this script both in the repository and under /usr/local/bin in
# the image, so the download policy lives in one place.
fetch_verified=$(dirname -- "$0")/fetch-verified
[ -x "${fetch_verified}" ] || fetch_verified=$(dirname -- "$0")/fetch-verified.sh
[ -x "${fetch_verified}" ] || {
    echo "missing required command: fetch-verified" >&2
    exit 1
}

[ -f "${input}" ] || {
    echo "exporter input does not exist: ${input}" >&2
    exit 1
}

# The public build uses an empty placeholder when no exporter is supplied.
if [ ! -s "${input}" ]; then
    cp "${input}" "${output}"
    exit 0
fi

unzip -tq "${input}" >/dev/null

expected_modules=$(printf '%s\n' "${NETTY_ARTIFACTS}" | awk 'NF { print $1 }' | sort)
input_modules=$(
    unzip -Z1 "${input}" |
        sed -n 's#^META-INF/maven/io\.netty/\([^/]*\)/pom\.properties$#\1#p' |
        sort -u
)
[ "${input_modules}" = "${expected_modules}" ] || {
    echo "exporter Netty module set changed; refusing a partial replacement" >&2
    echo "expected modules:" >&2
    printf '%s\n' "${expected_modules}" >&2
    echo "input modules:" >&2
    printf '%s\n' "${input_modules}" >&2
    exit 1
}

work_dir=$(mktemp -d)
trap 'rm -rf "${work_dir}"' EXIT HUP INT TERM
payload_dir=${work_dir}/payload
downloads_dir=${work_dir}/downloads
mkdir -p "${payload_dir}" "${downloads_dir}"
unzip -q "${input}" -d "${payload_dir}"

exporter_metadata_dir=${payload_dir}/META-INF/maven/com.zegelin.cassandra-exporter
[ -d "${exporter_metadata_dir}" ] || {
    echo "exporter Maven metadata is missing: META-INF/maven/com.zegelin.cassandra-exporter" >&2
    exit 1
}
exporter_poms=$(find "${exporter_metadata_dir}" -name pom.xml -type f -print)
[ -n "${exporter_poms}" ] || {
    echo "exporter Maven metadata contains no pom.xml files" >&2
    exit 1
}

# Replacing shaded classes invalidates any signatures covering the input JAR.
# Remove top-level signature metadata so the JVM does not reject the repacked
# agent with a SecurityException when it reads modified entries.
find "${payload_dir}/META-INF" -maxdepth 1 -type f \
    \( -name '*.SF' -o -name '*.RSA' -o -name '*.DSA' -o -name '*.EC' -o -name 'SIG-*' \) \
    -exec rm -f {} +

# Remove the complete shaded Netty payload and its dependency metadata. The
# module-set check above makes this fail closed if the exporter gains another
# Netty module that is not pinned here.
rm -rf \
    "${payload_dir}/io/netty" \
    "${payload_dir}/META-INF/maven/io.netty" \
    "${payload_dir}/META-INF/native-image/io.netty"
rm -f \
    "${payload_dir}/META-INF/io.netty.versions.properties" \
    "${payload_dir}/META-INF/services/reactor.blockhound.integration.BlockHoundIntegration"

printf '%s\n' "${NETTY_ARTIFACTS}" |
while read -r artifact checksum; do
    [ -n "${artifact}" ] || continue
    jar=${downloads_dir}/${artifact}-${NETTY_VERSION}.jar
    "${fetch_verified}" "${checksum}" \
        "${MAVEN_CENTRAL_URL}/${artifact}/${NETTY_VERSION}/${artifact}-${NETTY_VERSION}.jar" \
        "${jar}"

    # Only copy Netty-owned entries. This keeps the exporter implementation,
    # manifest, and all unrelated shaded dependencies byte-for-byte intact.
    unzip -oq "${jar}" \
        'io/netty/*' \
        'META-INF/maven/io.netty/*' \
        -d "${payload_dir}"
    if unzip -Z1 "${jar}" | grep -q '^META-INF/native-image/io.netty/'; then
        unzip -oq "${jar}" 'META-INF/native-image/io.netty/*' -d "${payload_dir}"
    fi
    if unzip -Z1 "${jar}" |
        grep -q '^META-INF/services/reactor.blockhound.integration.BlockHoundIntegration$'; then
        unzip -oq "${jar}" \
            'META-INF/services/reactor.blockhound.integration.BlockHoundIntegration' \
            -d "${payload_dir}"
    fi
done

# The exporter POMs are scanner-visible provenance. Update only their Netty
# version declarations so they describe the dependency payload just inserted.
printf '%s\n' "${exporter_poms}" |
while read -r pom; do
    if ! awk -v version="${NETTY_VERSION}" '
        /<dependency>/ { in_dependency = 1; dependency = "" }
        in_dependency {
            dependency = dependency $0 ORS
            if (/<\/dependency>/) {
                if (dependency ~ /<groupId>io[.]netty<\/groupId>/) {
                    if (dependency ~ /<version>4[.]1[.][0-9][0-9]*[.]Final<\/version>/) {
                        sub(/<version>4[.]1[.][0-9][0-9]*[.]Final<\/version>/,
                            "<version>" version "</version>", dependency)
                        changed = 1
                    }
                }
                printf "%s", dependency
                in_dependency = 0
                next
            }
            next
        }
        { print }
        END {
            if (in_dependency || !changed) {
                exit 2
            }
        }
    ' "${pom}" > "${pom}.updated"; then
        rm -f "${pom}.updated"
        echo "unexpected exporter POM dependency layout: ${pom}" >&2
        exit 1
    fi
    mv "${pom}.updated" "${pom}"
done

output_modules=$(
    find "${payload_dir}/META-INF/maven/io.netty" -name pom.properties -type f |
        sed 's#.*/META-INF/maven/io\.netty/\([^/]*\)/pom\.properties$#\1#' |
        sort -u
)
[ "${output_modules}" = "${expected_modules}" ] || {
    echo "replacement Netty module set is incomplete" >&2
    exit 1
}

for properties in "${payload_dir}"/META-INF/maven/io.netty/*/pom.properties; do
    grep_version=$(sed -n 's/^version=//p' "${properties}")
    [ "${grep_version}" = "${NETTY_VERSION}" ] || {
        echo "unexpected Netty version in ${properties}: ${grep_version}" >&2
        exit 1
    }
done

# Netty's per-module files share this name. Combine them so Version.identify()
# can report every module from the repacked fat jar.
versions_file=${payload_dir}/META-INF/io.netty.versions.properties
: > "${versions_file}"
printf '%s\n' "${NETTY_ARTIFACTS}" |
while read -r artifact checksum; do
    [ -n "${artifact}" ] || continue
    unzip -p "${downloads_dir}/${artifact}-${NETTY_VERSION}.jar" \
        META-INF/io.netty.versions.properties >> "${versions_file}"
done

mkdir -p "$(dirname "${output}")"
output_abs=$(cd "$(dirname "${output}")" && pwd)/$(basename "${output}")
rm -f "${output_abs}"
# ZIP records DOS timestamps. Normalize them so identical inputs and pinned
# dependencies produce an identical repacked jar on every build.
find "${payload_dir}" -type f -exec touch -t 198001010000 {} +
(
    cd "${payload_dir}"
    find . -type f -print | LC_ALL=C sort | zip -Xq "${output_abs}" -@
)
unzip -tq "${output}" >/dev/null
