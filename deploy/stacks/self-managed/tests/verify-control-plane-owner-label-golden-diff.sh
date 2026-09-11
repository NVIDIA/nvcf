#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

golden_dir="${1:?golden manifest directory is required}"
base_ref="${SELF_MANAGED_OWNER_LABEL_DIFF_BASE:-}"
owner_label_re='nvcf\.nvidia\.com/control-plane-owner'

fail() {
  echo "verify-control-plane-owner-label-golden-diff: $*" >&2
  exit 1
}

test -n "$base_ref" ||
  fail "set SELF_MANAGED_OWNER_LABEL_DIFF_BASE to the commit/ref before the ownership-label golden refresh"
test -d "$golden_dir" || fail "$golden_dir does not exist"

repo_dir="$(git -C "$golden_dir" rev-parse --show-toplevel)"
golden_rel="${golden_dir#$repo_dir/}"
diff_file="$(mktemp)"
trap 'rm -f "$diff_file"' EXIT

git -C "$repo_dir" diff --no-color --unified=0 "$base_ref" -- "$golden_rel" >"$diff_file"
test -s "$diff_file" || fail "no golden diff found against $base_ref"

awk -v owner_label_re="$owner_label_re" '
  function fail_pending_labels() {
    if (pending_labels) {
      print "labels parent line was not immediately followed by an ownership label: " pending_labels > "/dev/stderr"
      bad = 1
      pending_labels = ""
    }
  }

  /^(diff --git|index |--- |\+\+\+ |@@ )/ {
    fail_pending_labels()
    next
  }
  /^\\ No newline at end of file$/ {
    fail_pending_labels()
    next
  }
  /^\+/ {
    if ($0 ~ ("^\\+[[:space:]]+" owner_label_re ": (default|shared)$")) {
      pending_labels = ""
      next
    }
    if ($0 ~ /^\+  labels:[[:space:]]*$/) {
      fail_pending_labels()
      pending_labels = $0
      next
    }
    fail_pending_labels()
    print "unexpected added line: " $0 > "/dev/stderr"
    bad = 1
    next
  }
  /^-/ {
    fail_pending_labels()
    print "unexpected removed line: " $0 > "/dev/stderr"
    bad = 1
    next
  }
  {
    fail_pending_labels()
    print "unexpected diff line: " $0 > "/dev/stderr"
    bad = 1
  }
  END {
    fail_pending_labels()
    exit bad
  }
' "$diff_file" || fail "golden diff contains changes other than ownership-label additions"

echo "verify-control-plane-owner-label-golden-diff: all changed golden lines are ownership-label additions"
