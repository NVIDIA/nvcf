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
  DOCKER_DEFAULT_PLATFORM="linux/$arch" "$script_dir/verify-runtime-ca.sh" "$RUNTIME_IMAGE"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT INT TERM
  . "$openbao_dir/jwt-plugin-versions.env"
  docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
    'cat /usr/bin/bao' > "$work/bao-linux-$arch"
  docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
    "cat /openbao/plugins/vault-plugin-secrets-jwt-${JWT_PLUGIN_TARGET_VERSION}" > \
    "$work/vault-plugin-secrets-jwt-linux-$arch"
  chmod 0555 "$work/bao-linux-$arch" "$work/vault-plugin-secrets-jwt-linux-$arch"
  BAO_DIR="$work" ARCHES="$arch" "$script_dir/verify-openbao.sh"
  PLUGIN_DIR="$work" ARCHES="$arch" "$script_dir/verify-jwt-plugin.sh"
  if [ "${EXPECT_TARGET_ONLY:-false}" = true ]; then
    docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec '
      . /openbao/jwt-plugin-versions.env
      test ! -e /openbao/plugins/vault-plugin-secrets-jwt
      test ! -e "/openbao/plugins/vault-plugin-secrets-jwt-$JWT_PLUGIN_OLD_VERSION"
      test -x "/openbao/plugins/vault-plugin-secrets-jwt-$JWT_PLUGIN_TARGET_VERSION"
    '
  fi
  if [ -n "${BASELINE_IMAGE:-}" ]; then
    baseline=$(docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$BASELINE_IMAGE" -ec \
      'sha256sum /openbao/plugins/vault-plugin-secrets-jwt')
    current=$(docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$RUNTIME_IMAGE" -ec \
      'sha256sum /openbao/plugins/vault-plugin-secrets-jwt')
    test "$current" = "$baseline" || {
      echo "legacy JWT plugin executable changed from baseline" >&2
      exit 1
    }
    printf '%s\n' "$current"
    echo "legacy JWT plugin bytes match baseline"
  fi
else
  echo "runtime package source contract passed; set RUNTIME_IMAGE for image verification"
fi
