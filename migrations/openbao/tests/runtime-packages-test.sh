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
printf '%s\n' "$runtime" | grep -Fq "'ca-certificates>=20261020-r0'" || {
  echo "final runtime must require patched CA certificates" >&2
  exit 1
}
printf '%s\n' "$runtime" | grep -Fq "'ca-certificates-bundle>=20261020-r0'" || {
  echo "final runtime must require patched CA bundle" >&2
  exit 1
}

# Set RUNTIME_IMAGE to verify a built image on the Docker host architecture.
if [ -n "${RUNTIME_IMAGE:-}" ]; then
  arch=$(docker image inspect --format '{{.Architecture}}' "$RUNTIME_IMAGE")
  case "$arch" in
    amd64|arm64) ;;
    *) echo "unsupported runtime architecture: $arch" >&2; exit 1 ;;
  esac
  docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec '
    test "$(id -u)" = 100
    test "$(id -g)" = 1000
    apk info --exists "brotli-libs>=1.2.1-r0"
    apk info --exists "ca-certificates>=20261020-r0"
    apk info --exists "ca-certificates-bundle>=20261020-r0"
    apk list --installed brotli-libs ca-certificates ca-certificates-bundle
    cat /etc/apk/repositories
    bao version
  '
  DOCKER_DEFAULT_PLATFORM="linux/$arch" \
    "$openbao_dir/../../infra/openbao/scripts/verify-runtime-ca.sh" "$RUNTIME_IMAGE"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT INT TERM
  docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
    'cat /usr/bin/bao' > "$work/bao-linux-$arch"
  docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
    'cat /usr/local/bin/jwker' > "$work/jwker"
  chmod 0555 "$work/bao-linux-$arch" "$work/jwker"
  BAO_DIR="$work" ARCHES="$arch" "$openbao_dir/scripts/verify-openbao.sh"
  "$openbao_dir/scripts/verify-go-security.sh" "$work/jwker"
else
  echo "runtime package source contract passed; set RUNTIME_IMAGE for image verification"
fi
