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

# nvcf-cli public skill installer.
#
# Downloads source skill files from the NVCF monorepo's ai-tooling/user/skills
# tree and writes them into agent-ecosystem skill directories. Use this when
# nvcf-cli is not installed yet. Operators with nvcf-cli should use
# 'nvcf-cli agent-skill install' instead.
#
# AUTHENTICATION:
#   This script fetches public files from github.com/NVIDIA/nvcf. Authentication
#   is optional; set GITHUB_TOKEN if your environment needs a higher GitHub API
#   rate limit.
#
# MANUAL SMOKE TEST:
#   export GITHUB_TOKEN=<optional-token>
#
#   # Install to a temp base skills dir:
#   bash scripts/install-agent-skill.sh --target=/tmp/skill-smoke
#   ls /tmp/skill-smoke/nvcf-self-managed-cli/
#   ls /tmp/skill-smoke/nvcf-self-managed-installation/
#
#   # Uninstall:
#   bash scripts/install-agent-skill.sh --uninstall --target=/tmp/skill-smoke
#
#   # Test default branch install:
#   bash scripts/install-agent-skill.sh
#   ls ~/.claude/skills/nvcf-self-managed-cli/
#   ls ~/.agents/skills/nvcf-self-managed-cli/
#
# CI SYNTAX CHECK:
#   bash -n scripts/install-agent-skill.sh

set -euo pipefail

DEFAULT_BRANCH="main"
GITHUB_API_URL="${GITHUB_API_URL:-https://api.github.com}"
GITHUB_REPO="${GITHUB_REPO:-NVIDIA/nvcf}"
SOURCE_PATH="ai-tooling/user/skills"
SKILL_NAMES=("nvcf-self-managed-cli" "nvcf-self-managed-installation")
MANIFEST_MARKER=".nvcf-cli-public-skills.manifest.json"
VERSION_MARKER=".nvcf-cli-public-skills.version"

BRANCH="$DEFAULT_BRANCH"
ACTION="install"
EXPLICIT_TARGET=""

usage() {
    cat <<'EOF'
Usage: install-agent-skill.sh [--branch=REF] [--target=DIR] [--uninstall] [--help]

Installs the public NVCF user skills into agent-ecosystem base skill
directories. With no args, writes skill directories into BOTH ~/.claude/skills/
and ~/.agents/skills/.

Options:
  --branch=REF      NVCF monorepo ref to fetch from (default: main)
  --target=DIR      install to a single base skills directory instead of the defaults
  --uninstall       remove these skills from the default targets, or --target if set
  --help            print this message and exit

Examples:
  curl -sSfL <URL> | bash
  curl -sSfL <URL> | bash -s -- --branch=feat/foo
  curl -sSfL <URL> | bash -s -- --uninstall
  curl -sSfL <URL> | bash -s -- --target=/path/to/skills

Environment:
  GITHUB_TOKEN      Optional GitHub token for higher API rate limits.
  GITHUB_REPO       Source repository in owner/name form (default: NVIDIA/nvcf).
  GITHUB_API_URL    GitHub API endpoint (default: https://api.github.com).

Source: github.com/NVIDIA/nvcf/ai-tooling/user/skills/
EOF
}

for arg in "$@"; do
    case "$arg" in
        --branch=*) BRANCH="${arg#*=}" ;;
        --target=*) EXPLICIT_TARGET="${arg#*=}" ;;
        --uninstall) ACTION="uninstall" ;;
        --help) usage; exit 0 ;;
        *)
            echo "Unknown arg: $arg" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [[ -n "$EXPLICIT_TARGET" ]] && [[ "$EXPLICIT_TARGET" == "~"* ]]; then
    EXPLICIT_TARGET="${EXPLICIT_TARGET/#~/$HOME}"
fi

declare -a TARGETS
if [[ -n "$EXPLICIT_TARGET" ]]; then
    TARGETS=("$EXPLICIT_TARGET")
else
    TARGETS=("$HOME/.claude/skills" "$HOME/.agents/skills")
fi

if [[ "$ACTION" == "uninstall" ]]; then
    for target in "${TARGETS[@]}"; do
        removed=false
        for skill in "${SKILL_NAMES[@]}"; do
            skill_target="$target/$skill"
            if [[ -d "$skill_target" ]]; then
                rm -rf "$skill_target"
                echo "Removed $skill_target"
                removed=true
            fi
        done
        if [[ -e "$target/$MANIFEST_MARKER" || -e "$target/$VERSION_MARKER" ]]; then
            rm -f "$target/$MANIFEST_MARKER" "$target/$VERSION_MARKER"
            echo "Removed install markers from $target"
            removed=true
        fi
        if [[ "$removed" == false ]]; then
            echo "Skipped $target (no installed public NVCF skills found)"
        fi
    done
    exit 0
fi

need() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Required tool not on PATH: $1" >&2
        exit 1
    }
}
need curl
need mktemp
need python3

declare -a CURL_AUTH_FLAGS
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    CURL_AUTH_FLAGS=(-H "Authorization: Bearer $GITHUB_TOKEN")
else
    CURL_AUTH_FLAGS=()
