#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Asserts the runtime contract of the nvct-service image, which is otherwise
# only observable by running the container:
#
#   1. The jar is at /usr/share/app.jar, the exact path the entrypoint names. If the
#      layer path and the entrypoint ever disagree, the image builds fine and
#      fails at startup with "Unable to access jarfile".
#   2. The entrypoint keeps the base image's shelless_ulimit shim. oci_image
#      REPLACES the base entrypoint rather than appending, so dropping the shim
#      is silent and leaves the container on the default 1024 fd soft limit.
#   3. Java is invoked through /usr/bin/java, not a JAVA_HOME-derived path. The
#      base sets JAVA_HOME per architecture, so an absolute JAVA_HOME path
#      would break the arm64 half of the image index.
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

# 1. the jar is present at /app.jar in some layer
jar_found=false
while IFS= read -r candidate; do
  tar -tf "${candidate}" >/dev/null 2>&1 || continue
  # grep -q would close the pipe on first match, SIGPIPE tar, and under
  # pipefail reject a valid image. Consume the whole stream.
  if tar -tf "${candidate}" | grep -E '^(\./)?usr/share/app\.jar$' >/dev/null; then
    jar_found=true
    break
  fi
done < <(find "${outer_dir}" -type f)

if [[ "${jar_found}" != "true" ]]; then
  echo "no image layer contains /usr/share/app.jar" >&2
  find "${outer_dir}" -type f -print >&2
  exit 1
fi

# Presence in a layer is necessary but not sufficient: a later layer could
# delete the file via an OCI whiteout marker, leaving the merged filesystem
# without it. Assert no layer whites out the jar or its directory.
while IFS= read -r candidate; do
  tar -tf "${candidate}" >/dev/null 2>&1 || continue
  if tar -tf "${candidate}" \
      | grep -E '^(\./)?usr/share/\.wh\.app\.jar$|^(\./)?usr/\.wh\.share$|^(\./)?\.wh\.usr$|\.wh\.\.wh\.opq$' \
      >/dev/null; then
    echo "a layer whites out /usr/share/app.jar or a parent directory" >&2
    tar -tf "${candidate}" | grep '\.wh\.' >&2 || true
    exit 1
  fi
done < <(find "${outer_dir}" -type f)

# 2 + 3. the entrypoint keeps the shim and uses the absolute java path. Scan
# every regular file rather than parsing manifest.json, so the test needs no
# JSON tool beyond coreutils. Note the config is a content-addressed blob under
# blobs/sha256/ with NO .json extension, so it cannot be found by suffix.
entrypoint_ok=false
while IFS= read -r meta; do
  grep -q '"Entrypoint"' "${meta}" 2>/dev/null || continue
  if tr -d ' \n' < "${meta}" \
      | grep '"Entrypoint":\["/usr/bin/shelless_ulimit","/usr/bin/java","-jar","/usr/share/app.jar"\]' >/dev/null; then
    entrypoint_ok=true
    break
  fi
done < <(find "${outer_dir}" -type f)

if [[ "${entrypoint_ok}" != "true" ]]; then
  echo "image entrypoint is not [/usr/bin/shelless_ulimit /usr/bin/java -jar /usr/share/app.jar]" >&2
  echo "entrypoints found in the image:" >&2
  find "${outer_dir}" -type f -exec grep -ho '"Entrypoint":[^]]*]' {} \; 2>/dev/null >&2 || true
  exit 1
fi

# 4. runtime parity with the pre-Bazel Dockerfile: ULIMIT_FLAG (which the
# shelless_ulimit shim reads; without it the shim raises nothing),
# JDK_JAVA_OPTIONS (container heap sizing), and the working directory.
for expected in 'ULIMIT_FLAG=1' 'MaxRAMPercentage=40.0' '"WorkingDir":"/home/app"'; do
  found=false
  while IFS= read -r meta; do
    grep -q '"Entrypoint"' "${meta}" 2>/dev/null || continue
    if tr -d ' \n' < "${meta}" | grep -F "${expected}" >/dev/null; then
      found=true
      break
    fi
  done < <(find "${outer_dir}" -type f)
  if [[ "${found}" != "true" ]]; then
    echo "image config is missing expected runtime setting: ${expected}" >&2
    exit 1
  fi
done
