#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Focused tests for ci/check-conventional-title.sh.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="${here}/check-conventional-title.sh"
fails=0

pass_case() { # title
  if TITLE="$1" bash "${script}" >/dev/null 2>&1; then
    echo "ok    (accept) $1"
  else
    echo "FAIL  (should accept) $1"; fails=$((fails+1))
  fi
}

fail_case() { # title
  if TITLE="$1" bash "${script}" >/dev/null 2>&1; then
    echo "FAIL  (should reject) $1"; fails=$((fails+1))
  else
    echo "ok    (reject) $1"
  fi
}

# Valid Conventional Commits
pass_case "fix(cassandra): bump migration image to 0.11.0"
pass_case "feat(worker): surface NATS auth violations"
pass_case "chore(ci): pin bazel-ci image tag"
pass_case "feat: add endpoint"                     # scope optional
pass_case "feat(api)!: drop deprecated field"      # breaking marker
pass_case "revert: undo the thing"
pass_case "Draft: fix(cassandra): retag chart"     # Draft prefix stripped
pass_case "[Draft] fix(cassandra): retag chart"    # GitLab bracket form stripped
pass_case "[draft]: fix(cassandra): retag chart"   # lowercase bracket form stripped
pass_case "WIP fix(worker): retry"                 # WIP prefix stripped

# Invalid: the real-world failure and friends
fail_case "Feat/bump cassandra migration"          # branch name, no colon (the bug)
fail_case "Feat: bump cassandra migration"         # capitalized type
fail_case "update the chart"                        # no type
fail_case "feature(x): wrong type word"             # 'feature' not a type
fail_case "fix:"                                    # empty subject
fail_case "fix:  "                                  # whitespace-only subject
fail_case "fix add thing"                           # missing colon
fail_case ""                                         # empty

echo
if [ "${fails}" -eq 0 ]; then
  echo "all check-conventional-title tests passed"
else
  echo "${fails} test(s) failed"; exit 1
fi
