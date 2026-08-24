#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dependencies_state="$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl"
core_state="$stack_dir/helmfile.d/02-core.yaml.gotmpl"
makefile_dist="$stack_dir/Makefile.dist"

fail() {
  echo "nats-auth-callout-openbao-order: $*" >&2
  exit 1
}

release_block="$(awk '
  /^  - name: nats-auth-callout-service$/ { capture = 1 }
  capture { print }
  capture && /^  - name: / && $0 != "  - name: nats-auth-callout-service" { exit }
' "$dependencies_state")"

test -n "$release_block" ||
  fail "nats-auth-callout-service must be declared in the dependencies state with OpenBao"

printf '%s\n' "$release_block" |
  grep -Eq '^    needs:$' ||
  fail "nats-auth-callout-service must wait for OpenBao before its Vault-agent pod is created"

printf '%s\n' "$release_block" |
  grep -Eq '^      - vault-system/openbao-server$' ||
  fail "nats-auth-callout-service must depend on vault-system/openbao-server"

printf '%s\n' "$release_block" |
  grep -Eq '^      release-group: dependencies$' ||
  fail "nats-auth-callout-service must remain in the dependencies release group"

if grep -Fqx '  - name: nats-auth-callout-service' "$core_state"; then
  fail "nats-auth-callout-service must not also be declared in the core state"
fi

# A name selector otherwise skips dependencies by default. Include the full
# OpenBao -> NATS chain for supported selected install/apply operations.
for target in install apply; do
  target_block="$(awk -v target="$target" '
    $0 ~ "^" target ":" { capture = 1 }
    capture { print }
    capture && /^[-[:alnum:]_]+:/ && $0 !~ "^" target ":" { exit }
  ' "$makefile_dist")"
  printf '%s\n' "$target_block" |
    grep -Fq -- '--include-transitive-needs' ||
    fail "$target must include transitive Helmfile dependencies when HELMFILE_SELECTOR is set"
done

echo "nats-auth-callout-openbao-order: PASS"
