#!/bin/sh
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

set -eu

header_file=$1
shift

strip_existing_header() {
  file=$1
  start_line=$2

  if [ "$(sed -n "${start_line}p" "$file")" != "# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved." ]; then
    cat "$file"
    return
  fi

  awk -v start="$start_line" '
    NR < start { print; next }
    NR == start && $0 !~ /^# SPDX-FileCopyrightText:/ { print; next }
    NR >= start && skipping {
      if ($0 == "# limitations under the License.") {
        skipping = 0
        skip_blank = 1
      }
      next
    }
    NR == start { skipping = 1; next }
    skip_blank && $0 == "" { skip_blank = 0; next }
    { print }
  ' "$file"
}

for file in "$@"; do
  tmp=$(mktemp)
  cleaned=$(mktemp)

  if sed -n '1p' "$file" | grep -q '^#!'; then
    sed -n '1p' "$file" > "$tmp"
    cat "$header_file" >> "$tmp"
    strip_existing_header "$file" 2 > "$cleaned"
    sed '1d' "$cleaned" | cat >> "$tmp"
  else
    strip_existing_header "$file" 1 > "$cleaned"
    cat "$header_file" > "$tmp"
    cat "$cleaned" >> "$tmp"
  fi

  # Use cat-redirect rather than `mv` so the destination's mode (and any
  # other file metadata) is preserved. mktemp creates files at 0600; an
  # `mv` would silently clobber the executable bit on tracked scripts.
  cat "$tmp" > "$file"
  rm -f "$tmp" "$cleaned"
done
