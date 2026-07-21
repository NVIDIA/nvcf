#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Fail a pull request that does not reference an associated issue.
#
# A PR passes if its body's ## Issues section contains a valid NVIDIA/nvcf
# GitHub issue reference or the NO-REF last-resort value:
#   - action keyword + optional NVIDIA/nvcf repo: "Closes #123",
#     "Fixes NVIDIA/nvcf#123", "Resolves #123", "Relates to #123"
#   - "NO-REF"
#
# Inputs (from the workflow, passed via env to avoid shell injection from the
# attacker-controlled PR body):
#   PR_BODY, GITHUB_TOKEN, GITHUB_REPOSITORY, GITHUB_API_URL
set -euo pipefail

# Strip HTML comments so the template's guidance and examples are not mistaken
# for a real ref. Only the body ## Issues section is checked.
if output="$(
  PR_BODY="${PR_BODY:-}" \
  GITHUB_TOKEN="${GITHUB_TOKEN:-}" \
  GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-NVIDIA/nvcf}" \
  SKIP_GITHUB_ISSUE_EXISTS_CHECK="${SKIP_GITHUB_ISSUE_EXISTS_CHECK:-}" \
  python3 - <<'PY'
import json
import os
import re
import sys
import urllib.error
import urllib.request

repo = os.environ.get("GITHUB_REPOSITORY", "NVIDIA/nvcf")
if repo.lower() != "nvidia/nvcf":
    print(f"ERROR: unsupported repository {repo}; expected NVIDIA/nvcf.")
    sys.exit(1)

body = os.environ.get("PR_BODY", "")
# GitHub stores PR bodies with CRLF line endings (web UI and bot edits), so
# normalize to LF before matching; the ## Issues heading regex expects a bare
# newline and would otherwise fail to find the section on a CRLF body.
body = body.replace("\r\n", "\n").replace("\r", "\n")
visible = re.sub(r"<!--.*?(?:-->|$)", "", body, flags=re.S)

section_match = re.search(
    r"(?ims)^##[ \t]+Issues[ \t]*\n(?P<section>.*?)(?=^##[ \t]+|\Z)",
    visible,
)
if not section_match:
    print("ERROR: PR body is missing a visible ## Issues section.")
    sys.exit(1)

section = section_match.group("section")

disallowed_repo = re.search(
    r"\b(?!NVIDIA/nvcf\b)[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+\b",
    section,
    flags=re.I,
)
if disallowed_repo:
    print(
        "ERROR: PR issue references must point to NVIDIA/nvcf; "
        f"found {disallowed_repo.group(0)}."
    )
    sys.exit(1)

disallowed_tracker = re.search(
    r"\b(?:NVCF|NVCFCLUST|NVCT|NVBUG|NVBUGS)-[0-9]+\b",
    section,
    flags=re.I,
)
if disallowed_tracker:
    print(
        "ERROR: Jira keys, private tracker keys, and private bug IDs do not "
        "satisfy this check. Create a generic public issue instead."
    )
    sys.exit(1)

disallowed_url = re.search(r"https?://\S+", section, flags=re.I)
if disallowed_url:
    print(
        "ERROR: URLs do not satisfy this check. Use an action keyword "
        "NVIDIA/nvcf issue reference instead."
    )
    sys.exit(1)

if re.search(r"(^|[^A-Za-z0-9])NO-REF([^A-Za-z0-9]|$)", section):
    print("check-pr-issue: NO-REF present in ## Issues.")
    sys.exit(0)

action = r"(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?|relate[sd]?[ \t]+to)"
candidates = set()
for match in re.finditer(
    action + r"[ \t:]+(?:(?P<repo>NVIDIA/nvcf))?#(?P<num>[0-9]+)\b",
    section,
    flags=re.I,
):
    candidates.add(match.group("num"))

if not candidates:
    print("ERROR: this pull request does not reference an associated issue.")
    print()
    print("Add a GitHub issue to the ## Issues section, for example:")
    print('  - "Closes #123" or "Fixes NVIDIA/nvcf#123"')
    print('  - or "NO-REF" only as a last resort')
    print()
    print("If private context exists, create a generic public issue instead of")
    print("using a Jira key, private tracker key, private bug ID, or private URL.")
    sys.exit(1)

if os.environ.get("SKIP_GITHUB_ISSUE_EXISTS_CHECK") == "1":
    print(
        "check-pr-issue: PR references an associated NVIDIA/nvcf issue "
        "(existence check skipped)."
    )
    sys.exit(0)

token = os.environ.get("GITHUB_TOKEN")
if not token:
    print("ERROR: GITHUB_TOKEN is required to validate issue existence.")
    sys.exit(1)

headers = {
    "Accept": "application/vnd.github+json",
    "Authorization": f"Bearer {token}",
    "User-Agent": "nvcf-pr-issue-check",
    "X-GitHub-Api-Version": "2022-11-28",
}
api_url = (os.environ.get("GITHUB_API_URL") or "https://api.github.com").rstrip("/")

for number in sorted(candidates, key=int):
    req = urllib.request.Request(
        f"{api_url}/repos/NVIDIA/nvcf/issues/{number}",
        headers=headers,
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            data = json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as err:
        if err.code == 404:
            continue
        print(f"ERROR: failed to validate NVIDIA/nvcf#{number}: HTTP {err.code}.")
        sys.exit(1)
    except Exception as err:
        print(f"ERROR: failed to validate NVIDIA/nvcf#{number}: {err}.")
        sys.exit(1)

    if "pull_request" in data:
        print(f"ERROR: NVIDIA/nvcf#{number} is a pull request, not an issue.")
        sys.exit(1)

    print(f"check-pr-issue: PR references NVIDIA/nvcf#{number}.")
    sys.exit(0)

print(
    "ERROR: referenced NVIDIA/nvcf issue number was not found or is not "
    "accessible."
)
sys.exit(1)
PY
)"; then
  echo "$output"
  exit 0
fi

echo "$output" >&2
exit 1
