#!/usr/bin/env bash
# Test that the stack records the version it installed.
#
# An upgrade has to know where it is starting from, and nothing else in a
# cluster carries that: Helm tracks chart versions per release, and helmfile has
# no concept of the bundle's own version. Without this receipt every cluster
# looks identical to every other one at upgrade time.
#
# The assertions that matter are the hook kinds and the recorded version. A
# pre-* hook would claim a version before it was applied, and a post-upgrade
# hook alone would skip the first install of this chart, which is precisely the
# set of clusters that need a receipt written.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_version="$(tr -d '[:space:]' < "$stack_dir/VERSION")"

rendered="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/04-upgrade-receipt.yaml.gotmpl template)"

fail() { echo "FAIL: $1" >&2; exit 1; }

grep -q 'kind: Job' <<<"$rendered" || fail "no Job rendered"
grep -q '"helm.sh/hook": post-install,post-upgrade' <<<"$rendered" \
  || fail "receipt must run on both install and upgrade, after the release it describes"
grep -q "value: \"${expected_version}\"" <<<"$rendered" \
  || fail "recorded version does not match VERSION (${expected_version})"
# The ConfigMap is created by the Job at run time, not rendered, so its name
# reaches the cluster as the env var the script reads.
grep -q 'value: "nvcf-upgrade-receipt"' <<<"$rendered" \
  || fail "receipt ConfigMap name is not the one an upgrade will read"

for kind in ServiceAccount Role RoleBinding; do
  grep -q "kind: ${kind}" <<<"$rendered" || fail "missing ${kind}; the Job cannot write the ConfigMap without it"
done
grep -qE '^\s+verbs: \["get", "create", "patch"\]' <<<"$rendered" \
  || fail "Role must allow get/create/patch on configmaps and nothing more"

# The stage number is the ordering guarantee. needs: is deliberately not used
# here; see the comment in the stage file.
last_stage="$(ls "$stack_dir"/helmfile.d/*.gotmpl | sort | tail -1)"
[[ "$(basename "$last_stage")" == "04-upgrade-receipt.yaml.gotmpl" ]] \
  || fail "receipt is not the last stage; it would record a version before the stack finished applying"

echo "PASS: upgrade-receipt-wiring"
