#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Regression: destroying one named control plane must delete only its owned
# namespaces. Another plane and intentionally shared prerequisites must remain.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
fake_bin="$work_dir/bin"
calls_file="$work_dir/kubectl.calls"
helmfile_calls_file="$work_dir/helmfile.calls"
prepared_file="$work_dir/prepared.namespaces"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "control-plane-lifecycle: $*" >&2
  exit 1
}

mkdir -p "$fake_bin"
cp -R "$stack_dir" "$test_stack_dir"

printf '%s\n' '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'printf "%s\n" "$*" >>"$HELMFILE_CALLS_FILE"' \
  'case " $* " in *" destroy "*|*" sync "*) exit 0 ;; *) exit 1 ;; esac' \
  >"$fake_bin/helmfile"
printf '%s\n' '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'printf "%s\n" "$*" >>"$KUBECTL_CALLS_FILE"' \
  'if test "${1:-}" = get && test "${2:-}" = namespace; then' \
  '  test "${FAKE_EXISTS:-0}" = 1 || exit 1' \
  '  case " $* " in *" jsonpath="*) printf "%s" "${FAKE_OWNER:-}" ;; esac' \
  'fi' \
  'if test "${1:-}" = get && test "${2:-}" = clusterissuers; then' \
  '  case " $* " in' \
  '    *"control-plane-id=alpha "*) printf "%s\n" clusterissuer.cert-manager.io/alpha-nvcf-openbao-pki clusterissuer.cert-manager.io/alpha-retained-pki ;;' \
  '    *"control-plane-id=gamma "*) printf "%s\n" clusterissuer.cert-manager.io/gamma-nvcf-openbao-pki ;;' \
  '  esac' \
  'fi' \
  'if test "${1:-}" = get && test "${2:-}" = clusterissuer; then' \
  '  if test -n "${FAKE_ISSUER_OWNER_OVERRIDE:-}"; then printf "%s" "$FAKE_ISSUER_OWNER_OVERRIDE"; else' \
  '    case "${3:-}" in alpha-*) printf alpha ;; beta-*) printf beta ;; gamma-*) printf gamma ;; *) printf external ;; esac' \
  '  fi' \
  'fi' \
  'if test "${1:-}" = label && test "${2:-}" = namespace && test -n "${PREPARED_FILE:-}"; then' \
  '  printf "%s\n" "${3:-}" >>"$PREPARED_FILE"' \
  'fi' \
  'exit 0' \
  >"$fake_bin/kubectl"
chmod +x "$fake_bin/helmfile" "$fake_bin/kubectl"

PATH="$fake_bin:$PATH" \
  KUBECTL_CALLS_FILE="$calls_file" \
  HELMFILE_CALLS_FILE="$helmfile_calls_file" \
  FAKE_EXISTS=1 \
  FAKE_OWNER=alpha \
  make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
    destroy DEV_MODE=1 HELMFILE_ENV=base \
    CONTROL_PLANE_ID=alpha CONTROL_PLANE_DOMAIN=alpha.example.test >/dev/null

helmfile_call="$(<"$helmfile_calls_file")"
for argument in \
  '--state-values-set-string global.controlPlane.id=alpha' \
  '--state-values-set-string global.controlPlane.sharedInfrastructure=external' \
  '--state-values-set certManager.enabled=false' \
  '--state-values-set-string global.domain=alpha.example.test'; do
  grep -Fq -- "$argument" <<<"$helmfile_call" ||
    fail "destroy did not pass named-plane state argument: $argument"
done

deleted_namespaces="$(sed -n 's/^delete namespace \([^ ]*\).*/\1/p' "$calls_file" | sort -u)"
for namespace in \
  alpha-api-keys \
  alpha-cassandra-system \
  alpha-ess \
  alpha-ingress \
  alpha-nats-system \
  alpha-nvcf \
  alpha-sis \
  alpha-vault-system; do
  grep -Fxq "$namespace" <<<"$deleted_namespaces" ||
    fail "destroy did not delete owned namespace $namespace"
