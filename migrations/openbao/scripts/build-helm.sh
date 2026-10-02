#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
helm_version=v3.22.0
helm_source_commit=144ca65f8501953fa8b41cd1d37c7223051c85b7
helm_source_sha256=b418a3db1021d5ec9b4f906da1b0a97ce85fab319d89a7046842feef8e00eeba
grpc_version=v1.83.2
output_dir=${OUTPUT_DIR:-/out}
work_dir=${WORK_DIR:-$(mktemp -d)}

mkdir -p "$work_dir/source" "$output_dir"
curl --fail --location --silent --show-error --retry 3 \
  "https://github.com/helm/helm/archive/${helm_source_commit}.tar.gz" \
  --output "$work_dir/helm.tar.gz"
printf '%s  %s\n' "$helm_source_sha256" "$work_dir/helm.tar.gz" | sha256sum -c -
tar -xzf "$work_dir/helm.tar.gz" --strip-components=1 -C "$work_dir/source"

(
  cd "$work_dir/source"
  # Helm's release already updates or removes the other affected modules.
  # Its gRPC dependency still needs the same patched version used by bao.
  GOTOOLCHAIN=local go get "google.golang.org/grpc@${grpc_version}"
  GOTOOLCHAIN=local go mod tidy
  GOTOOLCHAIN=local go mod verify

  for arch in ${TARGETARCH:-amd64 arm64}; do
    case "$arch" in amd64|arm64) ;; *) echo "unsupported architecture: $arch" >&2; exit 1 ;; esac
    GOOS=linux GOARCH="$arch" GOTOOLCHAIN=local make build \
      BINDIR="$output_dir" BINNAME="helm-linux-$arch" \
      VERSION="$helm_version" VERSION_METADATA=nvcf \
      GIT_COMMIT="$helm_source_commit" GIT_DIRTY=dirty \
      GOFLAGS='-mod=readonly -buildvcs=false' CGO_ENABLED=0
  done
  cp LICENSE "$output_dir/helm-LICENSE"
)

HELM_DIR="$output_dir" ARCHES="${TARGETARCH:-amd64 arm64}" "$script_dir/verify-helm.sh"
