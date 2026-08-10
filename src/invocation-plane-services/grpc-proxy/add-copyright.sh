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
#
# Insert or verify SPDX copyright headers on every tracked source file.
# Run without arguments to stamp missing headers. Run with --check to
# exit non-zero if any tracked file is missing the SPDX line; CI uses
# this mode as a merge gate (see .gitlab-ci.yml copyright-check job).
#
# This script is the canonical template from the nvcf-oss-prep skill;
# keep changes coordinated across sibling NVCF repos so the SPDX line
# stays identical everywhere.
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

make_slash_header() {
    local result=""
    while IFS= read -r line; do
        if [ -z "$line" ]; then
            result+="//
"
        else
            result+="// $line
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

make_dash_header() {
    local result=""
    while IFS= read -r line; do
        if [ -z "$line" ]; then
            result+="--
"
        else
            result+="-- $line
"
        fi
    done <<< "$LICENSE_TEXT"
    printf '%s' "$result"
}

HASH_HEADER="$(make_hash_header)"
SLASH_HEADER="$(make_slash_header)"
BLOCK_HEADER="$(make_block_header)"
HTML_HEADER="$(make_html_header)"
DASH_HEADER="$(make_dash_header)"

# Files we never stamp:
# - Lockfiles + manifest files that do not support comments.
# - .gitkeep (placeholder, no semantic content).
# - req.csr (PEM-encoded certificate request).
# - README.md (already covered by LICENSE; HTML comment above the H1 is noise).
# - nvcf-service / nvcf-ratelimiter (compiled binaries left in the worktree).
# - MODULE.bazel.lock (Bazel-generated dep lock; equivalent to go.sum).
# - .bazelversion (single version-string line; Bazelisk parses the full file).
SKIP_FILES=(
    "go.sum" "go.mod" "package-lock.json" "yarn.lock" "pnpm-lock.yaml"
    "Cargo.lock" "poetry.lock" "Gemfile.lock" "composer.lock"
    "package.json" "tsconfig.json" "tslint.json"
    ".gitkeep"
    "req.csr"
    "NOTES.txt"
    "README.md"
    "nvcf-service"
    "nvcf-ratelimiter"
    "MODULE.bazel.lock"
    ".bazelversion"
)

# Path-glob skip patterns for binary or vendored subtrees. None currently
# needed in this repo; kept so future additions (keystores, vendored
# upstreams) have an obvious home.
SKIP_PATH_PATTERNS=()

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
    $skip && continue

    for pat in "${SKIP_PATH_PATTERNS[@]}"; do
        # shellcheck disable=SC2053
        if [[ "$file" == $pat ]]; then
            skip=true
            break
        fi
    done
    $skip && continue

    header=""
    case "$ext" in
        # Block comment /* ... */
        go|java|scala|kt|kts|groovy|c|h|cpp|cc|cxx|hpp|cs|swift|css|scss|less|cu)
            header="$BLOCK_HEADER"
            ;;
        # Slash comment // ...
        js|jsx|ts|tsx|mjs|cjs|proto|rs|dart|zig)
            header="$SLASH_HEADER"
            ;;
        # Hash comment # ...
        # bazelrc / bazelignore: Bazelisk accepts `#` comments on these.
        # bzl / bazel: Starlark and BUILD files.
        py|rb|pl|pm|sh|bash|zsh|fish|yaml|yml|toml|cfg|ini|conf|properties|hcl|tf|tfvars|bzl|bazel|bazelrc|bazelignore|nix|r|R|cmake|mk|ex|exs|gitignore|dockerignore|gitmodules|gitattributes|editorconfig|envrc|helmignore)
            header="$HASH_HEADER"
            ;;
        # HTML comment <!-- ... -->
        md|mdx|html|htm|xml|svg|vue)
            header="$HTML_HEADER"
            ;;
        # Dash comment -- ...
        sql|lua|hs|cql)
            header="$DASH_HEADER"
            ;;
        # .env files
        env|env.*)
            header="$HASH_HEADER"
            ;;
        # Template files: skip; need engine-native comments, stamp manually.
        tmpl|tpl|gotmpl|ctmpl)
            continue
            ;;
        *)
            case "$basename" in
                Dockerfile|Dockerfile.*|Containerfile|Containerfile.*)
                    header="$HASH_HEADER"
                    ;;
                Makefile|Jenkinsfile|Vagrantfile|Rakefile|Gemfile|Justfile)
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
    # Preserve a leading shebang OR XML declaration on line 1 (must be the
    # very first thing in the file). Header is inserted on line 2.
    preamble=""
    if [[ "$content" == "#!"* ]] || [[ "$content" == "<?xml"* ]]; then
        preamble="$(head -1 "$file")"
        content="$(tail -n +2 "$file")"
    fi

    {
        if [ -n "$preamble" ]; then
            printf '%s\n' "$preamble"
        fi
        printf '%s\n\n' "$header"
        printf '%s\n' "$content"
    } > "$file"

    echo "UPDATED $file"
done < <(git ls-files)

if $CHECK_MODE && [ "$FAILED" -ne 0 ]; then
    echo ""
    echo "ERROR: Files missing copyright header. Run ./add-copyright.sh to fix."
    exit 1
fi
