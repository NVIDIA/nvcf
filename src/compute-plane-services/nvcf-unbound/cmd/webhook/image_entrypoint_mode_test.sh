#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Asserts the binary inside the image tarball is mode 0755 at
# /usr/bin/app. Without this guard, rules_pkg defaults non-source srcs
# to 0644 and the container fails to start with "permission denied".
set -euo pipefail

image_tar="$1"
tmp_dir="${TEST_TMPDIR:-/tmp}/nvcf-unbound-image-mode-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

while IFS= read -r candidate; do
  if ! tar -tf "${candidate}" >/dev/null 2>&1; then
    continue
  fi
  if ! tar -tf "${candidate}" | grep -Eq '^(\./)?usr/bin/app$'; then
    continue
  fi

  layer_dir="${tmp_dir}/layer"
  mkdir -p "${layer_dir}"
  tar -xf "${candidate}" -C "${layer_dir}"

  entrypoint="${layer_dir}/usr/bin/app"
  if [[ ! -x "${entrypoint}" ]]; then
    echo "/usr/bin/app is not executable" >&2
    ls -l "${entrypoint}" >&2
    exit 1
  fi

  exit 0
done < <(find "${outer_dir}" -type f)

echo "no image layer contains /usr/bin/app" >&2
find "${outer_dir}" -type f -print >&2
exit 1
