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
test "$(yq -r 'select(.kind == "Job") | .spec.template.spec.containers[0].securityContext.runAsNonRoot' <<<"$rendered")" = true \
  || fail "receipt must run as non-root"
test "$(yq -r 'select(.kind == "Job") | .spec.template.spec.securityContext.fsGroup' <<<"$rendered")" = 1000 \
  || fail "receipt must set the pod filesystem group to 1000"
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
# get and patch are scoped to the receipt by name so this identity cannot
# reach any other ConfigMap. create cannot be scoped -- RBAC matches
# resourceNames against an object that does not exist yet.
grep -qE '^\s+resourceNames: \["nvcf-upgrade-receipt"\]' <<<"$rendered" \
  || fail "get/patch are not scoped to the receipt ConfigMap by name"
grep -qE '^\s+verbs: \["get", "patch"\]' <<<"$rendered" \
  || fail "scoped rule should carry only get and patch"
grep -qE '^\s+verbs: \["create"\]' <<<"$rendered" \
  || fail "create must remain, in its own rule, for the first install"

# The stage number is the ordering guarantee. needs: is deliberately not used
# here; see the comment in the stage file.
last_stage="$(printf '%s\n' "$stack_dir"/helmfile.d/*.gotmpl | sort | tail -1)"
[[ "$(basename "$last_stage")" == "04-upgrade-receipt.yaml.gotmpl" ]] \
  || fail "receipt is not the last stage; it would record a version before the stack finished applying"

chart_values="$stack_dir/charts/nvcf-upgrade-receipt/values.yaml"
expected_image="$(yq -r '.image.registry + "/" + .image.repository + ":" + .image.tag' "$chart_values")"
image() { yq -r 'select(.kind == "Job") | .spec.template.spec.containers[0].image'; }
test "$(image <<<"$rendered")" = "$expected_image" || fail "receipt must use its chart image default"
test "$(yq -r '.image.registry + "/" + .image.repository' "$chart_values")" = docker.io/alpine/k8s \
  || fail "receipt default must use the public upstream repository"

public="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/04-upgrade-receipt.yaml.gotmpl \
  --state-values-set global.image.registry=nvcr.io \
  --state-values-set global.image.repository=nvidia/nvcf template)"
test "$(image <<<"$public")" = "$expected_image" || fail "global NVCF image settings replaced the receipt helper"

custom="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/04-upgrade-receipt.yaml.gotmpl \
  --state-values-set upgradeReceipt.image.registry=mirror.example.com \
  --state-values-set upgradeReceipt.image.repository=maintenance/helper \
  --state-values-set upgradeReceipt.image.tag=9.8.7 template)"
test "$(image <<<"$custom")" = mirror.example.com/maintenance/helper:9.8.7 \
  || fail "receipt image overrides did not reach the Job"

empty="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/04-upgrade-receipt.yaml.gotmpl \
  --state-values-set upgradeReceipt.image.registry= \
  --state-values-set upgradeReceipt.image.repository= \
  --state-values-set upgradeReceipt.image.tag= template)"
test "$(image <<<"$empty")" = "$expected_image" || fail "empty overrides must preserve the chart defaults"

echo "PASS: upgrade-receipt-wiring"
