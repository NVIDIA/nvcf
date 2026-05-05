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

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_ROOT"

CHECK_MODE=false
if [[ "${1:-}" == "--check" ]]; then
    CHECK_MODE=true
fi

LICENSE_TEXT="SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the \"License\");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an \"AS IS\" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License."

SPDX_LINE="SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved."

make_hash_header() {
    local result=""
    while IFS= read -r line; do
        if [ -z "$line" ]; then
            result+="#
"
        else
            result+="# $line
"
        fi
    done <<< "$LICENSE_TEXT"
    printf '%s' "$result"
}

make_block_header() {
    local result="/*
"
    while IFS= read -r line; do
        result+="$line
"
    done <<< "$LICENSE_TEXT"
    result+="*/
"
    printf '%s' "$result"
}

make_html_header() {
    local result="<!--
"
    while IFS= read -r line; do
        result+="$line
"
    done <<< "$LICENSE_TEXT"
    result+="-->
"
    printf '%s' "$result"
}

HASH_HEADER="$(make_hash_header)"
BLOCK_HEADER="$(make_block_header)"
HTML_HEADER="$(make_html_header)"

SKIP_FILES=("go.sum" "go.mod")

# SKIP_PATH_PREFIXES excludes whole directories from the copyright check.
# Used for content mirrored in from another repo where the copy must remain
# byte-identical to the upstream (a SPDX header would corrupt the bytes and
# break the manifest verification chain).
#
# internal/agentskill/data/ — agent-skill markdown bundle copied from
#   mcamp/docs/nvcf/one-click-deploy/agent-skill/. The manifest.json there
#   records SHA256 per file; CI fails if any byte drifts.
SKIP_PATH_PREFIXES=("internal/agentskill/data/")

FAILED=0

while IFS= read -r file; do
    basename="$(basename "$file")"
    ext="${basename##*.}"
    if [ "$ext" = "$basename" ]; then
        ext=""
    fi

    skip=false
    for sf in "${SKIP_FILES[@]}"; do
        if [ "$basename" = "$sf" ]; then
            skip=true
            break
        fi
    done
    if ! $skip; then
        for sp in "${SKIP_PATH_PREFIXES[@]}"; do
            case "$file" in
                "$sp"*) skip=true; break ;;
            esac
        done
    fi
    $skip && continue

    header=""
    case "$ext" in
        yaml|yml|sh|py|gitignore|dockerignore)
            header="$HASH_HEADER"
            ;;
        go)
            header="$BLOCK_HEADER"
            ;;
        md)
            header="$HTML_HEADER"
            ;;
        *)
            case "$basename" in
                Makefile|Dockerfile)
                    header="$HASH_HEADER"
                    ;;
                *)
                    continue
                    ;;
            esac
            ;;
    esac

    if $CHECK_MODE; then
        if ! grep -qF "$SPDX_LINE" "$file" 2>/dev/null; then
            echo "MISSING header: $file"
            FAILED=1
        fi
        continue
    fi

    if grep -qF "$SPDX_LINE" "$file" 2>/dev/null; then
        continue
    fi

    content="$(cat "$file")"
    shebang=""
    if [[ "$content" == "#!"* ]]; then
        shebang="$(head -1 "$file")"
        content="$(tail -n +2 "$file")"
    fi

    {
        if [ -n "$shebang" ]; then
            printf '%s\n' "$shebang"
        fi
        printf '%s\n' "$header"
        printf '%s\n' "$content"
    } > "$file"

    echo "UPDATED $file"
done < <(git ls-files)

if $CHECK_MODE && [ "$FAILED" -ne 0 ]; then
    echo ""
    echo "ERROR: Files missing copyright header. Run ./add-copyright.sh to fix."
    exit 1
fi

if $CHECK_MODE; then
    echo "All files have copyright headers."
fi
