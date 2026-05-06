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

# nvcf-cli agent skill — curl-pipe installer.
#
# Downloads the markdown bundle from nvcf-cli's internal/agentskill/data/
# directory (the byte-identical mirror of the source-of-truth markdown in
# mcamp/docs, verified by manifest SHA256) and writes it into the operator's
# agent-ecosystem skill directories. Verifies each file's SHA256 against
# manifest.json before any write.
#
# Use this when nvcf-cli isn't installed yet (chicken-and-egg). Operators
# with nvcf-cli should use 'nvcf-cli agent-skill install' instead.
#
# AUTHENTICATION:
#   This script fetches from github.com/NVIDIA, which requires auth.
#   Set GITLAB_TOKEN in your environment (a GitLab personal access token with
#   read_repository scope), or ensure ~/.netrc has credentials for the host.
#   Get a token at: https://github.com/NVIDIA/-/user_settings/personal_access_tokens
#
# MANUAL SMOKE TEST:
#   export GITLAB_TOKEN=<your-token>
#
#   # Install to a temp dir:
#   bash scripts/install-agent-skill.sh --target=/tmp/skill-smoke
#   ls /tmp/skill-smoke/
#   cat /tmp/skill-smoke/.version
#
#   # Uninstall:
#   bash scripts/install-agent-skill.sh --uninstall --target=/tmp/skill-smoke
#   ls /tmp/skill-smoke 2>&1
#
#   # Test default branch install (both ecosystem dirs):
#   bash scripts/install-agent-skill.sh
#   ls ~/.claude/skills/nvcf-cli/
#   ls ~/.agents/skills/nvcf-cli/
#
#   # Test custom branch:
#   bash scripts/install-agent-skill.sh --branch=mcamp/feat/m-plus-10-agent-skill --target=/tmp/skill-smoke
#
# CI SYNTAX CHECK:
#   bash -n scripts/install-agent-skill.sh

set -euo pipefail

# Defaults
DEFAULT_BRANCH="main"
GITLAB_HOST="github.com/NVIDIA"
# Source of truth for the embedded agent skill is the nvcf-cli repo's
# internal/agentskill/data/ directory — the byte-identical mirror of
# mcamp/docs/nvcf/one-click-deploy/agent-skill/, verified by manifest SHA256
# at every CI build. Pulling from cli rather than mcamp/docs because cli is
# the repo operators already interact with (one-stop bootstrap surface).
GITLAB_PROJECT="ncp/nvcf/cli"
SKILL_PATH="internal/agentskill/data"

# State (populated by argv parse)
BRANCH="$DEFAULT_BRANCH"
ACTION="install"
EXPLICIT_TARGET=""

usage() {
    cat <<'EOF'
Usage: install-agent-skill.sh [--branch=REF] [--target=DIR] [--uninstall] [--help]

Installs the nvcf-cli agent skill markdown bundle into the agent-ecosystem
skill directories. With no args, writes to BOTH ~/.claude/skills/nvcf-cli/
and ~/.agents/skills/nvcf-cli/. Source: nvcf-cli's internal/agentskill/data/
directory (verified byte-identical mirror of the canonical content).

Options:
  --branch=REF      nvcf-cli ref to fetch from (default: main)
  --target=DIR      install to a single directory instead of the defaults
  --uninstall       remove from the default targets (or --target if set)
  --help            print this message and exit

Examples:
  curl -sSfL <URL> | GITLAB_TOKEN=<token> bash
  curl -sSfL <URL> | GITLAB_TOKEN=<token> bash -s -- --branch=feat/foo
  curl -sSfL <URL> | GITLAB_TOKEN=<token> bash -s -- --uninstall
  curl -sSfL <URL> | GITLAB_TOKEN=<token> bash -s -- --target=/path/to/skill

Environment:
  GITLAB_TOKEN      GitLab personal access token (read_repository scope)
                    Get one at: https://github.com/NVIDIA/-/user_settings/personal_access_tokens
                    If unset, curl will attempt to use ~/.netrc credentials.

Source: github.com/NVIDIA/ncp/nvcf/cli/internal/agentskill/data/
(byte-identical mirror of mcamp/docs/nvcf/one-click-deploy/agent-skill/,
verified by manifest SHA256 at every CI build)
EOF
}

# Parse argv
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

# Tilde-expansion for EXPLICIT_TARGET
if [[ -n "$EXPLICIT_TARGET" ]] && [[ "$EXPLICIT_TARGET" == "~"* ]]; then
    EXPLICIT_TARGET="${EXPLICIT_TARGET/#~/$HOME}"
fi

# Resolve target list
declare -a TARGETS
if [[ -n "$EXPLICIT_TARGET" ]]; then
    TARGETS=("$EXPLICIT_TARGET")
else
    TARGETS=("$HOME/.claude/skills/nvcf-cli" "$HOME/.agents/skills/nvcf-cli")
fi

# Uninstall
if [[ "$ACTION" == "uninstall" ]]; then
    for target in "${TARGETS[@]}"; do
        if [[ -d "$target" ]]; then
            rm -rf "$target"
            echo "Removed $target"
        else
            echo "Skipped $target (not present)"
        fi
    done
    exit 0
fi

# Tooling check
need() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Required tool not on PATH: $1" >&2
        exit 1
    }
}
need curl
need shasum
need mktemp

# Build curl auth flags.
# Prefer GITLAB_TOKEN env var; fall back to ~/.netrc via curl -n.
declare -a CURL_AUTH_FLAGS
if [[ -n "${GITLAB_TOKEN:-}" ]]; then
    CURL_AUTH_FLAGS=(-H "PRIVATE-TOKEN: $GITLAB_TOKEN")
