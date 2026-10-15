#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Shared runtime-contract guard for java_oci_image services.
#
# Asserts the parts of the contract that are otherwise only observable by
# running the container, for every architecture in the image index:
#
#   1. The jar is at the exact path the entrypoint names, and no layer whites
#      it out. If the layer path and the entrypoint ever disagree the image
#      builds fine and fails at startup with "Unable to access jarfile".
#   2. The entrypoint keeps the base image's shelless_ulimit shim. oci_image
#      REPLACES the base entrypoint rather than appending, so dropping the shim
#      is silent and leaves the container on the default 1024 fd soft limit.
#   3. Java is invoked through /usr/bin/java, not a JAVA_HOME-derived path. The
#      base sets JAVA_HOME per architecture, so an absolute JAVA_HOME path
#      would break the arm64 half of the image index.
#   4. Runtime parity with the service's pre-Bazel Dockerfile: every declared
#      environment variable, compared as a complete entry, and the working
#      directory.
#   5. Both architectures are present in the index and each carries the same
#      contract. Checking only the host tar proves nothing about the other half,
#      and an index missing or mislabelling a platform pushes without complaint.
#
# Unlike the Go services' image_entrypoint_mode_test this does not assert an
# exec bit: a jar is read by the JVM, never executed, so mode 0644 is correct.
#
# Parsed with tar, grep and sed rather than jq so the test stays hermetic.
#
# Usage:
#   EXPECT_ENV_COUNT=n EXPECT_ENV_0=K=V ... \
#     java_image_contract_test.sh <image.tar> <index-dir> <jar-path> <workdir>
set -euo pipefail

if [[ "$#" -lt 4 ]]; then
  echo "usage: $0 <image.tar> <index dir> <jar path> <workdir>" >&2
  exit 1
fi

image_tar="$1"
index_dir="$2"
jar_abs="$3"
workdir="$4"
shift 4

# Expectations arrive as EXPECT_ENV_0..N-1 rather than positional arguments:
# sh_test args are shell-word-split, so a value containing spaces would be
# truncated at its first space and this test would assert only a fragment of it.
expected_env=()
expect_count="${EXPECT_ENV_COUNT:-0}"
i=0
while [[ "${i}" -lt "${expect_count}" ]]; do
  var="EXPECT_ENV_${i}"
  expected_env+=("${!var}")
  i=$((i + 1))
done

jar_path="${jar_abs#/}"

tmp_dir="${TEST_TMPDIR:-/tmp}/java-image-contract-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

expected_entrypoint="\"Entrypoint\":[\"/usr/bin/shelless_ulimit\",\"/usr/bin/java\",\"-jar\",\"${jar_abs}\"]"

# ---------------------------------------------------------------------------
# 1. the jar is installed at the path the entrypoint names
# ---------------------------------------------------------------------------
# grep -q would close the pipe on first match, SIGPIPE tar, and under pipefail
# reject a valid image. Consume the whole stream.
jar_found=false
while IFS= read -r candidate; do
  tar -tf "${candidate}" >/dev/null 2>&1 || continue
  if tar -tf "${candidate}" | grep -E "^(\./)?${jar_path}$" >/dev/null; then
    jar_found=true
    break
  fi
done < <(find "${outer_dir}" -type f)

if [[ "${jar_found}" != "true" ]]; then
  echo "no image layer contains ${jar_abs}" >&2
  find "${outer_dir}" -type f -print >&2
  exit 1
fi

# Presence in a layer is necessary but not sufficient: a later layer can delete
# the file with a whiteout, leaving the merged filesystem without it. Check for
# a whiteout of the jar itself and for opaque whiteouts on its ancestor
# directories ONLY. An opaque marker on an unrelated directory does not affect
# this file and must not fail the test.
#   file whiteout   : <dir>/.wh.<name>
#   opaque whiteout : <dir>/.wh..wh..opq   (empties that directory)
#
# Derived from jar_path so this stays correct for any install location.
jar_dir="${jar_path%/*}"
jar_name="${jar_path##*/}"
whiteout_re="^(\./)?${jar_dir}/\.wh\.${jar_name//./\\.}$"
whiteout_re+="|^(\./)?\.wh\.\.wh\.\.opq$"
ancestor=""
IFS='/' read -r -a jar_parts <<< "${jar_dir}"
for part in "${jar_parts[@]}"; do
  if [[ -z "${ancestor}" ]]; then
    whiteout_re+="|^(\./)?\.wh\.${part}$"
    ancestor="${part}"
  else
    whiteout_re+="|^(\./)?${ancestor}/\.wh\.${part}$"
    ancestor="${ancestor}/${part}"
  fi
  whiteout_re+="|^(\./)?${ancestor}/\.wh\.\.wh\.\.opq$"
done

while IFS= read -r candidate; do
  tar -tf "${candidate}" >/dev/null 2>&1 || continue
  if tar -tf "${candidate}" | grep -E "${whiteout_re}" >/dev/null; then
    echo "a layer whites out ${jar_abs} or one of its parent directories" >&2
    tar -tf "${candidate}" | grep -E "${whiteout_re}" >&2 || true
    exit 1
  fi
done < <(find "${outer_dir}" -type f)

# ---------------------------------------------------------------------------
# 2-4. assert against the image config blob, and only that blob
# ---------------------------------------------------------------------------
# Resolve manifest.json -> the "Config" blob rather than scanning every file in
# the archive. Scanning everything would let a layer tar or bundled payload that
# merely contains the expected text satisfy these assertions even when the real
# image config is wrong.
manifest="${outer_dir}/manifest.json"
if [[ ! -f "${manifest}" ]]; then
  echo "archive has no manifest.json; cannot resolve the image config" >&2
  exit 1
