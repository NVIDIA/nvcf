#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../helm" && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

# Discover defaults through the catalog paths, then assert the packaged chart
# renders exactly those images, including hooks and init containers.
helm package "$chart_dir" --destination "$test_root" >/dev/null
chart_package=("$test_root"/*.tgz)
values="$chart_dir/values.yaml"
: > "$test_root/default-images"
while IFS= read -r image_path; do
    image="$(yq -r ".$image_path | .registry + \"/\" + .repository + \":\" + .tag" "$values")"
    [[ "$image" != /* && "$image" != *null* && "$image" != *'//'* ]]
    printf '%s\n' "$image" >> "$test_root/default-images"
done < <(yq -r '.ownedImages[]' "$values")
LC_ALL=C sort -u "$test_root/default-images" > "$test_root/expected-defaults"
test "$(wc -l < "$test_root/expected-defaults" | tr -d ' ')" = 2

assert_images() {
    local expected="$1"
    shift
    helm template cassandra "${chart_package[0]}" --namespace image-test "$@" > "$test_root/rendered.yaml"
    yq -N -r '.. | select(tag == "!!map" and has("image")) | .image' "$test_root/rendered.yaml" \
        | LC_ALL=C sort -u > "$test_root/actual-images"
    diff -u "$expected" "$test_root/actual-images"
}

assert_images "$test_root/expected-defaults"

digest="$(printf '%064d' 0)"
cat > "$test_root/catalog.yaml" <<EOF
cassandra:
  image:
    registry: registry.example:5443
    repository: renamed/database
    tag: candidate@sha256:$digest
  migrations:
    image:
      registry: registry.example:5443
      repository: renamed/schema
      tag: candidate@sha256:$digest
EOF
printf '%s\n' \
    "registry.example:5443/renamed/database:candidate@sha256:$digest" \
    "registry.example:5443/renamed/schema:candidate@sha256:$digest" \
    | LC_ALL=C sort > "$test_root/expected-overrides"
assert_images "$test_root/expected-overrides" -f "$test_root/catalog.yaml"

# A user's later file must win over the complete image-selection profile.
cat > "$test_root/operator.yaml" <<'EOF'
cassandra:
  image:
    registry: operator.example
    repository: custom/database
    tag: operator-patch
EOF
printf '%s\n' \
    'operator.example/custom/database:operator-patch' \
    "registry.example:5443/renamed/schema:candidate@sha256:$digest" \
    | LC_ALL=C sort > "$test_root/expected-operator"
assert_images "$test_root/expected-operator" -f "$test_root/catalog.yaml" -f "$test_root/operator.yaml"

printf '%s\n' 'PASS: packaged Cassandra defaults, catalog coverage and ordinary image overrides'
