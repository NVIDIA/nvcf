#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
compute_stack_dir="$(cd "$script_dir/.." && pwd)"
base_values="$compute_stack_dir/environments/base.yaml"
compute_state="$compute_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl"
golden_state="$compute_stack_dir/testdata/golden/local/01-dependencies.yaml-dynamo-operator/dynamo-platform/charts/nats/templates/stateful-set.yaml"

registry="$(yq -r '.addons.dynamoOperator.nats.image.registry' "$base_values")"
repository="$(yq -r '.addons.dynamoOperator.nats.image.repository' "$base_values")"
tag="$(yq -r '.addons.dynamoOperator.nats.image.tag' "$base_values")"
expected_image="$registry/$repository:$tag"

if [[ "$expected_image" != "docker.io/library/nats:2.10.21-alpine" ]]; then
  printf 'ERROR: unexpected Dynamo NATS default image: %s\n' "$expected_image" >&2
  exit 1
fi

if ! grep -Fq "image: $expected_image" "$golden_state"; then
  printf 'ERROR: compute-plane golden render does not use %s.\n' "$expected_image" >&2
  exit 1
fi

render_dir="$(mktemp -d "${TMPDIR:-/tmp}/nvcf-nats-server-source.XXXXXX")"
trap 'rm -rf "$render_dir"' EXIT

override_registry="mirror.example.invalid"
override_repository="team/nats"
override_tag="review-test"
override_image="$override_registry/$override_repository:$override_tag"
render_log="$render_dir/helmfile.log"

if ! HELMFILE_ENV=base \
  CLUSTER_NAME=nats-server-source-test \
  NCA_ID=nats-server-source-test \
  OUTPUT_DIR="$render_dir" \
  helmfile \
    --file "$compute_state" \
    --environment default \
    --state-values-set "addons.dynamoOperator.enabled=true,addons.dynamoOperator.nats.image.registry=$override_registry,addons.dynamoOperator.nats.image.repository=$override_repository,addons.dynamoOperator.nats.image.tag=$override_tag" \
    --selector name=dynamo-operator \
    template \
    --output-dir "$render_dir" \
    --output-dir-template '{{ .OutputDir }}/{{ .Release.Name }}' \
    >"$render_log" 2>&1; then
  sed -n '1,240p' "$render_log" >&2
  exit 1
fi

override_state="$render_dir/dynamo-operator/dynamo-platform/charts/nats/templates/stateful-set.yaml"
if ! grep -Fq "image: $override_image" "$override_state"; then
  printf 'ERROR: rendered Dynamo NATS override does not use %s.\n' "$override_image" >&2
  exit 1
fi

printf 'nats-server-source: OK (default %s, override %s)\n' \
  "$expected_image" "$override_image"