done

for namespace in \
  beta-nvcf \
  beta-vault-system \
  cert-manager \
  gateway-system \
  nvcf \
  vault-system; do
  if grep -Fxq "$namespace" <<<"$deleted_namespaces"; then
    fail "destroy deleted unowned namespace $namespace"
  fi
done

expected_count=8
actual_count="$(printf '%s\n' "$deleted_namespaces" | sed '/^$/d' | wc -l | tr -d ' ')"
test "$actual_count" = "$expected_count" ||
  fail "destroy deleted $actual_count namespaces, expected exactly $expected_count"

# Managed ClusterIssuers carry the same plane owner label but are retained by
# Helm. Named destroy must explicitly remove all current/retained issuers owned
# by alpha without touching beta or an externally managed issuer.
deleted_clusterissuers="$(sed -n 's/^delete clusterissuer \([^ ]*\).*/\1/p' "$calls_file" | sort -u)"
for issuer in alpha-nvcf-openbao-pki alpha-retained-pki; do
  grep -Fxq "$issuer" <<<"$deleted_clusterissuers" ||
    fail "destroy did not delete owned managed ClusterIssuer $issuer"
done
for issuer in beta-nvcf-openbao-pki external-shared-pki; do
  if grep -Fxq "$issuer" <<<"$deleted_clusterissuers"; then
    fail "destroy deleted unowned ClusterIssuer $issuer"
  fi
done
grep -Fq 'get clusterissuers -l nvcf.nvidia.com/control-plane-id=alpha -o name' "$calls_file" ||
  fail 'destroy did not select managed ClusterIssuers by plane owner label'

# Re-check ownership on each selected object so a stale list result or label
# race fails closed instead of deleting an issuer that is no longer alpha's.
: >"$calls_file"
if PATH="$fake_bin:$PATH" \
    KUBECTL_CALLS_FILE="$calls_file" \
    HELMFILE_CALLS_FILE="$helmfile_calls_file" \
    FAKE_EXISTS=1 \
    FAKE_OWNER=alpha \
    FAKE_ISSUER_OWNER_OVERRIDE=beta \
    make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
      destroy DEV_MODE=1 HELMFILE_ENV=base \
      CONTROL_PLANE_ID=alpha CONTROL_PLANE_DOMAIN=alpha.example.test \
      >"$work_dir/foreign-issuer-owner.log" 2>&1; then
  fail 'destroy accepted a managed ClusterIssuer whose owner changed'
fi
if grep -q '^delete clusterissuer ' "$calls_file"; then
  fail 'destroy deleted a managed ClusterIssuer after its owner changed'
fi

# Empty ID remains backward compatible with the historical namespace set.
: >"$calls_file"
PATH="$fake_bin:$PATH" \
  KUBECTL_CALLS_FILE="$calls_file" \
  HELMFILE_CALLS_FILE="$helmfile_calls_file" \
  FAKE_EXISTS=1 \
  make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
    destroy DEV_MODE=1 HELMFILE_ENV=base CONTROL_PLANE_ID= >/dev/null
legacy_deleted="$(sed -n 's/^delete namespace \([^ ]*\).*/\1/p' "$calls_file" | sort -u)"
for namespace in api-keys cassandra-system ess nats-system ncp nvcf sis vault-system; do
  grep -Fxq "$namespace" <<<"$legacy_deleted" ||
    fail "legacy destroy did not delete namespace $namespace"
done
if grep -q '^delete clusterissuer ' "$calls_file"; then
  fail 'legacy destroy deleted a retained unlabeled ClusterIssuer'
fi

