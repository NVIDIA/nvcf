#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_stack_dir="$(cd "$script_dir/.." && pwd)"
work_dir="$(mktemp -d)"
stack_dir="$work_dir/stack"
helmfile_dir="$stack_dir/helmfile.d"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-helmfile-validation: $*" >&2
  exit 1
}

helmfile_env() {
  (
    cd "$helmfile_dir"
    HELMFILE_ENV=local \
      NVCF_CONTROL_PLANE_OWNER="${NVCF_CONTROL_PLANE_OWNER:-default}" \
      CLUSTER_NAME=ncp-local \
      NCA_ID=ncp-local \
      OUTPUT_DIR="$work_dir" \
      HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
      "$@"
  )
}

command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"

max_owner="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
too_long_owner="${max_owner}a"

mkdir -p "$stack_dir"
cp -R "$source_stack_dir/helmfile.d" "$stack_dir/"
cp -R "$source_stack_dir/environments" "$stack_dir/"
cp -R "$source_stack_dir/charts" "$stack_dir/"
cp "$source_stack_dir/global.yaml.gotmpl" "$stack_dir/"
cp "$source_stack_dir/testdata/environments/local.yaml" "$stack_dir/environments/local.yaml"
cp "$source_stack_dir/testdata/registration/ncp-local-register-values.yaml" \
  "$work_dir/ncp-local-register-values.yaml"

nvca_state="02-nvca.yaml.gotmpl"
dependencies_state="01-dependencies.yaml.gotmpl"

valid_log="$work_dir/nvca.valid.log"
if ! NVCF_CONTROL_PLANE_OWNER=plane-a helmfile_env \
  helmfile --file "$nvca_state" list --skip-charts \
  --selector control-plane-owner=plane-a --output json \
  >"$work_dir/nvca.valid.json" 2>"$valid_log"; then
  fail "02-nvca rejected valid owner plane-a: $(tr '\n' ' ' <"$valid_log")"
fi
if ! grep -Eq '"name":[[:space:]]*"nvca-operator"' "$work_dir/nvca.valid.json"; then
  fail "owner selector did not include nvca-operator release"
fi

max_valid_log="$work_dir/nvca.max-valid.log"
if ! NVCF_CONTROL_PLANE_OWNER="$max_owner" helmfile_env \
  helmfile --file "$nvca_state" list --skip-charts \
  --selector control-plane-owner="$max_owner" --output json \
  >"$work_dir/nvca.max-valid.json" 2>"$max_valid_log"; then
  fail "02-nvca rejected 30-character owner: $(tr '\n' ' ' <"$max_valid_log")"
fi

invalid_owners=(
  "shared"
  "PlaneA"
  "$too_long_owner"
)

for owner in "${invalid_owners[@]}"; do
  invalid_log="$work_dir/nvca.$owner.invalid.log"
  if NVCF_CONTROL_PLANE_OWNER="$owner" helmfile_env \
    helmfile --file "$nvca_state" list --skip-charts --output json \
    >"$work_dir/nvca.$owner.invalid.json" 2>"$invalid_log"; then
    fail "02-nvca accepted invalid owner $owner"
  fi
  if ! grep -q "NVCF_CONTROL_PLANE_OWNER must" "$invalid_log"; then
    fail "02-nvca rejected $owner without the owner validation message"
  fi
done

shared_dependencies="$work_dir/dependencies.shared.json"
if ! NVCF_CONTROL_PLANE_OWNER=plane-a helmfile_env \
  helmfile --file "$dependencies_state" list --skip-charts \
  --selector control-plane-owner=shared --output json \
  >"$shared_dependencies" 2>"$work_dir/dependencies.shared.log"; then
  fail "shared selector failed for dependencies: $(tr '\n' ' ' <"$work_dir/dependencies.shared.log")"
fi