else
    # Try to find credentials in ~/.netrc
    if grep -q "$GITLAB_HOST" "$HOME/.netrc" 2>/dev/null; then
        CURL_AUTH_FLAGS=(-n)
    else
        echo "Warning: GITLAB_TOKEN not set and no ~/.netrc entry for $GITLAB_HOST." >&2
        echo "Set GITLAB_TOKEN=<personal-access-token> to authenticate." >&2
        echo "Get a token at: https://$GITLAB_HOST/-/user_settings/personal_access_tokens" >&2
        CURL_AUTH_FLAGS=()
    fi
fi

# Build GitLab API v4 URL for a file path in the project.
# The /-/raw/ endpoint requires a browser session; the API endpoint
# accepts PRIVATE-TOKEN headers and works for programmatic access.
#
# URL format:
#   https://HOST/api/v4/projects/PROJECT_ENCODED/repository/files/FILE_ENCODED/raw?ref=REF
#
# Shell-portable URL encoding (replace / with %2F only, which is all we need
# for the project path and file paths within this repo layout).
url_encode_path() {
    local path="$1"
    # Encode forward slashes only — sufficient for our paths.
    printf '%s' "${path//\//%2F}"
}

project_encoded="$(url_encode_path "$GITLAB_PROJECT")"

gitlab_raw_url() {
    local file_path="$1"
    local file_encoded
    file_encoded="$(url_encode_path "$file_path")"
    printf 'https://%s/api/v4/projects/%s/repository/files/%s/raw?ref=%s' \
        "$GITLAB_HOST" "$project_encoded" "$file_encoded" "$BRANCH"
}

# Stage to a tmpdir, verify, then copy in.
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

MANIFEST_URL="$(gitlab_raw_url "$SKILL_PATH/manifest.json")"

echo ">>> Fetching manifest.json from $BRANCH..."
curl -sSfL "${CURL_AUTH_FLAGS[@]}" -o "$STAGE/manifest.json" "$MANIFEST_URL"

# Validate we got JSON (not an HTML error page)
if ! python3 -c 'import json,sys; json.load(sys.stdin)' < "$STAGE/manifest.json" 2>/dev/null && \
   ! jq empty "$STAGE/manifest.json" 2>/dev/null; then
    echo "manifest.json appears to be invalid JSON — authentication may have failed." >&2
    echo "Set GITLAB_TOKEN=<personal-access-token> or add ~/.netrc credentials." >&2
    exit 1
fi

# Parse the manifest with jq or python3 fallback.
# manifest.json schema (from M+10.1b): files[].path, files[].sha256, totalFiles
if command -v jq >/dev/null 2>&1; then
    mapfile -t FILE_PATHS < <(jq -r '.files[].path' "$STAGE/manifest.json")
    expected_count=$(jq -r '.totalFiles' "$STAGE/manifest.json")
elif command -v python3 >/dev/null 2>&1; then
    mapfile -t FILE_PATHS < <(python3 -c '
import json, sys
m = json.load(sys.stdin)
for f in m["files"]:
    print(f["path"])
' < "$STAGE/manifest.json")
    expected_count=$(python3 -c '
import json, sys
print(json.load(sys.stdin)["totalFiles"])
' < "$STAGE/manifest.json")
else
    echo "Need either jq or python3 to parse manifest.json" >&2
    exit 1
fi

actual_count="${#FILE_PATHS[@]}"
if [[ "$actual_count" != "$expected_count" ]]; then
    echo "manifest.json: totalFiles=$expected_count but listed $actual_count entries — refusing to install" >&2
    exit 1
fi

# Path-traversal defense: reject any manifest entry containing .. or starting with /
for path in "${FILE_PATHS[@]}"; do
    case "$path" in
        *..*|/*)
            echo "manifest.json contains suspicious path: $path — refusing to install" >&2
            exit 1
            ;;
    esac
done

# Fetch each file + verify SHA256
echo ">>> Fetching $expected_count files and verifying SHA256..."
for path in "${FILE_PATHS[@]}"; do
    [[ -z "$path" ]] && continue
    dst="$STAGE/$path"
    mkdir -p "$(dirname "$dst")"
    file_url="$(gitlab_raw_url "$SKILL_PATH/$path")"
    curl -sSfL "${CURL_AUTH_FLAGS[@]}" -o "$dst" "$file_url"

    # Look up expected hash from manifest
    if command -v jq >/dev/null 2>&1; then
        expected=$(jq -r --arg p "$path" '.files[] | select(.path == $p) | .sha256' "$STAGE/manifest.json")
    else
        expected=$(python3 -c '
import json, sys
m = json.load(sys.stdin)
for f in m["files"]:
    if f["path"] == sys.argv[1]:
        print(f["sha256"])
        break
' < "$STAGE/manifest.json" "$path")
    fi

    actual=$(shasum -a 256 "$dst" | cut -d' ' -f1)

    if [[ "$expected" != "$actual" ]]; then
        echo "SHA256 mismatch for $path: expected $expected, got $actual — refusing to install" >&2
        exit 1
    fi
    echo "  verified $path"
done

echo ">>> All $expected_count files verified."

# Now copy from STAGE to each target (only after all verifications pass)
for target in "${TARGETS[@]}"; do
    mkdir -p "$target"
    cp "$STAGE/manifest.json" "$target/"
    for path in "${FILE_PATHS[@]}"; do
        [[ -z "$path" ]] && continue
        tgt_file="$target/$path"
        mkdir -p "$(dirname "$tgt_file")"
        cp "$STAGE/$path" "$tgt_file"
    done
    # Write a .version file with the branch ref for audit
    printf 'branch: %s\n' "$BRANCH" > "$target/.version"
    echo "Installed to $target"
done

echo ">>> Done."
