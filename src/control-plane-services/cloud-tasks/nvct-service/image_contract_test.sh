#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Asserts the runtime contract of the nvct-service image, which is otherwise
# only observable by running the container:
#
#   1. The jar is at /usr/share/app.jar, the exact path the entrypoint names,
#      and no layer whites it out. If the layer path and the entrypoint ever
#      disagree the image builds fine and fails at startup with "Unable to
#      access jarfile".
#   2. The entrypoint keeps the base image's shelless_ulimit shim. oci_image
#      REPLACES the base entrypoint rather than appending, so dropping the shim
#      is silent and leaves the container on the default 1024 fd soft limit.
#   3. Java is invoked through /usr/bin/java, not a JAVA_HOME-derived path. The
#      base sets JAVA_HOME per architecture, so an absolute JAVA_HOME path
#      would break the arm64 half of the image index.
#   4. Runtime parity with the pre-Bazel Dockerfile: ULIMIT_FLAG (which the
#      shim reads; without it the shim raises nothing), JDK_JAVA_OPTIONS
#      (container heap sizing), and the working directory.
#
# Unlike the Go services' image_entrypoint_mode_test this does not assert an
# exec bit: a jar is read by the JVM, never executed, so mode 0644 is correct.
set -euo pipefail

image_tar="$1"
tmp_dir="${TEST_TMPDIR:-/tmp}/nvct-image-contract-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

jar_path="usr/share/app.jar"

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
  echo "no image layer contains /${jar_path}" >&2
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
whiteout_re='^(\./)?usr/share/\.wh\.app\.jar$'
whiteout_re+='|^(\./)?usr/\.wh\.share$'
whiteout_re+='|^(\./)?\.wh\.usr$'
whiteout_re+='|^(\./)?\.wh\.\.wh\.\.opq$'
whiteout_re+='|^(\./)?usr/\.wh\.\.wh\.\.opq$'
whiteout_re+='|^(\./)?usr/share/\.wh\.\.wh\.\.opq$'

while IFS= read -r candidate; do
  tar -tf "${candidate}" >/dev/null 2>&1 || continue
  if tar -tf "${candidate}" | grep -E "${whiteout_re}" >/dev/null; then
    echo "a layer whites out /${jar_path} or one of its parent directories" >&2
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

expected_entrypoint='"Entrypoint":["/usr/bin/shelless_ulimit","/usr/bin/java","-jar","/usr/share/app.jar"]'
if ! printf '%s' "${config_flat}" | grep -F "${expected_entrypoint}" >/dev/null; then
  echo "image entrypoint is not [/usr/bin/shelless_ulimit /usr/bin/java -jar /usr/share/app.jar]" >&2
  printf '%s' "${config_flat}" | grep -o '"Entrypoint":[^]]*]' >&2 || true
  exit 1
fi

# Match whole Env elements, quotes included, not substrings. Grepping for
# MaxRAMPercentage=40.0 alone passes even if the other three JDK_JAVA_OPTIONS
# values are dropped, and a bare ULIMIT_FLAG=1 would also be satisfied by a
# differently named variable such as MY_ULIMIT_FLAG=1. The surrounding quotes
# anchor each check to a complete entry.
expected_jdk_opts='-XX:MaxRAMPercentage=40.0 -XX:+EnableDynamicAgentLoading -Dreactor.netty.pool.maxIdleTime=30000 -Dreactor.netty.pool.maxConnections=500'
for expected in \
  "\"ULIMIT_FLAG=1\"" \
  "\"JDK_JAVA_OPTIONS=${expected_jdk_opts}\""; do
  if ! printf '%s' "${config_raw}" | grep -F "${expected}" >/dev/null; then
    echo "image config is missing expected runtime setting: ${expected}" >&2
    printf '%s' "${config_flat}" | grep -o '"Env":\[[^]]*\]' >&2 || true
    exit 1
  fi
done

if ! printf '%s' "${config_flat}" | grep -F '"WorkingDir":"/home/app"' >/dev/null; then
  echo "image working directory is not /home/app" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 5. the image index carries both architectures, each with the same contract
# ---------------------------------------------------------------------------
# Everything above inspects the host-architecture tar. That proves nothing
# about the arm64 half, and a multi-arch index whose second platform is absent
# or misconfigured builds and pushes without complaint. Walk the OCI layout
# directly: index.json -> manifest list -> per-platform manifest -> config.
#
# Parsed with grep and sed rather than jq to keep the test hermetic; the
# structures read here are small and fixed-shape.
index_dir="${2:-}"
if [[ -z "${index_dir}" ]]; then
  echo "usage: $0 <image.tar> <image_index dir>" >&2
  exit 1
fi
if [[ ! -f "${index_dir}/index.json" ]]; then
  echo "not an OCI layout (no index.json): ${index_dir}" >&2
  exit 1
fi

blob_for() {  # blob_for <digest as sha256:hex>
  printf '%s/blobs/%s/%s' "${index_dir}" "${1%%:*}" "${1#*:}"
}

# index.json holds one descriptor pointing at the manifest list.
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
  # Split the manifests array into one line per entry so a digest is only ever
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

  arch_cfg="$(tr -d ' \n' < "${cfg_blob}")"
  arch_raw="$(cat "${cfg_blob}")"

  # The config must declare the architecture it was filed under, otherwise the
  # index is mislabelled and runtimes pull the wrong image for their platform.
  if ! printf '%s' "${arch_cfg}" | grep -F "\"architecture\":\"${arch}\"" >/dev/null; then
    echo "${arch} entry points at a config declaring a different architecture" >&2
    exit 1
  fi

  # Same runtime contract as the host-architecture checks above.
  if ! printf '%s' "${arch_cfg}" | grep -F "${expected_entrypoint}" >/dev/null; then
    echo "${arch} image has the wrong entrypoint" >&2
    printf '%s' "${arch_cfg}" | grep -o '"Entrypoint":[^]]*]' >&2 || true
    exit 1
  fi
  for expected in \
    "\"ULIMIT_FLAG=1\"" \
    "\"JDK_JAVA_OPTIONS=${expected_jdk_opts}\""; do
    if ! printf '%s' "${arch_raw}" | grep -F "${expected}" >/dev/null; then
      echo "${arch} image config is missing expected runtime setting: ${expected}" >&2
      printf '%s' "${arch_cfg}" | grep -o '"Env":\[[^]]*\]' >&2 || true
      exit 1
    fi
  done

  if ! printf '%s' "${arch_cfg}" | grep -F '"WorkingDir":"/home/app"' >/dev/null; then
    echo "${arch} image working directory is not /home/app" >&2
    exit 1
  fi

  echo "ok: ${arch} manifest present with the expected runtime contract"
done
