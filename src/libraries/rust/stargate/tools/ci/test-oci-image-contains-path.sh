#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <oci-image-layout> <path>" >&2
  exit 2
fi

image_layout="$1"
image_path="${2#/}"

if [[ ! -f "${image_layout}/index.json" || ! -d "${image_layout}/blobs/sha256" ]]; then
  echo "${image_layout} is not an OCI image layout" >&2
  exit 1
fi

while IFS= read -r -d '' blob; do
  if entries="$(tar -tf "${blob}" 2>/dev/null)"; then
    while IFS= read -r entry; do
      entry="${entry#./}"
      entry="${entry#/}"
      if [[ "${entry}" == "${image_path}" ]]; then
        exit 0
      fi
    done <<< "${entries}"
  fi
# rules_oci may symlink blob files to their source layers. Select those links
# directly without following symlinked directories outside the blob tree.
done < <(find "${image_layout}/blobs/sha256" \( -type f -o -type l \) -print0)

echo "missing /${image_path} in ${image_layout}" >&2
exit 1
