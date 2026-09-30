#!/usr/bin/env bash
# Test that api.jwt.trustedIssuers / nvctApi.jwt.trustedIssuers reach the NVCF
# and NVCT APIs as additional inbound JWT issuers. Each service used to accept
# exactly one iss value, so an operator whose callers are signed by their own
# identity provider had to repoint api.jwt.issuerUri -- which replaces the
# built-in issuer instead of adding to it and breaks internal service-to-service
# traffic. The list is additive: the primary issuer env stays alongside it.
#
# Entries render as NVCF_/NVCT_SECURITY_JWT_TRUSTEDISSUERS_<i>_ISSUERURI and
# _JWKSETURI. Spring's relaxed binding reads the underscore-delimited number as
# a list index and ignores the missing dashes, so those names bind to
# nvcf.security.jwt.trusted-issuers[<i>].issuer-uri and its jwk-set-uri.
#
# global.yaml.gotmpl is the shared values source for every release, so it emits
# both the api: and nvctApi: top-level blocks into any release it renders;
# rendering the api release therefore exposes both for assertion.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="jwt-trusted-issuers-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "jwt-trusted-issuers-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

run_helmfile() {
  HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=api \
    "$@"
}

render_values() {
  local output_file="$1"
  local render_log="$work_dir/write-values.log"
  if ! run_helmfile write-values --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "helmfile wrote no values to $output_file"
}

expect_render_failure() {
  local expected_error="$1"
  local render_log="$work_dir/render-failure.log"
  if run_helmfile write-values \
    --output-file-template "$work_dir/should-not-exist.yaml" >"$render_log" 2>&1; then
    fail "expected the stack to reject this configuration: $expected_error"
  fi
  grep -Fq "$expected_error" "$render_log" ||
    fail "error did not contain the expected message: $expected_error"
}

assert_value() {
  local file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

primary_issuer="http://api.nvcf.svc.cluster.local"
primary_jwks="$primary_issuer/v2/nvcf/services/nvcf-api/jwt/jwks"
first_issuer="https://issuer.example.test"
first_jwks="https://issuer.example.test/.well-known/jwks.json"
second_issuer="https://other-issuer.example.test"
second_jwks="https://other-issuer.example.test/.well-known/jwks.json"

# ---------------------------------------------------------------------------
# 1. Default: no trustedIssuers configured. No indexed keys are emitted, so
#    each service stays on its single built-in issuer -- unchanged behavior.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_values="$work_dir/default-values.yaml"
render_values "$default_values"
assert_value "$default_values" '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI' null \
  "default: no NVCF trusted issuer emitted"
assert_value "$default_values" '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_JWKSETURI' null \
  "default: no NVCF trusted JWKS emitted"
assert_value "$default_values" '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI' null \
  "default: no NVCT trusted issuer emitted"
assert_value "$default_values" '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_0_JWKSETURI' null \
  "default: no NVCT trusted JWKS emitted"

# ---------------------------------------------------------------------------
# 2. One trusted issuer alongside an explicit primary issuer. Both must be
#    present: the trusted list adds to the primary issuer, never replaces it.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
api:
  jwt:
    issuerUri: $primary_issuer
    jwkSetUri: $primary_jwks
    trustedIssuers:
      - issuerUri: $first_issuer
        jwkSetUri: $first_jwks
nvctApi:
  jwt:
    issuerUri: $primary_issuer
    jwkSetUri: $primary_jwks
    trustedIssuers:
      - issuerUri: $first_issuer
        jwkSetUri: $first_jwks
EOF

single_values="$work_dir/single-values.yaml"
render_values "$single_values"
assert_value "$single_values" \
  '.api.env.SPRING_SECURITY_OAUTH2_RESOURCESERVER_JWT_ISSUER_URI' "$primary_issuer" \
  "single: NVCF primary issuer still emitted"
assert_value "$single_values" \
  '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI' "$first_issuer" \
  "single: NVCF trusted issuer emitted"
assert_value "$single_values" \
  '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_JWKSETURI' "$first_jwks" \
  "single: NVCF trusted JWKS emitted"
assert_value "$single_values" \
  '.nvctApi.env.SPRING_SECURITY_OAUTH2_RESOURCESERVER_JWT_ISSUER_URI' "$primary_issuer" \
  "single: NVCT primary issuer still emitted"
assert_value "$single_values" \
  '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI' "$first_issuer" \
  "single: NVCT trusted issuer emitted"
assert_value "$single_values" \
  '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_0_JWKSETURI' "$first_jwks" \
  "single: NVCT trusted JWKS emitted"

# ---------------------------------------------------------------------------
# 3. Two entries: the second must land on index 1. A list that collapsed onto
#    one index would silently drop an issuer.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
api:
  jwt:
    trustedIssuers:
      - issuerUri: $first_issuer
        jwkSetUri: $first_jwks
      - issuerUri: $second_issuer
        jwkSetUri: $second_jwks
nvctApi:
  jwt:
    trustedIssuers:
      - issuerUri: $first_issuer
        jwkSetUri: $first_jwks
      - issuerUri: $second_issuer
        jwkSetUri: $second_jwks
EOF

multiple_values="$work_dir/multiple-values.yaml"
render_values "$multiple_values"
assert_value "$multiple_values" \
  '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI' "$first_issuer" \
  "multiple: NVCF first issuer on index 0"
assert_value "$multiple_values" \
  '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_1_ISSUERURI' "$second_issuer" \
  "multiple: NVCF second issuer on index 1"
assert_value "$multiple_values" \
  '.api.env.NVCF_SECURITY_JWT_TRUSTEDISSUERS_1_JWKSETURI' "$second_jwks" \
  "multiple: NVCF second JWKS on index 1"
assert_value "$multiple_values" \
  '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_1_ISSUERURI' "$second_issuer" \
  "multiple: NVCT second issuer on index 1"
assert_value "$multiple_values" \
  '.nvctApi.env.NVCT_SECURITY_JWT_TRUSTEDISSUERS_1_JWKSETURI' "$second_jwks" \
  "multiple: NVCT second JWKS on index 1"

# ---------------------------------------------------------------------------
# 4. An entry missing its JWKS is rejected at render time. The service skips
#    half-filled entries, so without this the issuer would be silently ignored.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
api:
  jwt:
    trustedIssuers:
      - issuerUri: $first_issuer
EOF

expect_render_failure "api.jwt.trustedIssuers[].jwkSetUri must be a non-empty string"

# ---------------------------------------------------------------------------
# 5. The same guard applies to NVCT, and to a list given as a bare string.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nvctApi:
  jwt:
    trustedIssuers:
      - jwkSetUri: $first_jwks
EOF

expect_render_failure "nvctApi.jwt.trustedIssuers[].issuerUri must be a non-empty string"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
api:
  jwt:
    trustedIssuers: https://issuer.example.test
EOF

expect_render_failure "api.jwt.trustedIssuers must be a list"

echo "jwt-trusted-issuers-wiring: OK"
