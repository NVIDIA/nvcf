#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

mr_url() {
  if [ -n "${CI_MERGE_REQUEST_PROJECT_URL:-}" ] && [ -n "${CI_MERGE_REQUEST_IID:-}" ]; then
    printf '%s/-/merge_requests/%s' "${CI_MERGE_REQUEST_PROJECT_URL}" "${CI_MERGE_REQUEST_IID}"
  elif [ -n "${CI_PROJECT_URL:-}" ] && [ -n "${CI_MERGE_REQUEST_IID:-}" ]; then
    printf '%s/-/merge_requests/%s' "${CI_PROJECT_URL}" "${CI_MERGE_REQUEST_IID}"
  elif [ -n "${CI_MERGE_REQUEST_IID:-}" ]; then
    printf 'MR !%s' "${CI_MERGE_REQUEST_IID}"
  else
    printf 'current merge request'
  fi
}

print_github_commit_guidance() {
  local reason="$1"

  cat >&2 <<EOF
ERROR: ${reason}

Why this is required:
  The NVCF OSS snapshot pipeline publishes sanitized changes to github.com/NVIDIA/nvcf.
  nvossctl reads the final "Github commit:" section from the GitLab MR description.
  Everything after the marker becomes the exact public GitHub commit message.

Required fix:
  1. Edit the GitLab MR description.
  2. Add this exact footer as the final section of the GitLab MR description:

Github commit:
docs(readme): update architecture diagram and registry credential docs

Update public docs for architecture diagrams and registry credential commands.

  3. Replace the example with the real public commit message for this MR.
     Use a Conventional Commit subject on the first line, followed by an optional body
     and public-safe trailers such as Co-authored-by.

Rules for agents:
  - Keep the marker text exactly: Github commit:
  - Put it after internal-only fields such as JIRA and NVBug.
  - Do not leave the section empty.
  - Do not use Jira/NVBug-only text as the public GitHub commit message.
  - Treat every line after the marker as public text that may appear on GitHub.

MR: $(mr_url)
EOF
}

if [ -z "${CI_MERGE_REQUEST_IID:-}" ]; then
  echo "Not an MR pipeline; skipping."
  exit 0
fi

if [ -n "${CI_MERGE_REQUEST_DESCRIPTION:-}" ] && [ "${CI_MERGE_REQUEST_DESCRIPTION_IS_TRUNCATED:-false}" != "true" ]; then
  description="${CI_MERGE_REQUEST_DESCRIPTION}"
else
  if [ -n "${GITLAB_TOKEN:-}" ]; then
    token_header=(--header "PRIVATE-TOKEN: ${GITLAB_TOKEN}")
  elif [ -n "${CI_JOB_TOKEN:-}" ]; then
    token_header=(--header "JOB-TOKEN: ${CI_JOB_TOKEN}")
  else
    echo "ERROR: GITLAB_TOKEN or CI_JOB_TOKEN is required to read the MR description."
    exit 1
  fi

  project_id="${CI_MERGE_REQUEST_PROJECT_ID:-${CI_PROJECT_ID}}"
  mr_url="${CI_API_V4_URL}/projects/${project_id}/merge_requests/${CI_MERGE_REQUEST_IID}"

  if ! mr_json="$(curl --silent --show-error --fail "${token_header[@]}" "${mr_url}")"; then
    echo "ERROR: Failed to read MR ${CI_MERGE_REQUEST_IID} description from the GitLab API."
    echo "If CI_JOB_TOKEN cannot read MR API data, set a masked GITLAB_TOKEN with read_api."
    exit 1
  fi

  description="$(printf '%s' "${mr_json}" | jq -r '.description // ""')"
fi

set +e
printf '%s\n' "${description}" | awk '
  function trim(s) {
    gsub(/^[[:space:]\n]+|[[:space:]\n]+$/, "", s)
    return s
  }
  BEGIN { found=0; body="" }
  {
    marker=$0
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", marker)
  }
  tolower(marker) == "github commit:" { found=1; body=""; next }
  found { body = body $0 "\n" }
  END {
    body=trim(body)
    if (!found) {
      exit 10
    }
    if (body == "") {
      exit 11
    }
  }
'
validation_status=$?
set -e

case "${validation_status}" in
  0)
    ;;
  10)
    print_github_commit_guidance 'The MR description is missing the required final "Github commit:" section.'
    exit 1
    ;;
  11)
    print_github_commit_guidance 'The MR description has a Github commit section, but it is empty.'
    exit 1
    ;;
  *)
    echo "ERROR: Failed to validate the MR Github commit section." >&2
    exit "${validation_status}"
    ;;
esac

echo "MR Github commit section found."
