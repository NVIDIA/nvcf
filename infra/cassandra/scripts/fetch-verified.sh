#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Download one artifact, retrying transient failures, and verify its SHA-256
# before it is allowed to exist at the destination.
#
# Maven Central answers 429 during release builds, and a single curl attempt
# turned that into a failed image build with valid inputs. curl retries the
# transient responses (408, 429, 5xx, timeouts) on its own once asked, backing
# off exponentially from one second and honouring Retry-After, so the loop
# lives in curl rather than here. A 404 or a checksum mismatch is not
# transient and fails at once. The checksum check is unchanged: a download
# that does not match is deleted and the build fails.
#
# usage: fetch-verified <sha256> <url> <destination>
#
# FETCH_RETRIES (default 8) is the total number of requests allowed, the
# first one included; curl counts retries after the first, so it is passed one
# fewer. FETCH_MAX_TIME (default 120,
# seconds) caps one attempt so a stalled transfer is cut off and retried rather
# than hanging the build, and FETCH_RETRY_MAX_TIME (default 600, seconds) caps
# the whole download. FETCH_RETRY_DELAY, when set, replaces the exponential
# backoff with a fixed delay; the tests use it.
set -eu

[ "$#" -eq 3 ] || {
    echo "usage: $0 <sha256> <url> <destination>" >&2
    exit 2
}
checksum=$1
url=$2
destination=$3

attempts=${FETCH_RETRIES:-8}
case "${attempts}" in
    ''|*[!0-9]*|0) echo "FETCH_RETRIES must be a whole number of at least 1, got '${attempts}'" >&2; exit 2 ;;
esac

rm -f "${destination}"
set -- --retry "$((attempts - 1))" \
       --max-time "${FETCH_MAX_TIME:-120}" \
       --retry-max-time "${FETCH_RETRY_MAX_TIME:-600}"
[ -z "${FETCH_RETRY_DELAY:-}" ] || set -- "$@" --retry-delay "${FETCH_RETRY_DELAY}"
if ! curl -fsSL "$@" -o "${destination}" "${url}"; then
    echo "download failed after retries: ${url}" >&2
    rm -f "${destination}"
    exit 1
fi
if ! printf '%s  %s\n' "${checksum}" "${destination}" | sha256sum -c - >/dev/null; then
    echo "checksum mismatch for ${url}" >&2
    rm -f "${destination}"
    exit 1
fi