# Existing unlabelled or foreign-owned namespaces must stop the destroy before
# Helmfile can uninstall releases from them.
for foreign_owner in '' beta; do
  : >"$calls_file"
  : >"$helmfile_calls_file"
  if PATH="$fake_bin:$PATH" \
      KUBECTL_CALLS_FILE="$calls_file" \
      HELMFILE_CALLS_FILE="$helmfile_calls_file" \
      FAKE_EXISTS=1 \
      FAKE_OWNER="$foreign_owner" \
      make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
        destroy DEV_MODE=1 HELMFILE_ENV=base \
        CONTROL_PLANE_ID=alpha CONTROL_PLANE_DOMAIN=alpha.example.test \
        >"$work_dir/foreign-owner.log" 2>&1; then
    fail "destroy accepted namespace owner ${foreign_owner:-<unset>}"
  fi
  test ! -s "$helmfile_calls_file" ||
    fail "destroy invoked Helmfile before rejecting namespace owner ${foreign_owner:-<unset>}"
  if grep -q '^delete namespace ' "$calls_file"; then
    fail "destroy deleted a namespace owned by ${foreign_owner:-<unset>}"
  fi
done

# A stable identity may live in the selected environment instead of the Make
# command. Lifecycle ownership must resolve that value before choosing what to
# delete.
environment_name=lifecycle-plane
printf '%s\n' \
  'global:' \
  '  controlPlane:' \
  '    id: gamma' \
  '  domain: gamma.example.test' \
  >"$test_stack_dir/environments/$environment_name.yaml"
: >"$calls_file"
: >"$helmfile_calls_file"
PATH="$fake_bin:$PATH" \
  KUBECTL_CALLS_FILE="$calls_file" \
  HELMFILE_CALLS_FILE="$helmfile_calls_file" \
  FAKE_EXISTS=1 \
  FAKE_OWNER=gamma \
  make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
    destroy DEV_MODE=1 HELMFILE_ENV="$environment_name" >/dev/null
grep -Fq -- '--state-values-set-string global.controlPlane.id=gamma' \
  "$helmfile_calls_file" ||
  fail 'environment-configured identity did not reach Helmfile destroy'
grep -Fq 'delete namespace gamma-nvcf --wait=true' "$calls_file" ||
  fail 'environment-configured identity did not select owned namespace cleanup'

if PATH="$fake_bin:$PATH" \
    make --no-print-directory -f "$test_stack_dir/Makefile.dist" \
      validate-control-plane-id CONTROL_PLANE_ID=alpha HELMFILE_ENV=base \
      >"$work_dir/missing-domain.log" 2>&1; then
  fail 'named control plane accepted a missing unique domain'
fi
grep -Fq 'CONTROL_PLANE_DOMAIN is required' "$work_dir/missing-domain.log" ||
  fail 'missing domain did not return the expected error'

# Namespace preparation and pre-install hooks are state mutations that must not
# race even when an operator invokes Make with -j. The hook sees all namespaces
# only if the install prerequisites are serialized in declaration order.
parallel_makefile="$work_dir/parallel.Makefile"
printf '%s\n' \
  "include $test_stack_dir/Makefile.dist" \
  '.PHONY: assert-namespaces-prepared' \
  'assert-namespaces-prepared:' \
  $'\t@test "$$(wc -l < "$(PREPARED_FILE)")" -eq 8' \
  >"$parallel_makefile"
: >"$calls_file"
: >"$helmfile_calls_file"
: >"$prepared_file"
PATH="$fake_bin:$PATH" \
  KUBECTL_CALLS_FILE="$calls_file" \
  HELMFILE_CALLS_FILE="$helmfile_calls_file" \
  PREPARED_FILE="$prepared_file" \
  FAKE_EXISTS=0 \
  make --no-print-directory -j4 -f "$parallel_makefile" \
    install DEV_MODE=1 HELMFILE_ENV=base INSTALL_PRE_HOOKS=assert-namespaces-prepared \
    CONTROL_PLANE_ID=alpha CONTROL_PLANE_DOMAIN=alpha.example.test >/dev/null ||
  fail 'parallel install allowed namespace preparation and install hooks to race'

echo 'control-plane-lifecycle: all checks passed'
