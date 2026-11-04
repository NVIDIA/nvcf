#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# PostToolUse hook: format the file Claude just wrote or edited.
# Reads the hook event JSON from stdin. A failure is reported as a
# non-blocking hook error; `task verify` remains the gate.
set -uo pipefail

file=$(jq -r '.tool_input.file_path // empty')
project_dir="${CLAUDE_PROJECT_DIR:?}"
ui_dir="$project_dir/ui"

# Only act on files inside this project, and never on traversal paths.
case "$file" in
"" | *..*) exit 0 ;;
"$project_dir"/*) ;;
*) exit 0 ;;
esac

case "$file" in
"$ui_dir"/*.ts | "$ui_dir"/*.tsx | "$ui_dir"/*.js | "$ui_dir"/*.jsx | "$ui_dir"/*.json | "$ui_dir"/*.css)
	cd "$ui_dir" && pnpm exec biome check --write --no-errors-on-unmatched \
		--files-ignore-unknown=true "$file"
	;;
*.go)
	gofmt -w "$file"
	;;
esac
