#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

kubectl=${KUBECTL_BINARY:-/out/kubectl}
kubectl_go_version=${KUBECTL_GO_VERSION:-go1.26.10}
go_bin=${GO:-go}
target_arch=${TARGETARCH:?TARGETARCH must be set}
metadata=$(mktemp)
trap 'rm -f "$metadata"' EXIT INT TERM

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
"$script_dir/verify-go-security.sh" "$kubectl"

"$go_bin" version -m "$kubectl" >"$metadata"

# Check the linked modules, not just the source graph. Reject replacements,
# missing core modules, and mixed releases that could reintroduce GO-2026-5158.
# The experimental metric/x module uses a separate v0 release series.
awk '
  $1 == "dep" && $2 ~ /^go.opentelemetry.io\/otel(\/|$)/ &&
      $2 != "go.opentelemetry.io/otel/metric/x" {
    version = $3
    if (version !~ /^v1\.[0-9]+\.[0-9]+$/) exit 1
    split(version, parts, ".")
    if (parts[2] < 44) exit 1
    if (release != "" && release != version) exit 1
    release = version
    modules[$2] = 1
    otel = 1
    next
  }
  otel && $1 == "=>" { exit 1 }
  { otel = 0 }
  END {
    if (!modules["go.opentelemetry.io/otel"] ||
        !modules["go.opentelemetry.io/otel/trace"]) exit 1
  }
' "$metadata" || {
  echo "$kubectl must embed aligned OpenTelemetry modules at v1.44.0 or newer" >&2
  awk '$1 == "dep" && $2 ~ /^go.opentelemetry.io\/otel(\/|$)/ { print $2, $3 }' "$metadata" >&2
  exit 1
}

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
