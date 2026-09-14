#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-api" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

render_job() {
  local output_file="$1"
  shift

  helm template api "$chart_dir" \
    --namespace nvcf \
    --set-string api.image.registry=example.com \
    --set-string api.image.repository=nvidia/nvcf-api \
    --show-only templates/account-bootstrap-hook-job.yaml \
    "$@" >"$output_file"
}

assert_image() {
  local manifest="$1"
  local expected="$2"

  grep -Fq "image: \"$expected\"" "$manifest" || {
    echo "expected account-bootstrap image $expected" >&2
    exit 1
  }
}

render_job "$work_dir/default.yaml"
assert_image "$work_dir/default.yaml" "docker.io/alpine/k8s:1.37.0"

render_job "$work_dir/partial-override.yaml" \
  --set-string api.accountBootstrap.image.repository=mirror/alpine-k8s
assert_image "$work_dir/partial-override.yaml" "docker.io/mirror/alpine-k8s:1.37.0"

render_job "$work_dir/override.yaml" \
  --set-string api.accountBootstrap.image.registry=mirror.example.com \
  --set-string api.accountBootstrap.image.repository=mirror/alpine-k8s \
  --set-string api.accountBootstrap.image.tag=9.9.9
assert_image "$work_dir/override.yaml" "mirror.example.com/mirror/alpine-k8s:9.9.9"

echo "account-bootstrap image tests passed"