for release_name in kai-scheduler nvcf-kai-topology grove-operator nvcf-grove-topology dynamo-operator; do
  if ! grep -Eq '"name":[[:space:]]*"'"$release_name"'"' "$shared_dependencies"; then
    fail "shared selector did not include $release_name release"
  fi
done

owner_dependencies="$work_dir/dependencies.owner.json"
owner_dependencies_log="$work_dir/dependencies.owner.log"
if NVCF_CONTROL_PLANE_OWNER=plane-a helmfile_env \
  helmfile --file "$dependencies_state" list --skip-charts \
  --selector control-plane-owner=plane-a --output json \
  >"$owner_dependencies" 2>"$owner_dependencies_log"; then
  if grep -Eq '"name":[[:space:]]*"(kai-scheduler|nvcf-kai-topology|grove-operator|nvcf-grove-topology|dynamo-operator)"' "$owner_dependencies"; then
    fail "owner selector included a shared dependency release"
  fi
else
  if ! grep -Eiq 'no releases|matching|selector' "$owner_dependencies_log"; then
    fail "owner selector failed unexpectedly: $(tr '\n' ' ' <"$owner_dependencies_log")"
  fi
fi

install_plan="$work_dir/make-install.txt"
make -n -C "$source_stack_dir" install \
  DEV_MODE=1 \
  CLUSTER_NAME=ncp-local \
  HELMFILE_ENV=local \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$install_plan" 2>&1
if ! grep -Fq 'renderers/mark-control-plane-namespaces.sh' "$install_plan"; then
  fail "make install did not mark control-plane namespaces"
fi
if ! grep -Fq -- '--primary-namespace "nvca-operator"' "$install_plan"; then
  fail "make install did not identify nvca-operator as the primary namespace"
fi
if ! grep -Fq -- '--stack "nvcf-compute-plane"' "$install_plan"; then
  fail "make install did not include the nvcf-compute-plane stack annotation"
fi
if ! grep -Fq -- '--chart-version "1.21.8"' "$install_plan"; then
  fail "make install did not parse the nvca-operator chart version"
fi
if ! grep -Fq -- '--nvca-operator-version "3.3.2"' "$install_plan"; then
  fail "make install did not parse the nvca-operator version"
fi
if ! grep -Fq -- '--shared-namespace "kai-scheduler"' "$install_plan"; then
  fail "make install did not include the shared KAI namespace"
fi

apply_plan="$work_dir/make-apply.txt"
make -n -C "$source_stack_dir" apply \
  DEV_MODE=1 \
  CLUSTER_NAME=ncp-local \
  HELMFILE_ENV=local \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$apply_plan" 2>&1
if ! grep -Fq 'renderers/mark-control-plane-namespaces.sh' "$apply_plan"; then
  fail "make apply did not mark control-plane namespaces"
fi

destroy_plan="$work_dir/make-destroy.txt"
make -n -C "$source_stack_dir" destroy \
  DEV_MODE=1 \
  CLUSTER_NAME=ncp-local \
  HELMFILE_ENV=local \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$destroy_plan" 2>&1
if ! grep -Fq -- '--selector "control-plane-owner=plane-a" destroy' "$destroy_plan"; then
  fail "make destroy did not pass the owner selector"
fi
if ! grep -Fq 'Skipping fixed namespace cleanup for named control-plane owner plane-a' "$destroy_plan"; then
  fail "make destroy did not skip fixed namespace cleanup for named owners"
fi

default_destroy_plan="$work_dir/make-destroy-default.txt"
make -n -C "$source_stack_dir" destroy \
  DEV_MODE=1 \
  CLUSTER_NAME=ncp-local \
  HELMFILE_ENV=local \
  NVCF_CONTROL_PLANE_OWNER=default \
  >"$default_destroy_plan" 2>&1
if ! grep -Fq 'renderers/delete-owned-namespace.sh' "$default_destroy_plan"; then
  fail "make destroy did not use owner-aware namespace cleanup"
fi

echo "verify-control-plane-owner-helmfile-validation: all checks passed"
