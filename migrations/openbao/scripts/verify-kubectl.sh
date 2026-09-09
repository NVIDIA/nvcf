#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

kubectl=${KUBECTL_BINARY:-/out/kubectl}
kubectl_go_version=${KUBECTL_GO_VERSION:-go1.26.6}
target_arch=${TARGETARCH:?TARGETARCH must be set}
metadata=$(mktemp)
trap 'rm -f "$metadata"' EXIT INT TERM

go version -m "$kubectl" >"$metadata"

actual_go_version=$(awk 'NR == 1 { print $2 }' "$metadata")
path=$(awk '$1 == "path" { print $2 }' "$metadata")
goos=$(awk '$1 == "build" && $2 ~ /^GOOS=/ { sub(/^GOOS=/, "", $2); print $2 }' "$metadata")
goarch=$(awk '$1 == "build" && $2 ~ /^GOARCH=/ { sub(/^GOARCH=/, "", $2); print $2 }' "$metadata")
cgo_enabled=$(awk '$1 == "build" && $2 ~ /^CGO_ENABLED=/ { sub(/^CGO_ENABLED=/, "", $2); print $2 }' "$metadata")

if [ "$actual_go_version" != "$kubectl_go_version" ]; then
  echo "$kubectl embeds $actual_go_version; expected $kubectl_go_version" >&2
  exit 1
fi
if [ "$path" != "k8s.io/kubernetes/cmd/kubectl" ]; then
  echo "$kubectl has unexpected module path: $path" >&2
  exit 1
fi
if [ "$goos" != "linux" ] || [ "$goarch" != "$target_arch" ] || [ "$cgo_enabled" != "0" ]; then
  echo "$kubectl has unexpected target metadata: GOOS=$goos GOARCH=$goarch CGO_ENABLED=$cgo_enabled" >&2
  exit 1
fi

echo "verified $kubectl: $kubectl_go_version linux/$target_arch"
