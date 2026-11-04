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
# A stack release publishes each chart under charts/ at its Chart.yaml version.
# Fail when a chart differs from the latest stack release but keeps the chart
# version of that release, so the next release cannot ship new chart content
# under an already published version.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
stack_path="${stack_dir#"$repo_dir"/}"

fail() {
  echo "chart-versions: $*" >&2
  exit 1
}

command -v yq >/dev/null 2>&1 || fail "yq is required"

if ! git -C "$repo_dir" rev-parse --git-dir >/dev/null 2>&1; then
  echo "chart-versions: skipped, $repo_dir is not a git checkout"
  exit 0
fi

release_tag="$(
  git -C "$repo_dir" tag --list "$stack_path/v*" --sort=-v:refname |
    grep -E "^$stack_path/v[0-9]+\.[0-9]+\.[0-9]+$" |
    head -n 1 || true
)"
if [[ -z "$release_tag" ]]; then
  echo "chart-versions: skipped, no $stack_path release tag; fetch tags to run this check"
  exit 0
fi

for chart_yaml in "$stack_dir"/charts/*/Chart.yaml; do
  chart_path="$(dirname "${chart_yaml#"$repo_dir"/}")"
  released_chart_yaml="$(git -C "$repo_dir" show "$release_tag:$chart_path/Chart.yaml" 2>/dev/null)" ||
    continue
  current_version="$(yq -r '.version' "$chart_yaml")"
  released_version="$(yq -r '.version' <<<"$released_chart_yaml")"
  [[ "$current_version" == "$released_version" ]] || continue

  status=0
  git -C "$repo_dir" diff --quiet "$release_tag" -- "$chart_path" || status=$?
  case "$status" in
    0) ;;
    1)
      fail "$chart_path changed since $release_tag but keeps chart version $current_version; bump version and appVersion in $chart_path/Chart.yaml"
      ;;
    *) fail "git diff against $release_tag failed for $chart_path" ;;
  esac
done

echo "chart-versions: all checks passed against $release_tag"