fi

config_rel="$(tr -d ' \n' < "${manifest}" \
  | sed -n 's/.*"Config":"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${config_rel}" ]]; then
  echo "could not read the Config entry from manifest.json" >&2
  cat "${manifest}" >&2
  exit 1
fi

config="${outer_dir}/${config_rel}"
if [[ ! -f "${config}" ]]; then
  echo "manifest.json points at a missing config blob: ${config_rel}" >&2
  exit 1
fi

# Two views of the same config. The flattened one is for structural patterns
# like "Entrypoint":[...] , which are written without whitespace. Env values are
# compared against the file as written, because deleting spaces would also
# delete them from inside values such as JDK_JAVA_OPTIONS.
config_flat="$(tr -d ' \n' < "${config}")"
config_raw="$(cat "${config}")"

# Assert one architecture's config. Used for the host tar and again for every
# platform in the index, so the two can never drift apart.
assert_config() {  # assert_config <label> <flattened> <raw>
  local label="$1" flat="$2" raw="$3"

  if ! printf '%s' "${flat}" | grep -F "${expected_entrypoint}" >/dev/null; then
    echo "${label}: entrypoint is not [/usr/bin/shelless_ulimit /usr/bin/java -jar ${jar_abs}]" >&2
    printf '%s' "${flat}" | grep -o '"Entrypoint":[^]]*]' >&2 || true
    return 1
  fi

  # Match whole Env elements, quotes included, not substrings. Grepping for a
  # fragment such as MaxRAMPercentage=40.0 passes even when the rest of
  # JDK_JAVA_OPTIONS is dropped, and a bare ULIMIT_FLAG=1 would also be
  # satisfied by a differently named variable like MY_ULIMIT_FLAG=1.
  local entry
  for entry in "${expected_env[@]}"; do
    if ! printf '%s' "${raw}" | grep -F "\"${entry}\"" >/dev/null; then
      echo "${label}: config is missing expected env entry: ${entry}" >&2
      printf '%s' "${flat}" | grep -o '"Env":\[[^]]*\]' >&2 || true
      return 1
    fi
  done

  if ! printf '%s' "${flat}" | grep -F "\"WorkingDir\":\"${workdir}\"" >/dev/null; then
    echo "${label}: working directory is not ${workdir}" >&2
    printf '%s' "${flat}" | grep -o '"WorkingDir":"[^"]*"' >&2 || true
    return 1
  fi
}

assert_config "host image" "${config_flat}" "${config_raw}"

# ---------------------------------------------------------------------------
# 5. the image index carries both architectures, each with the same contract
# ---------------------------------------------------------------------------
# Everything above inspects the host-architecture tar, which says nothing about
# the other half of a multi-arch index. Walk the OCI layout directly:
# index.json -> manifest list -> per-platform manifest -> config.
if [[ ! -f "${index_dir}/index.json" ]]; then
  echo "not an OCI layout (no index.json): ${index_dir}" >&2
  exit 1
fi

blob_for() {  # blob_for <digest as sha256:hex>
  printf '%s/blobs/%s/%s' "${index_dir}" "${1%%:*}" "${1#*:}"
}

top_digest="$(tr -d ' \n' < "${index_dir}/index.json" \
  | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
if [[ -z "${top_digest}" ]]; then
  echo "index.json has no manifest descriptor" >&2
  exit 1
fi
manifest_list="$(blob_for "${top_digest}")"
if [[ ! -f "${manifest_list}" ]]; then
  echo "index.json points at a missing blob: ${top_digest}" >&2
  exit 1
fi

list_flat="$(tr -d ' \n' < "${manifest_list}")"

for arch in amd64 arm64; do
  # Split the manifests array into one entry per line so a digest is only ever
  # read from the same entry that declares the architecture.
  entry="$(printf '%s' "${list_flat}" \
    | sed 's/}, *{/}\n{/g' \
    | grep -F "\"architecture\":\"${arch}\"" | head -1)"
  if [[ -z "${entry}" ]]; then
    echo "image index has no ${arch} manifest" >&2
    printf '%s' "${list_flat}" | grep -o '"architecture":"[a-z0-9]*"' >&2 || true
    exit 1
  fi

  man_digest="$(printf '%s' "${entry}" | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
  man_blob="$(blob_for "${man_digest}")"
  if [[ ! -f "${man_blob}" ]]; then
    echo "${arch} manifest blob is missing: ${man_digest}" >&2
    exit 1
  fi

  cfg_digest="$(tr -d ' \n' < "${man_blob}" \
    | grep -o '"config":{[^}]*}' | grep -o '"digest":"sha256:[0-9a-f]*"' | head -1 | cut -d'"' -f4)"
  cfg_blob="$(blob_for "${cfg_digest}")"
  if [[ ! -f "${cfg_blob}" ]]; then
    echo "${arch} config blob is missing: ${cfg_digest}" >&2
    exit 1
  fi

  arch_flat="$(tr -d ' \n' < "${cfg_blob}")"
  arch_raw="$(cat "${cfg_blob}")"

  # The config must declare the architecture it was filed under, otherwise the
  # index is mislabelled and runtimes pull the wrong image for their platform.
  if ! printf '%s' "${arch_flat}" | grep -F "\"architecture\":\"${arch}\"" >/dev/null; then
    echo "${arch} entry points at a config declaring a different architecture" >&2
    exit 1
  fi

  assert_config "${arch} image" "${arch_flat}" "${arch_raw}"

  echo "ok: ${arch} manifest present with the expected runtime contract"
done
