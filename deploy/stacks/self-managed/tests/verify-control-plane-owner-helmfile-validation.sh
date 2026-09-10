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

max_owner="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
too_long_owner="${max_owner}a"
states=(
  "01-dependencies.yaml.gotmpl"
  "02-core.yaml.gotmpl"
  "03-observability.yaml.gotmpl"
)
invalid_owners=(
  "shared"
  "PlaneA"
  "$too_long_owner"
)

for state in "${states[@]}"; do
  state_file="$stack_dir/helmfile.d/$state"
  if ! grep -Fq '$controlPlaneNamespacePrefix = printf "%s-" $controlPlaneOwner' "$state_file"; then
    fail "$state does not derive namespace prefix from the control-plane owner"
  fi

  default_log="$work_dir/$state.default.log"
  if ! HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=default \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.default.json" 2>"$default_log"; then
    fail "$state rejected default owner: $(tr '\n' ' ' <"$default_log")"
  fi

  named_blocked_log="$work_dir/$state.named-blocked.log"
  if HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=plane-a \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.named-blocked.json" 2>"$named_blocked_log"; then
    fail "$state rendered named owner before object-level identity derivation was wired"
  fi
  if ! grep -q "object-level identity derivation is not wired" "$named_blocked_log"; then
    fail "$state rejected named owner without the object-level identity message"
  fi

  max_valid_log="$work_dir/$state.max-valid.log"
  if HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER="$max_owner" \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.max-valid.json" 2>"$max_valid_log"; then
    fail "$state rendered a 30-character named owner before object-level identity derivation was wired"
  fi
  if ! grep -q "object-level identity derivation is not wired" "$max_valid_log"; then
    fail "$state rejected 30-character owner before reaching object-level identity validation"
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

  alpha_log="$work_dir/$state.alpha-missing.log"
  if HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=plane-a \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.alpha-missing.json" 2>"$alpha_log"; then
    fail "$state accepted named owner without alpha opt-in"
  fi
  if ! grep -q "NVCF_ALPHA_NAMED_CONTROL_PLANE=true is required" "$alpha_log"; then
    fail "$state rejected missing alpha opt-in without the alpha validation message"
  fi

  bad_alpha_log="$work_dir/$state.bad-alpha.log"
  if HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=plane-a \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=yes \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$state_file" list --skip-charts --output json \
    >"$work_dir/$state.bad-alpha.json" 2>"$bad_alpha_log"; then
    fail "$state accepted invalid alpha opt-in value"
  fi
  if ! grep -q "NVCF_ALPHA_NAMED_CONTROL_PLANE must be true or false" "$bad_alpha_log"; then
    fail "$state rejected invalid alpha opt-in without the alpha validation message"
  fi
done

global_chart_dir="$work_dir/global-guard-chart"
global_state_file="$work_dir/global-guard.yaml.gotmpl"
mkdir -p "$global_chart_dir/templates"
if ! grep -Fq '$controlPlaneNamespacePrefix = printf "%s-" $controlPlaneOwner' "$stack_dir/global.yaml.gotmpl"; then
  fail "global.yaml.gotmpl does not derive namespace prefix from the control-plane owner"
fi
cat >"$global_chart_dir/Chart.yaml" <<'YAML'
apiVersion: v2
name: global-guard
version: 0.1.0
YAML
cat >"$global_chart_dir/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: global-guard
data:
  ok: "true"
YAML
cat >"$global_state_file" <<YAML
environments:
  default:
    values:
      - $stack_dir/environments/base.yaml
      - $stack_dir/testdata/environments/local.yaml

---

releases:
  - name: global-guard
    chart: $global_chart_dir
    values:
      - $stack_dir/global.yaml.gotmpl
YAML

global_alpha_log="$work_dir/global.alpha-missing.log"
if HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile --file "$global_state_file" template \
  >"$work_dir/global.alpha-missing.yaml" 2>"$global_alpha_log"; then
  fail "global.yaml.gotmpl accepted named owner without alpha opt-in"
fi
if ! grep -q "NVCF_ALPHA_NAMED_CONTROL_PLANE=true is required" "$global_alpha_log"; then
  fail "global.yaml.gotmpl rejected missing alpha opt-in without the alpha validation message"
fi

global_named_blocked_log="$work_dir/global.named-blocked.log"
if HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile --file "$global_state_file" template \
  >"$work_dir/global.named-blocked.yaml" 2>"$global_named_blocked_log"; then
  fail "global.yaml.gotmpl rendered named owner before object-level identity derivation was wired"
fi
if ! grep -q "object-level identity derivation is not wired" "$global_named_blocked_log"; then
  fail "global.yaml.gotmpl rejected named owner without the object-level identity message"
fi
owner_dependencies="$work_dir/dependencies.owner.json"
HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=default \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile --file "$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  list --skip-charts --selector control-plane-owner=default --output json \
  >"$owner_dependencies"
if grep -Eq '"name":[[:space:]]*"cert-manager"' "$owner_dependencies"; then
  fail "owner selector included shared cert-manager release"
fi
if ! grep -Eq '"name":[[:space:]]*"nats"' "$owner_dependencies"; then
  fail "owner selector did not include plane-owned dependency release"
fi

shared_dependencies="$work_dir/dependencies.shared.json"
HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=default \
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
  NVCF_CONTROL_PLANE_OWNER=default \
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

named_install_plan="$work_dir/make-install-named.txt"
make -n -C "$stack_dir" install \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
  >"$named_install_plan" 2>&1
if ! grep -Fq -- '--primary-namespace "plane-a-nvcf"' "$named_install_plan"; then
  fail "make install did not identify plane-a-nvcf as the named primary namespace"
fi
if ! grep -Fq -- '--namespace "plane-a-cassandra-system"' "$named_install_plan"; then
  fail "make install did not include the named Cassandra namespace"
fi

apply_plan="$work_dir/make-apply.txt"
make -n -C "$stack_dir" apply \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=default \
  >"$apply_plan" 2>&1
if ! grep -Fq 'renderers/mark-control-plane-namespaces.sh' "$apply_plan"; then
  fail "make apply did not mark control-plane namespaces"
fi

destroy_plan="$work_dir/make-destroy.txt"
make -n -C "$stack_dir" destroy \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=default \
  >"$destroy_plan" 2>&1
if ! grep -Fq -- '--selector "control-plane-owner=default" destroy' "$destroy_plan"; then
  fail "make destroy did not pass the owner selector"
fi
if ! grep -Fq 'renderers/delete-owned-namespace.sh' "$destroy_plan"; then
  fail "make destroy did not use owner-aware namespace cleanup"
fi

named_destroy_plan="$work_dir/make-destroy-named.txt"
make -n -C "$stack_dir" destroy \
  DEV_MODE=1 \
  HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
  >"$named_destroy_plan" 2>&1
if ! grep -Fq -- '--selector "control-plane-owner=plane-a" destroy' "$named_destroy_plan"; then
  fail "make destroy did not pass the named owner selector"
fi
if ! grep -Fq 'Skipping named namespace cleanup until shared prerequisite lifecycle is wired' "$named_destroy_plan"; then
  fail "make destroy did not defer named namespace cleanup"
fi

echo "verify-control-plane-owner-helmfile-validation: all checks passed"
