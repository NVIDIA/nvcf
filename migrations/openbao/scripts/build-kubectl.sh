#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

kubectl_version=${KUBECTL_VERSION:-v1.36.5}
kubectl_source_commit=${KUBECTL_SOURCE_COMMIT:-ad950d1cc78b0183c476bd4d3f1934c104229727}
kubectl_source_sha256=${KUBECTL_SOURCE_SHA256:-168162db6254cf3c453798de09a3a65f3d11673e0d3e1705fe061d960b05d137}
kubectl_build_date=${KUBECTL_BUILD_DATE:-2026-09-23T17:07:00Z}
kubectl_source_date_epoch=${KUBECTL_SOURCE_DATE_EPOCH:-1790183220}
x_net_version=${X_NET_VERSION:-v0.60.0}
target_arch=${TARGETARCH:?TARGETARCH must be set}
output_dir=${OUTPUT_DIR:-/out}
source_url=${KUBECTL_SOURCE_URL:-"https://dl.k8s.io/${kubectl_version}/kubernetes-src.tar.gz"}

case "$target_arch" in
  amd64 | arm64) ;;
  *)
    echo "unsupported kubectl architecture: $target_arch" >&2
    exit 1
    ;;
esac

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-kubectl-source.XXXXXX")
cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

archive="$work_dir/kubernetes-src.tar.gz"
source_dir="$work_dir/source"
mkdir -p "$source_dir" "$output_dir"

curl --fail --location --silent --show-error "$source_url" --output "$archive"
printf '%s  %s\n' "$kubectl_source_sha256" "$archive" | sha256sum -c -
tar -xzf "$archive" -C "$source_dir"

version_ldflags=""
for package in k8s.io/client-go/pkg/version k8s.io/component-base/version; do
  version_ldflags="$version_ldflags -X ${package}.gitVersion=${kubectl_version}"
  version_ldflags="$version_ldflags -X ${package}.gitCommit=${kubectl_source_commit}"
  version_ldflags="$version_ldflags -X ${package}.gitTreeState=dirty"
  version_ldflags="$version_ldflags -X ${package}.buildDate=${kubectl_build_date}"
  version_ldflags="$version_ldflags -X ${package}.gitMajor=1"
  version_ldflags="$version_ldflags -X ${package}.gitMinor=36"
done

(
  cd "$source_dir"
  # Update the workspace dependency graph before regenerating vendor. Building
  # offline below then uses exactly this patched graph, including staging modules.
  GOTOOLCHAIN=local go mod edit -require="golang.org/x/net@${x_net_version}"
  GOTOOLCHAIN=local GOWORK="$source_dir/go.work" go work sync
  GOTOOLCHAIN=local GOWORK="$source_dir/go.work" go work vendor
  CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH="$target_arch" \
    GOTOOLCHAIN=local \
    GOPROXY=off \
    SOURCE_DATE_EPOCH="$kubectl_source_date_epoch" \
    go build \
      -mod=vendor \
      -trimpath \
      -buildvcs=false \
      -tags=selinux,notest,grpcnotrace \
      -ldflags="-s -w -buildid=${version_ldflags}" \
      -o "$output_dir/kubectl" \
      ./cmd/kubectl
)

chmod 0555 "$output_dir/kubectl"
