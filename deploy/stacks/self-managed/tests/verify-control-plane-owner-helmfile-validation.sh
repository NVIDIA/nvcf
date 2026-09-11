#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-helmfile-validation: $*" >&2
  exit 1
}

command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"

states=(
  "01-dependencies.yaml.gotmpl"
  "02-core.yaml.gotmpl"
  "03-observability.yaml.gotmpl"
)
invalid_owners=(
  "shared"
  "PlaneA"
  "control-plane-id-that-is-too-long"
)

for state in "${states[@]}"; do
  state_file="$stack_dir/helmfile.d/$state"
  valid_log="$work_dir/$state.valid.log"
  if ! HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=plane-a \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.valid.json" 2>"$valid_log"; then
    fail "$state rejected valid owner plane-a: $(tr '\n' ' ' <"$valid_log")"
  fi

  for owner in "${invalid_owners[@]}"; do
    invalid_log="$work_dir/$state.$owner.invalid.log"
    if HELMFILE_ENV=base \
      NVCF_CONTROL_PLANE_OWNER="$owner" \
      HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
      helmfile --file "$state_file" list --skip-charts --output json \
      >"$work_dir/$state.$owner.invalid.json" 2>"$invalid_log"; then
      fail "$state accepted invalid owner $owner"
    fi
    if ! grep -q "NVCF_CONTROL_PLANE_OWNER must" "$invalid_log"; then
      fail "$state rejected $owner without the owner validation message"
    fi
  done
done

owner_dependencies="$work_dir/dependencies.owner.json"
HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile --file "$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  list --skip-charts --selector control-plane-owner=plane-a --output json \
  >"$owner_dependencies"
if grep -Eq '"name":[[:space:]]*"cert-manager"' "$owner_dependencies"; then
  fail "owner selector included shared cert-manager release"
fi
if ! grep -Eq '"name":[[:space:]]*"nats"' "$owner_dependencies"; then
  fail "owner selector did not include plane-owned dependency release"
fi

shared_dependencies="$work_dir/dependencies.shared.json"
HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile --file "$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  list --skip-charts --selector control-plane-owner=shared --output json \
  >"$shared_dependencies"
if ! grep -Eq '"name":[[:space:]]*"cert-manager"' "$shared_dependencies"; then
  fail "shared selector did not include cert-manager release"
fi
if grep -Eq '"name":[[:space:]]*"(nats|openbao-server|cassandra)"' "$shared_dependencies"; then
  fail "shared selector included a plane-owned dependency release"
fi

install_plan="$work_dir/make-install.txt"
make -n -C "$stack_dir" install \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$install_plan" 2>&1
if ! grep -Fq 'renderers/mark-control-plane-namespaces.sh' "$install_plan"; then
  fail "make install did not mark control-plane namespaces"
fi
if ! grep -Fq -- '--primary-namespace "nvcf"' "$install_plan"; then
  fail "make install did not identify nvcf as the primary namespace"
fi
if ! grep -Fq -- '--stack "self-managed"' "$install_plan"; then
  fail "make install did not include the self-managed stack annotation"
fi
if ! grep -Fq -- '--chart-version "1.25.1"' "$install_plan"; then
  fail "make install did not parse the primary chart version"
fi
if ! grep -Fq -- '--nvca-operator-version "not-installed"' "$install_plan"; then
  fail "make install did not record that nvca-operator is not installed in this stack"
fi
if ! grep -Fq -- '--shared-namespace "cert-manager"' "$install_plan"; then
  fail "make install did not include the shared cert-manager namespace"
fi

apply_plan="$work_dir/make-apply.txt"
make -n -C "$stack_dir" apply \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$apply_plan" 2>&1
if ! grep -Fq 'renderers/mark-control-plane-namespaces.sh' "$apply_plan"; then
  fail "make apply did not mark control-plane namespaces"
fi

destroy_plan="$work_dir/make-destroy.txt"
make -n -C "$stack_dir" destroy \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  >"$destroy_plan" 2>&1
if ! grep -Fq -- '--selector "control-plane-owner=plane-a" destroy' "$destroy_plan"; then
  fail "make destroy did not pass the owner selector"
fi
if ! grep -Fq 'Skipping fixed namespace cleanup for named control-plane owner plane-a' "$destroy_plan"; then
  fail "make destroy did not skip fixed namespace cleanup for named owners"
fi

default_destroy_plan="$work_dir/make-destroy-default.txt"
make -n -C "$stack_dir" destroy \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=default \
  >"$default_destroy_plan" 2>&1
if ! grep -Fq 'renderers/delete-owned-namespace.sh' "$default_destroy_plan"; then
  fail "make destroy did not use owner-aware namespace cleanup"
fi

echo "verify-control-plane-owner-helmfile-validation: all checks passed"
