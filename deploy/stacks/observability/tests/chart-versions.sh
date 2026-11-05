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
# Compare each chart with every published stack release and release candidate,
# on any train. Fail when the chart content differs from a release but keeps
# that release's chart version or appVersion, so no release can ship new chart
# content under an already published version. Every chart must declare an
# appVersion, since it moves with the chart version.
set -euo pipefail

# Inspect the repository that holds this script, not one named by variables
# inherited from the caller, such as from a Git hook.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_INDEX_FILE

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

release_tags="$(
  git -C "$repo_dir" tag --list "$stack_path/v*" |
    grep -E "^$stack_path/v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$" || true
)"
if [[ -z "$release_tags" ]]; then
  echo "chart-versions: skipped, no $stack_path release tag; fetch tags to run this check"
  exit 0
fi

# chart_changed_since <tag> <chart path> succeeds when the working tree chart
# differs from the chart at <tag>, including untracked files.
chart_changed_since() {
  local tag="$1" chart_path="$2" status=0
  if [[ -n "$(git -C "$repo_dir" ls-files --others --exclude-standard -- "$chart_path")" ]]; then
    return 0
  fi
  git -C "$repo_dir" diff --quiet "$tag" -- "$chart_path" || status=$?
  case "$status" in
    0) return 1 ;;
    1) return 0 ;;
    *) fail "git diff against $tag failed for $chart_path" ;;
  esac
}

release_count="$(wc -l <<<"$release_tags" | tr -d ' ')"
for chart_yaml in "$stack_dir"/charts/*/Chart.yaml; do
  chart_path="$(dirname "${chart_yaml#"$repo_dir"/}")"
  current_version="$(yq -r '.version' "$chart_yaml")"
  current_app_version="$(yq -r '.appVersion // ""' "$chart_yaml")"
  [[ -n "$current_app_version" ]] ||
    fail "$chart_path/Chart.yaml has no appVersion; set it and bump it with version"
  while IFS= read -r tag; do
    released_chart_yaml="$(git -C "$repo_dir" show "$tag:$chart_path/Chart.yaml" 2>/dev/null)" ||
      continue
    released_version="$(yq -r '.version' <<<"$released_chart_yaml")"
    released_app_version="$(yq -r '.appVersion' <<<"$released_chart_yaml")"
    same_version=false
    same_app_version=false
    [[ "$current_version" == "$released_version" ]] && same_version=true
    [[ "$current_app_version" == "$released_app_version" ]] && same_app_version=true
    if [[ "$same_version" == false && "$same_app_version" == false ]]; then
      continue
    fi
    chart_changed_since "$tag" "$chart_path" || continue
    if [[ "$same_version" == true ]]; then
      fail "$chart_path differs from $tag but keeps its chart version $current_version; bump version and appVersion in $chart_path/Chart.yaml"
    fi
    fail "$chart_path differs from $tag but keeps its appVersion $current_app_version; bump appVersion with version in $chart_path/Chart.yaml"
  done <<<"$release_tags"
done

echo "chart-versions: all checks passed against $release_count $stack_path releases"