fi
CURL_JSON_FLAGS=("${CURL_AUTH_FLAGS[@]}" -H "Accept: application/vnd.github+json")
CURL_RAW_FLAGS=("${CURL_AUTH_FLAGS[@]}" -H "Accept: application/vnd.github.raw")

url_encode() {
    python3 - "$1" <<'PY'
import sys
import urllib.parse

print(urllib.parse.quote(sys.argv[1], safe=""))
PY
}

ref_encoded="$(url_encode "$BRANCH")"

github_raw_url() {
    local file_path="$1"
    printf '%s/repos/%s/contents/%s?ref=%s' \
        "${GITHUB_API_URL%/}" "$GITHUB_REPO" "$file_path" "$ref_encoded"
}

github_tree_url() {
    printf '%s/repos/%s/git/trees/%s?recursive=1' \
        "${GITHUB_API_URL%/}" "$GITHUB_REPO" "$ref_encoded"
}

validate_rel_path() {
    local rel="$1"
    case "$rel" in
        ""|/*|*\\*)
            return 1
            ;;
    esac

    IFS='/' read -r -a parts <<< "$rel"
    case "${parts[0]}" in
        nvcf-self-managed-cli|nvcf-self-managed-installation) ;;
        *) return 1 ;;
    esac
    for part in "${parts[@]}"; do
        case "$part" in
            ""|"."|".."|.*|_*)
                return 1
                ;;
        esac
    done
}

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

declare -a FILE_PATHS
declare -a REL_PATHS

echo ">>> Listing public NVCF skill files from $BRANCH..."
tree_json="$STAGE/tree.json"
paths_file="$STAGE/tree.paths"
curl -sSfL "${CURL_JSON_FLAGS[@]}" -o "$tree_json" "$(github_tree_url)"
python3 - "$tree_json" "$paths_file" "$SOURCE_PATH" "${SKILL_NAMES[@]}" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
if not isinstance(data, dict) or not isinstance(data.get("tree"), list):
    message = data.get("message", data) if isinstance(data, dict) else data
    raise SystemExit(f"repository tree response was not a GitHub tree: {message}")
if data.get("truncated"):
    raise SystemExit("GitHub tree response was truncated; use a narrower source repo or ref")
source_path = sys.argv[3].strip("/")
skills = sys.argv[4:]
prefixes = tuple(f"{source_path}/{skill}/" for skill in skills)
with open(sys.argv[2], "w", encoding="utf-8") as out:
    for item in data["tree"]:
        path = item.get("path", "")
        if item.get("type") == "blob" and path.startswith(prefixes):
            print(path, file=out)
PY
while IFS= read -r file_path; do
    rel="${file_path#"$SOURCE_PATH"/}"
    if [[ "$rel" == "$file_path" ]] || ! validate_rel_path "$rel"; then
        echo "Repository tree returned suspicious path: $file_path" >&2
        exit 1
    fi
    FILE_PATHS+=("$file_path")
    REL_PATHS+=("$rel")
done < "$paths_file"

expected_count="${#REL_PATHS[@]}"
if (( expected_count == 0 )); then
    echo "No source skill files found under $SOURCE_PATH" >&2
    exit 1
fi

echo ">>> Fetching $expected_count files..."
for idx in "${!FILE_PATHS[@]}"; do
    file_path="${FILE_PATHS[$idx]}"
    rel="${REL_PATHS[$idx]}"
    dst="$STAGE/$rel"
    mkdir -p "$(dirname "$dst")"
    curl -sSfL "${CURL_RAW_FLAGS[@]}" -o "$dst" "$(github_raw_url "$file_path")"
done

python3 - "$STAGE" "${REL_PATHS[@]}" > "$STAGE/$MANIFEST_MARKER" <<'PY'
import hashlib
import json
import os
import sys

stage = sys.argv[1]
paths = sorted(sys.argv[2:])
files = []
total_bytes = 0
for rel in paths:
    with open(os.path.join(stage, rel), "rb") as f:
        body = f.read()
    total_bytes += len(body)
    files.append({
        "path": rel,
        "sha256": hashlib.sha256(body).hexdigest(),
        "size": len(body),
    })

json.dump({
    "schemaVersion": 1,
    "generatedAt": "1970-01-01T00:00:00Z",
    "totalFiles": len(files),
    "totalBytes": total_bytes,
    "files": files,
}, sys.stdout, indent=2)
print()
PY

for target in "${TARGETS[@]}"; do
    mkdir -p "$target"
    for rel in "${REL_PATHS[@]}"; do
        tgt_file="$target/$rel"
        mkdir -p "$(dirname "$tgt_file")"
        cp "$STAGE/$rel" "$tgt_file"
    done
    cp "$STAGE/$MANIFEST_MARKER" "$target/$MANIFEST_MARKER"
    {
        printf 'branch: %s\n' "$BRANCH"
        printf 'source: %s\n' "$SOURCE_PATH"
    } > "$target/$VERSION_MARKER"
    echo "Installed $expected_count files to $target"
done

echo ">>> Done."
