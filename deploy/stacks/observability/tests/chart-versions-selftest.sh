#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Run tests/chart-versions.sh against a scratch repository with its own stack
# release tags.
set -euo pipefail

tests_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "chart-versions-selftest: $*" >&2
  exit 1
}

# Keep the scratch repository independent of the caller's Git settings.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_INDEX_FILE
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=selftest GIT_AUTHOR_EMAIL=selftest@example.com
export GIT_COMMITTER_NAME=selftest GIT_COMMITTER_EMAIL=selftest@example.com

repo="$work_dir/repo"
stack="$repo/deploy/stacks/observability"
chart="$stack/charts/demo"
tag_prefix="deploy/stacks/observability"
mkdir -p "$stack/tests" "$chart"
cp "$tests_dir/chart-versions.sh" "$stack/tests/"
git -C "$repo" init -q
git -C "$repo" symbolic-ref HEAD refs/heads/main

set_chart() {
  printf 'apiVersion: v2\nname: demo\nversion: %s\nappVersion: "%s"\n' "$1" "$2" >"$chart/Chart.yaml"
}

set_value() {
  printf 'value: %s\n' "$1" >"$chart/values.yaml"
}

commit() {
  git -C "$repo" add -A
  git -C "$repo" commit -q -m "$1"
}

expect() {
  local want="$1" name="$2" pattern="$3" output status=0
  output="$("$stack/tests/chart-versions.sh" 2>&1)" || status=$?
  if [[ "$want" == pass && "$status" -ne 0 ]]; then
    fail "$name: expected pass, got: $output"
  fi
  if [[ "$want" == fail && "$status" -eq 0 ]]; then
    fail "$name: expected failure, got: $output"
  fi
  [[ "$output" == *"$pattern"* ]] || fail "$name: expected '$pattern' in: $output"
}

set_chart 0.1.0 0.1.0
set_value 1
commit initial
expect pass 'no release tag' 'skipped'

git -C "$repo" tag "$tag_prefix/v1.0.0"
expect pass 'unchanged chart' 'all checks passed'

set_value 2
expect fail 'changed chart, same version' 'keeps its chart version 0.1.0'
set_chart 0.2.0 0.1.0
expect fail 'changed chart, same appVersion' 'keeps its appVersion 0.1.0'
set_chart 0.2.0 0.2.0
expect pass 'changed chart, bumped' 'all checks passed'
commit 'release 1.1'
git -C "$repo" tag "$tag_prefix/v1.1.0"

printf 'kind: ConfigMap\n' >"$chart/extra.yaml"
expect fail 'untracked chart file' "differs from $tag_prefix/v1.1.0"
rm "$chart/extra.yaml"

# A maintenance branch must not reuse its own train's chart version, even
# when a newer train holds the highest tag.
git -C "$repo" checkout -q -b release-1.0 "$tag_prefix/v1.0.0"
expect pass 'maintenance branch unchanged' 'all checks passed'
set_value 3
expect fail 'maintenance branch, same version' "differs from $tag_prefix/v1.0.0"
set_chart 0.1.1 0.1.1
expect pass 'maintenance branch, bumped' 'all checks passed'
commit 'release 1.0.1'
git -C "$repo" tag "$tag_prefix/v1.0.1"

git -C "$repo" checkout -q main
set_value 4
set_chart 0.3.0 0.3.0
commit 'release candidate'
git -C "$repo" tag "$tag_prefix/v1.2.0-rc.1"
set_value 5
expect fail 'changed after release candidate' "differs from $tag_prefix/v1.2.0-rc.1"

# Repository variables inherited from the caller, such as from a Git hook,
# must not point the check at another repository without release tags.
other="$work_dir/other"
git init -q "$other"
(
  export GIT_DIR="$other/.git" GIT_WORK_TREE="$other"
  expect fail 'inherited repository variables' "differs from $tag_prefix/v1.2.0-rc.1"
)

# Every chart bumps appVersion with version, so a chart must declare one.
printf 'apiVersion: v2\nname: demo\nversion: 0.4.0\n' >"$chart/Chart.yaml"
expect fail 'missing appVersion' 'has no appVersion'

echo "chart-versions-selftest: all checks passed"
