#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: render-sample.sh [--dry-run]

Renders sample Kubernetes YAML to stdout. It never applies resources.
The --dry-run flag is accepted for consistency with other render helpers.

Environment:
  SAMPLE_IMAGE  Full public image reference, with or without tag. Default:
                registry.k8s.io/e2e-test-images/agnhost:2.53

Example:
  make deploy-sample
  SAMPLE_IMAGE=registry.k8s.io/e2e-test-images/nginx:1.15-4 make deploy-sample
EOF
}

dry_run=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      dry_run=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

readonly dry_run

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

split_image_ref() {
  local ref="$1"

  sample_digest=""

  if [[ "$ref" == *@sha256:* ]]; then
    sample_image_name="${ref%%@sha256:*}"
    sample_digest="sha256:${ref#*@sha256:}"
    sample_tag=""
    return 0
  fi

  if [[ "$ref" == *:* ]]; then
    sample_image_name="${ref%:*}"
    sample_tag="${ref##*:}"
    return 0
  fi

  sample_image_name="${ref}"
  sample_tag="latest"
}

sample_image="${SAMPLE_IMAGE:-registry.k8s.io/e2e-test-images/agnhost:2.53}"
split_image_ref "${sample_image}"

tmp_dir="$(mktemp -d "${ROOT_DIR}/.sample-render.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "${tmp_dir}/sample"
cp "${ROOT_DIR}/sample/namespace.yaml" "${tmp_dir}/sample/namespace.yaml"
cp "${ROOT_DIR}/sample/deployment.yaml" "${tmp_dir}/sample/deployment.yaml"

if [ -n "${sample_digest}" ]; then
  cat >"${tmp_dir}/sample/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - namespace.yaml
  - deployment.yaml

namespace: sample

images:
  - name: sample
    newName: ${sample_image_name}
    digest: ${sample_digest}
EOF
else
  cat >"${tmp_dir}/sample/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - namespace.yaml
  - deployment.yaml

namespace: sample

images:
  - name: sample
    newName: ${sample_image_name}
    newTag: "${sample_tag}"
EOF
fi

kubectl kustomize "${tmp_dir}/sample"
