#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Assert that an OCI image index carries exactly the expected architectures.
#
# Why this exists: this service shipped an amd64-only image index for months and
# nothing noticed. The build stayed green the whole time because dropping a
# platform is a configuration edit, not a compile error -- DEFAULT_PLATFORMS lost
# an entry and every downstream target happily built the smaller index. The
# arm64 half was missing from the published manifest and only a human reading
# the registry would have caught it.
#
# The expected architectures are passed in as policy, deliberately NOT derived
# from DEFAULT_PLATFORMS. Deriving them would make this test restate whatever
# the build already decided: removing arm64 from DEFAULT_PLATFORMS would also
# remove it from the expectation and the test would pass, which is precisely the
# regression it is here to catch.
#
# Both directions are checked. A missing architecture is the known failure, and
# an unexpected extra one means the index gained a platform nobody declared.
#
# Parsed with grep and sed rather than jq so the test stays hermetic, matching
# java_image_contract_test.sh.
set -euo pipefail

if [[ "$#" -lt 2 ]]; then
    echo "usage: $0 <oci layout dir> <arch> [<arch>...]" >&2
    exit 1
fi

index_dir="$1"
shift
expected=("$@")

if [[ ! -f "${index_dir}/index.json" ]]; then
    echo "not an OCI layout (no index.json): ${index_dir}" >&2
    ls -la "${index_dir}" >&2 || true
    exit 1
fi

blob_for() {  # blob_for <digest as sha256:hex>
    printf '%s/blobs/%s/%s' "${index_dir}" "${1%%:*}" "${1#*:}"
}

top_digest="$(tr -d ' \n' < "${index_dir}/index.json" \
    | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
if [[ -z "${top_digest}" ]]; then
    echo "index.json has no manifest descriptor" >&2
    cat "${index_dir}/index.json" >&2
    exit 1
fi

manifest_list="$(blob_for "${top_digest}")"
if [[ ! -f "${manifest_list}" ]]; then
    echo "index.json points at a missing blob: ${top_digest}" >&2
    exit 1
fi

# Split the manifests array into one entry per line so an architecture is only
# ever read from the entry that declares it, and a digest is only ever read from
# the same entry as its architecture.
list_flat="$(tr -d ' \n' < "${manifest_list}")"
entries="$(printf '%s' "${list_flat}" | sed 's/}, *{/}\n{/g')"

found=()
while IFS= read -r arch; do
    [[ -n "${arch}" ]] && found+=("${arch}")
done < <(printf '%s' "${entries}" \
    | grep -o '"architecture":"[a-z0-9]*"' | cut -d'"' -f4 | sort -u)

if [[ "${#found[@]}" -eq 0 ]]; then
    echo "image index declares no architectures at all" >&2
    printf '%s\n' "${list_flat}" >&2
    exit 1
fi

status=0

for want in "${expected[@]}"; do
    hit=false
    for got in "${found[@]}"; do
        [[ "${got}" == "${want}" ]] && hit=true && break
    done
    if [[ "${hit}" != "true" ]]; then
        echo "image index is missing the ${want} manifest" >&2
        status=1
        continue
    fi

    # Present in the list is not enough. Follow the descriptor to the config
    # blob and confirm the image itself declares that architecture: an entry
    # can be filed under one platform while pointing at another image, which
    # pushes without complaint and then runs the wrong binary on that host.
    entry="$(printf '%s' "${entries}" | grep -F "\"architecture\":\"${want}\"" | head -1)"
    man_digest="$(printf '%s' "${entry}" \
        | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
    man_blob="$(blob_for "${man_digest}")"
    if [[ ! -f "${man_blob}" ]]; then
        echo "${want}: manifest blob is missing: ${man_digest}" >&2
        status=1
        continue
    fi

    cfg_digest="$(tr -d ' \n' < "${man_blob}" \
        | grep -o '"config":{[^}]*}' \
        | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
    cfg_blob="$(blob_for "${cfg_digest}")"
    if [[ ! -f "${cfg_blob}" ]]; then
        echo "${want}: config blob is missing: ${cfg_digest}" >&2
        status=1
        continue
    fi

    if ! tr -d ' \n' < "${cfg_blob}" | grep -F "\"architecture\":\"${want}\"" >/dev/null; then
        echo "${want}: entry points at a config declaring a different architecture" >&2
        tr -d ' \n' < "${cfg_blob}" | grep -o '"architecture":"[a-z0-9]*"' >&2 || true
        status=1
        continue
    fi

    echo "ok: ${want} manifest present and self-consistent"
done

for got in "${found[@]}"; do
    hit=false
    for want in "${expected[@]}"; do
        [[ "${got}" == "${want}" ]] && hit=true && break
    done
    if [[ "${hit}" != "true" ]]; then
        echo "image index declares an unexpected architecture: ${got}" >&2
        status=1
    fi
done

if [[ "${status}" -ne 0 ]]; then
    echo "expected architectures: ${expected[*]}" >&2
    echo "index declares:         ${found[*]}" >&2
    exit 1
fi
