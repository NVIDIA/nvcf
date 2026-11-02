#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
openbao_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
# Check the final stage: fixing a discarded builder does not fix the image.
runtime=$(awk '/^FROM / { stage = "" } { stage = stage $0 "\n" } END { printf "%s", stage }' "$openbao_dir/Dockerfile")
printf '%s\n' "$runtime" | grep -Fq "'brotli-libs>=1.2.1-r0'" || {
  echo "final runtime must require fixed Brotli" >&2
  exit 1
}
printf '%s\n' "$runtime" | grep -Fq 'apk upgrade --no-cache' || {
  echo "final runtime must refresh inherited packages" >&2
  exit 1
}

# Set RUNTIME_IMAGE to verify a built image on the Docker host architecture.
if [ -n "${RUNTIME_IMAGE:-}" ]; then
  docker run --rm --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec '
    test "$(id -u)" = 100
    test "$(id -g)" = 1000
    apk info --exists "brotli-libs>=1.2.1-r0"
    apk list --installed brotli-libs
    cat /etc/apk/repositories
    bao version
  '
  if [ -n "${BASELINE_IMAGE:-}" ]; then
    baseline=$(docker run --rm --entrypoint /bin/sh "$BASELINE_IMAGE" -ec \
      'sha256sum /usr/bin/bao /openbao/plugins/vault-plugin-secrets-jwt')
    current=$(docker run --rm --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
      'sha256sum /usr/bin/bao /openbao/plugins/vault-plugin-secrets-jwt')
    test "$current" = "$baseline" || {
      echo "server or JWT plugin executable changed from baseline" >&2
      exit 1
    }
    printf '%s\n' "$current"
    echo "server and JWT plugin bytes match baseline"
  fi
else
  echo "runtime package source contract passed; set RUNTIME_IMAGE for image verification"
fi
