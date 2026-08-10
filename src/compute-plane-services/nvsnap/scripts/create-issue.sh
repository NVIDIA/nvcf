#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Create issue on both GitHub and GitLab simultaneously
#
# Usage:
#   ./scripts/create-issue.sh "Issue title" "Issue body" [label1,label2]
#
# Example:
#   ./scripts/create-issue.sh "Add Slurm support" "Support non-K8s clusters" "enhancement,P2"

set -euo pipefail

TITLE="${1:?Usage: create-issue.sh \"title\" \"body\" [labels]}"
BODY="${2:-}"
LABELS="${3:-}"

GH_REPO="balajinvda/nvsnap"
GL_PROJECT="bganesan%2Fnvsnap"
GL_URL="https://github.com/NVIDIA"
GL_TOKEN="${GITLAB_TOKEN:?Set GITLAB_TOKEN}"

# GitHub
echo "=== GitHub ==="
GH_ARGS=(--repo "$GH_REPO" --title "$TITLE")
if [ -n "$BODY" ]; then GH_ARGS+=(--body "$BODY"); fi
if [ -n "$LABELS" ]; then GH_ARGS+=(--label "$LABELS"); fi

GH_URL=$(gh issue create "${GH_ARGS[@]}" 2>&1)
GH_NUM=$(echo "$GH_URL" | grep -o '[0-9]*$')
echo "  Created: $GH_URL"

# GitLab
echo "=== GitLab ==="
GL_BODY="Mirrored from GitHub #${GH_NUM}\n\n${BODY}"
GL_DATA=$(python3 -c "import json; print(json.dumps({'title': '[GH#${GH_NUM}] ${TITLE}', 'description': '${GL_BODY}', 'labels': '${LABELS}'}))")

GL_RESP=$(curl -s --request POST \
    --header "PRIVATE-TOKEN: ${GL_TOKEN}" \
    --header "Content-Type: application/json" \
    --data "$GL_DATA" \
    "${GL_URL}/api/v4/projects/${GL_PROJECT}/issues")

GL_IID=$(echo "$GL_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('iid','?'))")
echo "  Created: ${GL_URL}/bganesan/nvsnap/-/issues/${GL_IID}"

echo ""
echo "GH#${GH_NUM} + GL#${GL_IID}: ${TITLE}"
