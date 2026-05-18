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
  SAMPLE_IMAGE          Full image path without tag. Overrides the NGC path.
  SAMPLE_IMAGE_REGISTRY Registry hostname. Default: nvcr.io
  SAMPLE_NGC_ORG       NGC org path segment. Required unless SAMPLE_IMAGE is set.
  SAMPLE_NGC_TEAM      NGC team path segment. Required unless SAMPLE_IMAGE is set.
  SAMPLE_IMAGE_NAME    Image repository name. Default: alpine-k8s
  SAMPLE_IMAGE_TAG     Image tag. Default: 1.30.12

Example:
  SAMPLE_NGC_ORG=my-org SAMPLE_NGC_TEAM=my-team make deploy-sample
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

sample_registry="${SAMPLE_IMAGE_REGISTRY:-nvcr.io}"
sample_org="${SAMPLE_NGC_ORG:-ngc-org}"
sample_team="${SAMPLE_NGC_TEAM:-ngc-team}"
sample_name="${SAMPLE_IMAGE_NAME:-alpine-k8s}"
sample_tag="${SAMPLE_IMAGE_TAG:-1.30.12}"

if [ -n "${SAMPLE_IMAGE:-}" ]; then
  sample_image="${SAMPLE_IMAGE}"
else
  if [ "$sample_org" = "ngc-org" ] || [ "$sample_team" = "ngc-team" ]; then
    echo "ERROR: replace the sample image NGC placeholders before deploying." >&2
    echo "Set SAMPLE_NGC_ORG and SAMPLE_NGC_TEAM, for example:" >&2
    echo "  SAMPLE_NGC_ORG=my-org SAMPLE_NGC_TEAM=my-team make deploy-sample" >&2
    echo "Or set SAMPLE_IMAGE to a full image path for another registry." >&2
    exit 1
  fi
  sample_image="${sample_registry}/${sample_org}/${sample_team}/${sample_name}"
fi

tmp_dir="$(mktemp -d "${ROOT_DIR}/.sample-render.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

cp -R "${ROOT_DIR}/sample" "${tmp_dir}/sample"
cat >"${tmp_dir}/sample/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - namespace.yaml
  - deployment.yaml

namespace: sample

images:
  - name: sample
    newName: ${sample_image}
    newTag: ${sample_tag}
EOF

kubectl kustomize "${tmp_dir}/sample"
