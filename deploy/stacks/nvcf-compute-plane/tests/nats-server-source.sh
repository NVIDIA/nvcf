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

for field in registry repository tag; do
  if ! grep -Fq "$field: {{ dig \"$field\"" "$compute_state"; then
    printf 'ERROR: Dynamo NATS %s is not wired from the environment values.\n' "$field" >&2
    exit 1
  fi
done

if ! grep -Fq "image: $expected_image" "$golden_state"; then
  printf 'ERROR: compute-plane golden render does not use %s.\n' "$expected_image" >&2
  exit 1
fi

printf 'nats-server-source: OK (%s)\n' "$expected_image"
