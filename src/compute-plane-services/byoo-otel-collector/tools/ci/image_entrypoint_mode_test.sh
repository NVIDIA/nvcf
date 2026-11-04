#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Inspect a pkg_tar layer and fail if the named entrypoint path is
# either missing or not executable. Catches the rules_pkg pkg_files
# default-mode bug (0644) that caused the helm-reval 0.2.1 image to
# fail startup with "permission denied". See nvcf/nvcf!365.
#
# Usage: image_entrypoint_mode_test.sh <layer.tar> <entrypoint_path>
#   layer.tar         The pkg_tar output that feeds into oci_image.
#   entrypoint_path   Absolute path inside the layer (e.g. /usr/bin/svc).

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <layer.tar> <entrypoint_path>" >&2
  exit 2
fi

layer_tar="$1"
entrypoint_path="$2"

# Normalize the lookup so a leading slash and a relative `./` form both
# match what tar emits for files under the layer root.
rel_entrypoint="${entrypoint_path#/}"

tmp_dir="${TEST_TMPDIR:-/tmp}/byoo-image-mode-${RANDOM}-${RANDOM}"
mkdir -p "${tmp_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

if ! tar -tf "${layer_tar}" >/dev/null 2>&1; then
  echo "${layer_tar} is not a readable tar archive" >&2
  exit 1
fi

if ! tar -tf "${layer_tar}" \
     | grep -Eq "^(\\./)?${rel_entrypoint}\$"; then
  echo "missing ${entrypoint_path} in ${layer_tar}" >&2
  echo "layer contents:" >&2
  tar -tf "${layer_tar}" >&2
  exit 1
fi

tar -xf "${layer_tar}" -C "${tmp_dir}"

entrypoint="${tmp_dir}/${rel_entrypoint}"
if [[ ! -e "${entrypoint}" ]]; then
  echo "${entrypoint_path} extracted but not found at ${entrypoint}" >&2
  exit 1
fi

if [[ ! -x "${entrypoint}" ]]; then
  echo "${entrypoint_path} is not executable" >&2
  ls -l "${entrypoint}" >&2
  exit 1
fi
