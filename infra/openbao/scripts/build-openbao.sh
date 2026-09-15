#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)

bao_version=${BAO_VERSION:-2.6.2}
bao_source_commit=${BAO_SOURCE_COMMIT:-dd9c19c37a878cf4a81b18efb8d6f0599c7da923}
bao_source_sha256=${BAO_SOURCE_SHA256:-a7784550a9db16f24e99d65a18c9b12a433707c79ef4c1f34262d3f48171c7a9}
bao_commit_date=${BAO_COMMIT_DATE:-2026-08-18T15:43:05Z}
go_toolchain=${GO_TOOLCHAIN:-go1.27.0}
x_crypto_version=${X_CRYPTO_VERSION:-v0.56.0}
grpc_version=${GRPC_VERSION:-v1.83.2}
go_archive_version=${GO_ARCHIVE_VERSION:-v0.3.0}
output_dir=${OUTPUT_DIR:-"$repo_root/files/openbao"}
source_url=${BAO_SOURCE_URL:-"https://github.com/openbao/openbao/releases/download/v${bao_version}/openbao-dist-v${bao_version}.tar.xz"}

case "$output_dir" in
  /*) ;;
  *) output_dir="$(pwd)/$output_dir" ;;
esac

if [ -n "${WORK_DIR:-}" ]; then
  work_dir=$WORK_DIR
  work_dir_is_ours=0
else
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-openbao-source.XXXXXX")
  work_dir_is_ours=1
fi

cleanup() {
  if [ -z "${KEEP_WORK_DIR:-}" ] && [ "$work_dir_is_ours" = "1" ]; then
    rm -rf "$work_dir"
  else
    echo "Keeping work dir: $work_dir"
  fi
}
trap cleanup EXIT INT TERM

archive="$work_dir/openbao.tar.xz"
source_dir="$work_dir/source"
mkdir -p "$source_dir" "$output_dir"

curl --fail --location --silent --show-error "$source_url" --output "$archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual_source_sha256=$(sha256sum "$archive" | awk '{ print $1 }')
else
  actual_source_sha256=$(shasum -a 256 "$archive" | awk '{ print $1 }')
fi
if [ "$actual_source_sha256" != "$bao_source_sha256" ]; then
  echo "OpenBao source checksum mismatch: got $actual_source_sha256, expected $bao_source_sha256" >&2
  exit 1
fi
tar -xJf "$archive" --strip-components=1 -C "$source_dir"

(
  cd "$source_dir"
  GOTOOLCHAIN="$go_toolchain" GOFLAGS=-mod=mod go get \
    "golang.org/x/crypto@${x_crypto_version}" \
    "google.golang.org/grpc@${grpc_version}" \
    "github.com/moby/go-archive@${go_archive_version}"
  GOTOOLCHAIN="$go_toolchain" GOFLAGS=-mod=mod go mod tidy
  GOTOOLCHAIN="$go_toolchain" GOFLAGS=-mod=mod go mod verify

  if [ -n "${TARGETARCH:-}" ]; then
    arches=$TARGETARCH
  else
    arches="amd64 arm64"
  fi

  for arch in $arches; do
    GOTOOLCHAIN="$go_toolchain" CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
      -mod=mod \
      -buildvcs=false \
      -trimpath \
      -tags ui \
      -ldflags "-s -w -X github.com/openbao/openbao/version.fullVersion=${bao_version} -X github.com/openbao/openbao/version.GitCommit=${bao_source_commit} -X github.com/openbao/openbao/version.CommitDate=${bao_commit_date}" \
      -o "$output_dir/bao-linux-${arch}" \
      .
    chmod 555 "$output_dir/bao-linux-${arch}"
  done
)

BAO_DIR="$output_dir" ARCHES="${TARGETARCH:-amd64 arm64}" "$script_dir/verify-openbao.sh"
