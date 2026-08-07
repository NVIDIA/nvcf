#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Verifies the OCI tarball contract consumed by NVCA: worker-init remains the
# primary entrypoint and nvcf-trust-bundle-install is available and executable.
set -euo pipefail

image_tar="$1"
tmp_dir="${TEST_TMPDIR:-/tmp}/worker-init-image-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

config_rel_path="$(sed -nE 's/.*"Config": "([^"]+)".*/\1/p' "${outer_dir}/manifest.json" | head -n 1)"
if [[ -z "${config_rel_path}" || ! -f "${outer_dir}/${config_rel_path}" ]]; then
  echo "could not find the image configuration in manifest.json" >&2
  exit 1
fi

entrypoint="$(sed -n '/"Entrypoint"/,/\]/p' "${outer_dir}/${config_rel_path}" | tr -d '[:space:]')"
case "${entrypoint}" in
  '"Entrypoint":["/worker-init"],'|'"Entrypoint":["/worker-init"]') ;;
  *)
    echo "unexpected image entrypoint: ${entrypoint}" >&2
    exit 1
    ;;
esac

for layer in "${outer_dir}"/blobs/sha256/*.tar.gz; do
  # Do not use grep -q here. With pipefail enabled, GNU tar receives SIGPIPE
  # as soon as grep exits after its first match, which makes a valid layer
  # appear to be missing the installer in Linux CI.
  if ! tar -tzf "${layer}" | grep -E '^(\./)?usr/bin/nvcf-trust-bundle-install$' >/dev/null; then
    continue
  fi

  layer_dir="${tmp_dir}/layer"
  mkdir -p "${layer_dir}"
  tar -xzf "${layer}" -C "${layer_dir}"
  installer="${layer_dir}/usr/bin/nvcf-trust-bundle-install"
  if [[ ! -x "${installer}" ]]; then
    echo "/usr/bin/nvcf-trust-bundle-install is not executable" >&2
    exit 1
  fi
  exit 0
done

echo "no image layer contains /usr/bin/nvcf-trust-bundle-install" >&2
exit 1
