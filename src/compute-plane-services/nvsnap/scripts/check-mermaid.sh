#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render every ```mermaid block in the given Markdown files with the
# Mermaid CLI so a diagram that GitHub cannot draw fails here first.
# Needs mmdc (npm i -g @mermaid-js/mermaid-cli); skips with a notice when
# it is absent so the check never blocks a machine without it.
set -euo pipefail
if ! command -v mmdc >/dev/null 2>&1; then
  echo "check-mermaid: mmdc not installed; skipping" >&2
  exit 0
fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
echo '{"args":["--no-sandbox","--disable-setuid-sandbox"]}' > "$tmp/pp.json"
rc=0
for md in "$@"; do
  n=0
  awk -v out="$tmp/$(basename "$md")" '
    /^```mermaid/ {f=1; n++; file=out "." n ".mmd"; next}
    /^```/ && f {f=0; close(file); next}
    f {print > file}' "$md"
  for mmd in "$tmp/$(basename "$md")".*.mmd; do
    [ -e "$mmd" ] || continue
    n=$((n+1))
    if mmdc -q -p "$tmp/pp.json" -i "$mmd" -o "$mmd.svg" >"$mmd.log" 2>&1 && [ -s "$mmd.svg" ]; then
      echo "ok   $md block $n"
    else
      echo "FAIL $md block $n"; grep -iE "parse error|expecting" "$mmd.log" | head -2; rc=1
    fi
  done
done
exit $rc
