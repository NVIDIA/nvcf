#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

kubectl_version=${KUBECTL_VERSION:-v1.36.4}
kubectl_source_commit=${KUBECTL_SOURCE_COMMIT:-bb826b1d48562f110659e64e8ec444327433db95}
kubectl_source_sha256=${KUBECTL_SOURCE_SHA256:-3c28f11492472df48e658551bf268fd92938b127b0f9dcef7090ac800318c821}
kubectl_build_date=${KUBECTL_BUILD_DATE:-2026-08-20T03:09:25Z}
kubectl_source_date_epoch=${KUBECTL_SOURCE_DATE_EPOCH:-1787195365}
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
  version_ldflags="$version_ldflags -X ${package}.gitTreeState=clean"
  version_ldflags="$version_ldflags -X ${package}.buildDate=${kubectl_build_date}"
  version_ldflags="$version_ldflags -X ${package}.gitMajor=1"
  version_ldflags="$version_ldflags -X ${package}.gitMinor=36"
done

(
  cd "$source_dir"
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
