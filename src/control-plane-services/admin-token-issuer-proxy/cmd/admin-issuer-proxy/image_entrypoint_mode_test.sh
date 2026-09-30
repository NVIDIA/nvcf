#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

image_tar="$1"
tmp_dir="${TEST_TMPDIR:-/tmp}/admin-issuer-proxy-image-mode-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

while IFS= read -r candidate; do
  entries="$(tar -tf "${candidate}" 2>/dev/null)" || continue
  if ! grep -Eq '^(\./)?usr/bin/admin-issuer-proxy$' <<<"${entries}"; then
    continue
  fi

  layer_dir="${tmp_dir}/layer"
  mkdir -p "${layer_dir}"
  tar -xf "${candidate}" -C "${layer_dir}"

  entrypoint="${layer_dir}/usr/bin/admin-issuer-proxy"
  if [[ ! -x "${entrypoint}" ]]; then
    echo "/usr/bin/admin-issuer-proxy is not executable" >&2
    ls -l "${entrypoint}" >&2
    exit 1
  fi

  exit 0
done < <(find "${outer_dir}" -type f)

echo "no image layer contains /usr/bin/admin-issuer-proxy" >&2
find "${outer_dir}" -type f -print >&2
exit 1
