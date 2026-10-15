#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Download every artifact in a java-libraries.lock into a directory, through
# the repository the build selects, verifying each SHA-256.
#
# The lock pins public Maven Central URLs so the source stays self-contained.
# A release build can point MAVEN_REPOSITORY_BASE at a caching repository
# manager instead; the artifact path and checksum are kept and only the base
# is swapped. An entry outside the public base fails the build rather than
# bypassing the selected repository.
#
# usage: fetch-java-libraries <lock-file> <destination-dir>
set -eu

[ "$#" -eq 2 ] || {
    echo "usage: $0 <lock-file> <destination-dir>" >&2
    exit 2
}
lock=$1
destination_dir=$2

public_base=https://repo.maven.apache.org/maven2
base=${MAVEN_REPOSITORY_BASE:-${public_base}}
base=${base%/}

fetch_verified=$(dirname -- "$0")/fetch-verified
[ -x "${fetch_verified}" ] || fetch_verified=$(dirname -- "$0")/fetch-verified.sh
[ -x "${fetch_verified}" ] || {
    echo "missing required command: fetch-verified" >&2
    exit 1
}

mkdir -p "${destination_dir}"
while read -r checksum url; do
    case "${checksum}" in \#*|'') continue ;; esac
    case "${url}" in
        "${public_base}"/*) ;;
        *)
            echo "lock entry is not under ${public_base}, refusing to fetch it: ${url}" >&2
            exit 1
            ;;
    esac
    "${fetch_verified}" "${checksum}" "${base}/${url#"${public_base}"/}" "${destination_dir}/${url##*/}"
done < "${lock}"
