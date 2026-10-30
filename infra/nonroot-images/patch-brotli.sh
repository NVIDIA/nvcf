#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

branch="$1"
arch="$(apk --print-arch)"
package=brotli-libs-1.2.1-r0.apk
checksums="$(dirname "$0")/brotli-checksums.txt"
checksum="$(awk -v key="${branch}/${arch}/${package}" '$2 == key { print $1 }' "$checksums")"
if [ "${#checksum}" -ne 64 ]; then
    echo "No pinned Brotli package for ${branch}/${arch}" >&2
    exit 1
fi
# Keep each base's Alpine branch and all other packages unchanged. A missing
# pinned APK must fail the build instead of silently selecting different bytes.
curl --fail --silent --show-error --location --retry 3 \
    "https://dl-cdn.alpinelinux.org/alpine/${branch}/main/${arch}/${package}" \
    --output "/tmp/helper-patch/${package}"
printf '%s  %s\n' "$checksum" "/tmp/helper-patch/${package}" | sha256sum -c -
apk --no-network add --upgrade "/tmp/helper-patch/${package}"
apk info --installed 'brotli-libs=1.2.1-r0'
