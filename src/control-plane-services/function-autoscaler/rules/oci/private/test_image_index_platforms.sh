#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral test for image_index_platforms_test.sh.
#
# A guard that only ever passes is indistinguishable from no guard. The bug this
# was written for -- an index silently losing its arm64 half -- is exactly the
# case that must fail, so assert the failures directly rather than only the
# happy path. Each case builds a synthetic OCI layout so these stay true
# independently of what the real image currently contains.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/image_index_platforms_test.sh"
fail=0

# Digests must be real 64-char lowercase hex: the guard matches
# sha256:[0-9a-f]* and would reject a readable placeholder, making every case
# fail for the wrong reason. Derived deterministically from a label so each blob
# gets a distinct, stable name.
digest_for() {  # digest_for <label>
    printf '%s' "$1" | sha256sum | cut -d' ' -f1
}

# Build a minimal but structurally real OCI layout: index.json -> manifest list
# -> per-arch manifest -> per-arch config. Each arch's config declares
# config_arch, which lets us forge the mislabelled case.
make_layout() {  # make_layout <dir> <arch:config_arch>...
    local dir="$1"; shift
    mkdir -p "${dir}/blobs/sha256"

    local entries=() spec arch cfg_arch cfg_d man_d
    for spec in "$@"; do
        arch="${spec%%:*}"
        cfg_arch="${spec##*:}"
        cfg_d="$(digest_for "config-${arch}")"
        man_d="$(digest_for "manifest-${arch}")"

        printf '{"architecture":"%s","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}' \
            "${cfg_arch}" > "${dir}/blobs/sha256/${cfg_d}"
        printf '{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":1},"layers":[]}' \
            "${cfg_d}" > "${dir}/blobs/sha256/${man_d}"

        entries+=("{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"digest\":\"sha256:${man_d}\",\"size\":1,\"platform\":{\"architecture\":\"${arch}\",\"os\":\"linux\"}}")
    done

    local joined list_d
    joined="$(IFS=,; echo "${entries[*]}")"
    list_d="$(digest_for list)"
    printf '{"schemaVersion":2,"manifests":[%s]}' "${joined}" > "${dir}/blobs/sha256/${list_d}"
    printf '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:%s","size":1}]}' \
        "${list_d}" > "${dir}/index.json"
}

run() {  # run <dir> <arch>... -> sets $out and $rc
    local dir="$1"; shift
    set +e
    out="$(bash "${script}" "${dir}" "$@" 2>&1)"
    rc=$?
    set -e
}

expect() {  # expect <desc> <want-rc> [substring]
    local desc="$1" want="$2" substr="${3:-}"
    if [ "${rc}" != "${want}" ]; then
        printf 'FAIL %s: want exit %s, got %s\n%s\n' "${desc}" "${want}" "${rc}" "${out}"
        fail=1
        return
    fi
    if [ -n "${substr}" ] && ! printf '%s' "${out}" | grep -q -- "${substr}"; then
        printf 'FAIL %s: output missing %q\n%s\n' "${desc}" "${substr}" "${out}"
        fail=1
        return
    fi
    printf 'ok   %s\n' "${desc}"
}

# Both architectures present and self-consistent: the expected good state.
t="$(mktemp -d)"; make_layout "${t}" amd64:amd64 arm64:arm64
run "${t}" amd64 arm64
expect "both arches pass" 0 "ok: arm64 manifest present"
rm -rf "${t}"

# THE regression this exists for: arm64 silently absent from the index.
t="$(mktemp -d)"; make_layout "${t}" amd64:amd64
run "${t}" amd64 arm64
expect "missing arm64 fails" 1 "missing the arm64 manifest"
rm -rf "${t}"

# The mirror image of the above, so the guard is not one-directional.
t="$(mktemp -d)"; make_layout "${t}" arm64:arm64
run "${t}" amd64 arm64
expect "missing amd64 fails" 1 "missing the amd64 manifest"
rm -rf "${t}"

# An entry filed under arm64 whose config actually declares amd64. The index
# looks complete but half of it runs the wrong binary.
t="$(mktemp -d)"; make_layout "${t}" amd64:amd64 arm64:amd64
run "${t}" amd64 arm64
expect "mislabelled config fails" 1 "different architecture"
rm -rf "${t}"

# An architecture nobody declared appearing in the index.
t="$(mktemp -d)"; make_layout "${t}" amd64:amd64 arm64:arm64 s390x:s390x
run "${t}" amd64 arm64
expect "unexpected extra arch fails" 1 "unexpected architecture: s390x"
rm -rf "${t}"

# A directory that is not an OCI layout must be a loud error, not an empty pass.
t="$(mktemp -d)"
run "${t}" amd64 arm64
expect "non-layout dir is an error" 1 "no index.json"
rm -rf "${t}"

# A descriptor pointing at a blob that does not exist must fail rather than be
# skipped, otherwise a truncated layout reads as a complete one.
t="$(mktemp -d)"; make_layout "${t}" amd64:amd64 arm64:arm64
rm -f "${t}/blobs/sha256/$(digest_for manifest-arm64)"
run "${t}" amd64 arm64
expect "dangling manifest blob fails" 1 "manifest blob is missing"
rm -rf "${t}"

if [ "${fail}" -ne 0 ]; then
    echo "image_index_platforms_test: FAILED" >&2
    exit 1
fi
echo "image_index_platforms_test: all checks passed"
