#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

image_tar="$1"
tmp_dir="${TEST_TMPDIR:-/tmp}/reval-image-mode-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

found=0
while IFS= read -r candidate; do
  if ! tar -tf "${candidate}" >/dev/null 2>&1; then
    continue
  fi
  if ! tar -tf "${candidate}" | grep -Eq '^(\./)?usr/bin/reval-service$'; then
    continue
  fi

  found=1
  layer_dir="${tmp_dir}/layer"
  mkdir -p "${layer_dir}"
  tar -xf "${candidate}" -C "${layer_dir}"

  entrypoint="${layer_dir}/usr/bin/reval-service"
  if [[ ! -x "${entrypoint}" ]]; then
    echo "/usr/bin/reval-service is not executable" >&2
    ls -l "${entrypoint}" >&2
    exit 1
  fi

  exit 0
done < <(find "${outer_dir}" -type f)

if [[ "${found}" -eq 0 ]]; then
  echo "missing /usr/bin/reval-service in image layer" >&2
  find "${outer_dir}" -type f -print >&2
  exit 1
fi
