#!/usr/bin/env bash
set -euo pipefail

render_dir="${1:?rendered manifest directory is required}"
template_dir="$render_dir/02-nvca.yaml-nvca-operator/helm-nvca-operator/templates"
deployment="$template_dir/deployment.yaml"
cleanup_job="$template_dir/pre-delete-cleanup-job.yaml"

fail() {
  echo "nvca-entrypoints: $*" >&2
  exit 1
}

for manifest in "$deployment" "$cleanup_job"; do
  test -f "$manifest" || fail "missing rendered manifest: $manifest"
done

for binary in nvca-operator nvca-mirror; do
  grep -Fq -- "- /usr/bin/$binary" "$deployment" ||
    fail "deployment does not invoke /usr/bin/$binary directly"
done

grep -Fq -- '- /usr/bin/nvca-operator-cleanup' "$cleanup_job" ||
  fail "cleanup job does not invoke /usr/bin/nvca-operator-cleanup directly"

if grep -Fq -- '- /tini' "$deployment" "$cleanup_job"; then
  fail "rendered NVCA containers still depend on /tini"
fi

echo "nvca-entrypoints: all checks passed"
