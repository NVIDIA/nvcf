#!/usr/bin/env bash
# Verify that a named self-managed environment does not also require an
# identically named environment file in the included observability stack.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_self_managed_dir="$test_stacks_dir/self-managed"
test_observability_dir="$test_stacks_dir/observability"
environment_name="custom-observability-environment-test"
parent_environment_file="$test_self_managed_dir/environments/$environment_name.yaml"
standalone_environment_file="$test_observability_dir/environments/$environment_name.yaml"
trap 'rm -rf "$work_dir"' EXIT

# Print a test-specific error and exit.
fail() {
  echo "custom-observability-environment: $*" >&2
  exit 1
}

for command in helmfile yq; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

# Preserve the sibling-stack layout used by the parent Helmfile.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/self-managed "$test_stacks_dir"
cp -R "$stacks_dir"/observability "$test_stacks_dir"

# The documented self-managed workflow creates this file only in the parent
# stack. The base profile remains control; the override proves parent values
# reach the included observability releases.
cat >"$parent_environment_file" <<'EOF'
observability:
  namespace: parent-monitoring
EOF

test ! -e "$standalone_environment_file" ||
  fail "test setup unexpectedly created an observability environment file"

# Without a parent, the shared stack must retain its fail-fast check for a
# missing named environment.
missing_standalone_log="$work_dir/missing-standalone.log"
if HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_observability_dir/helmfile.d/01-observability.yaml.gotmpl" \
    --environment default \
    list >"$work_dir/missing-standalone-list.txt" 2>"$missing_standalone_log"; then
  fail "standalone observability accepted a missing named environment"
fi
if ! grep -q 'environment values file matching.*does not exist' "$missing_standalone_log"; then
  cat "$missing_standalone_log" >&2
  fail "standalone observability failed for an unexpected reason"
fi

parent_list="$work_dir/parent-list.json"
parent_log="$work_dir/parent-list.log"
if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_self_managed_dir/helmfile.d/00-observability-infrastructure.yaml.gotmpl" \
    --environment default \
    list --output json >"$parent_list" 2>"$parent_log"; then
  cat "$parent_log" >&2
  fail "the parent stack could not list included observability releases"
fi

expected_names="default-monitors,opentelemetry-operator,otel-collector,prometheus-operator-crds,victoria-metrics"
parent_names="$(
  yq -p=json -o=yaml -r '.[] | select(.enabled == true) | .name' "$parent_list" |
    sort |
    paste -sd, -
)"
[[ "$parent_names" == "$expected_names" ]] ||
  fail "unexpected parent release list: $parent_names"

parent_namespaces="$(
  yq -p=json -o=yaml -r '.[] | select(.enabled == true) | .namespace' "$parent_list" |
    sort -u |
    paste -sd, -
)"
[[ "$parent_namespaces" == "parent-monitoring" ]] ||
  fail "parent namespace override was not forwarded: $parent_namespaces"

# Template one local chart to exercise the install rendering path without
# downloading or applying third-party releases.
parent_template_dir="$work_dir/parent-template"
parent_template_log="$work_dir/parent-template.log"
if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_self_managed_dir/helmfile.d/00-observability-infrastructure.yaml.gotmpl" \
    --environment default \
    --selector name=default-monitors \
    template --output-dir "$parent_template_dir" >"$work_dir/parent-template.txt" \
    2>"$parent_template_log"; then
  cat "$parent_template_log" >&2
  fail "the parent stack could not template included observability releases"
fi

rendered_monitors="$(
  find "$parent_template_dir" -type f -name 'servicemonitors.yaml' -print -quit
)"
[[ -n "$rendered_monitors" ]] ||
  fail "the parent stack rendered no ServiceMonitor manifests"
grep -q 'namespace: parent-monitoring' "$rendered_monitors" ||
  fail "the rendered monitors did not use the parent namespace"

# Standalone observability must still load its own named environment and retain
# the existing fail-fast lookup when no parent has resolved values for it.
cat >"$standalone_environment_file" <<'EOF'
observability:
  profile: compute
  namespace: standalone-monitoring
EOF

# A child file with the same name belongs to standalone use and must not
# override values already resolved by the self-managed parent.
parent_with_child_list="$work_dir/parent-with-child-list.json"
parent_with_child_log="$work_dir/parent-with-child-list.log"
if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_self_managed_dir/helmfile.d/00-observability-infrastructure.yaml.gotmpl" \
    --environment default \
    list --output json >"$parent_with_child_list" 2>"$parent_with_child_log"; then
  cat "$parent_with_child_log" >&2
  fail "the parent stack failed when a standalone environment file existed"
fi

parent_with_child_namespaces="$(
  yq -p=json -o=yaml -r \
    '.[] | select(.enabled == true) | .namespace' "$parent_with_child_list" |
    sort -u |
    paste -sd, -
)"
[[ "$parent_with_child_namespaces" == "parent-monitoring" ]] ||
  fail "standalone values overrode the parent: $parent_with_child_namespaces"

standalone_list="$work_dir/standalone-list.json"
standalone_log="$work_dir/standalone-list.log"
if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_observability_dir/helmfile.d/01-observability.yaml.gotmpl" \
    --environment default \
    list --output json >"$standalone_list" 2>"$standalone_log"; then
  cat "$standalone_log" >&2
  fail "the standalone stack could not load its named environment"
fi

standalone_namespaces="$(
  yq -p=json -o=yaml -r '.[] | select(.enabled == true) | .namespace' "$standalone_list" |
    sort -u |
    paste -sd, -
)"
[[ "$standalone_namespaces" == "standalone-monitoring" ]] ||
  fail "standalone namespace override was not loaded: $standalone_namespaces"

echo "custom-observability-environment: all checks passed"
