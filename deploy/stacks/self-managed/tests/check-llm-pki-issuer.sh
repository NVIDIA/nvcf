#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "check-llm-pki-issuer: $*" >&2
  exit 1
}

helmfile_args=(
  --file "$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl"
  --environment default
)

# The router's default advertised hostname is the stable service name at
# replicaCount=1 and the per-pod headless name otherwise. Request both SANs,
# matching the documented operator configuration, so these cases stay valid
# at any replica count.
router_dns_names=(
  --state-values-set-string
  'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local'
  --state-values-set-string
  'addons.llm.pki.dnsNames[1]=*.llm-request-router-headless.nvcf.svc.cluster.local'
)

core_state_values=(
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy
)

render_list() {
  local case_name="$1"
  shift

  HELMFILE_ENV=base helmfile \
    "${helmfile_args[@]}" \
    "$@" \
    list --skip-charts --output json \
    >"$work_dir/$case_name.json"
}

render_debug() {
  local case_name="$1"
  shift

  HELMFILE_ENV=base helmfile \
    --log-level debug \
    "${helmfile_args[@]}" \
    "$@" \
    list --skip-charts --output json \
    >"$work_dir/$case_name.debug" 2>&1
}

expect_enabled() {
  local case_name="$1"
  local expected="$2"

  local actual
  actual="$(
    jq -r \
      'any(.[]; .name == "nvcf-pki" and .enabled == true)' \
      "$work_dir/$case_name.json"
  )"
  test "$actual" = "$expected" ||
    fail "$case_name expected nvcf-pki enabled=$expected, got $actual"
}

# Explicit null, empty, and quoted-boolean values cannot be expressed with
# --state-values-set or --state-values-set-string: those either leave the key
# unset, so dig falls back to its default, or coerce the type. Write a real
# YAML override file so the exact value and type reach the template. Body is
# read from stdin and indented under addons.llm.pki.
pki_override() {
  local case_name="$1"
  local override_file="$work_dir/$case_name.override.yaml"

  {
    printf 'addons:\n  llm:\n    pki:\n'
    sed 's/^/      /'
  } >"$override_file"
  printf '%s' "$override_file"
}

# Guards the defect this stack exists to prevent: a Certificate that names an
# issuer no release creates. Dangling is only acceptable when the operator
# explicitly declined management, so pass explicitly_external=true for those.
expect_no_dangling_issuer() {
  local case_name="$1"
  local explicitly_external="$2"
  local manifests_file="$work_dir/$case_name.router-manifests.yaml"

  local issuer_kind issuer_name pki_enabled
  issuer_kind="$(yq -rN 'select(.kind == "Certificate") | .spec.issuerRef.kind' "$manifests_file" | head -1)"
  issuer_name="$(yq -rN 'select(.kind == "Certificate") | .spec.issuerRef.name' "$manifests_file" | head -1)"
  pki_enabled="$(jq -r 'any(.[]; .name == "nvcf-pki" and .enabled == true)' "$work_dir/$case_name.json")"

  # Without this the check is vacuous: if the manifest shape changes and the
  # queries stop resolving, every case would take the "not the default issuer"
  # exit below and silently pass.
  if test -z "$issuer_kind" || test "$issuer_kind" = "null"; then
    fail "$case_name could not read .spec.issuerRef.kind from the rendered Certificate"
  fi
  if test -z "$issuer_name" || test "$issuer_name" = "null"; then
    fail "$case_name could not read .spec.issuerRef.name from the rendered Certificate"
  fi

  if test "$issuer_kind" != "ClusterIssuer" || test "$issuer_name" != "nvcf-openbao-pki"; then
    return 0
  fi
  if test "$pki_enabled" = "true"; then
    return 0
  fi
  if test "$explicitly_external" = "true"; then
    return 0
  fi
  fail "$case_name renders a Certificate for ClusterIssuer/nvcf-openbao-pki while the nvcf-pki release is absent and management was not explicitly declined"
}

# The ownership cases above render 01-dependencies on its own, which cannot see
# a second nvcf-pki declaration in a neighbouring state. `make` applies every
# state in helmfile.d in one invocation, so the ownership decision only holds if
# it holds across the whole directory.
render_list_all() {
  local case_name="$1"
  shift

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    "${core_state_values[@]}" \
    "$@" \
    list --skip-charts --output json \
    >"$work_dir/$case_name.all.json"
}

# Counts declarations, not enabled releases. A release that is merely
# conditioned off is still a second owner of the same cluster-scoped issuer and
# still reappears whenever its condition flips.
expect_declared_all() {
  local case_name="$1"
  local expected="$2"

  local actual
  actual="$(jq -r '[.[] | select(.name == "nvcf-pki")] | length' "$work_dir/$case_name.all.json")"
  test "$actual" = "$expected" ||
    fail "$case_name expected $expected nvcf-pki releases across helmfile.d, got $actual"
}

expect_failure() {
  local case_name="$1"
  local expected_error="$2"
  shift 2

  if HELMFILE_ENV=base helmfile \
    "${helmfile_args[@]}" \
    "$@" \
    list --skip-charts --output json \
    >"$work_dir/$case_name.log" 2>&1; then
    fail "$case_name rendered successfully"
  fi

  grep -Fq "$expected_error" "$work_dir/$case_name.log" ||
    fail "$case_name did not return the expected error: $expected_error"
}

render_router() {
  local case_name="$1"
  shift
  local values_file="$work_dir/$case_name.router-values.yaml"
  local manifests_file="$work_dir/$case_name.router-manifests.yaml"
  local router_chart="$stack_dir/../../helm/llm-request-router/llm-request-router"

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=llm-request-router \
    --chart "$router_chart" \
    --skip-deps \
    "${core_state_values[@]}" \
    --state-values-set addons.llm.enabled=true \
    --state-values-set addons.llm.pki.enabled=true \
    "${router_dns_names[@]}" \
    "$@" \
    write-values \
    --output-file-template "$values_file" >/dev/null

  helm template llm-request-router "$router_chart" \
    --namespace nvcf \
    --values "$values_file" \
    >"$manifests_file"
}

expect_external_router() {
  local case_name="$1"
  local issuer_kind="$2"
  local issuer_name="$3"
  local manage_mode="${4:-explicit}"
  local manifests_file="$work_dir/$case_name.router-manifests.yaml"
  local certificate_file="$work_dir/$case_name.certificate.yaml"
  local router_overrides=(
    --state-values-set openbao.enabled=false
    --state-values-set-string "addons.llm.pki.issuerKind=$issuer_kind"
    --state-values-set-string "addons.llm.pki.issuerName=$issuer_name"
  )

  if test "$manage_mode" = "explicit"; then
    router_overrides+=(
      --state-values-set addons.llm.pki.clusterIssuer.enabled=false
    )
  fi

  render_router "$case_name" \
    "${router_overrides[@]}"

  sed -n '/^kind: Certificate$/,/^---$/p' \
    "$manifests_file" \
    >"$certificate_file"
  if ! grep -Fq "kind: \"$issuer_kind\"" "$certificate_file" &&
    ! grep -Fq "kind: $issuer_kind" "$certificate_file"; then
    fail "$case_name did not render Certificate issuer kind $issuer_kind"
  fi
  if ! grep -Fq "name: \"$issuer_name\"" "$certificate_file" &&
    ! grep -Fq "name: $issuer_name" "$certificate_file"; then
    fail "$case_name did not render Certificate issuer name $issuer_name"
  fi
  grep -Fq -- '--tls-cert-path=/etc/stargate/tls/tls.crt' "$manifests_file" ||
    fail "$case_name did not enable the request-router TLS certificate"
  grep -Fq -- '--tls-key-path=/etc/stargate/tls/tls.key' "$manifests_file" ||
    fail "$case_name did not enable the request-router TLS key"
  if grep -Fq -- '--quic-insecure' "$manifests_file"; then
    fail "$case_name enabled insecure request-router transport"
  fi
  if grep -Fq 'name: addons-llm-migrations' "$manifests_file"; then
    fail "$case_name rendered the managed OpenBao provisioning hook"
  fi
}

# existingSecret mode cannot reuse render_router: that helper always passes
# dnsNames, which this mode rejects as a mixed-ownership conflict.
render_existing_secret_router() {
  local case_name="$1"
  shift
  local values_file="$work_dir/$case_name.router-values.yaml"
  local router_chart="$stack_dir/../../helm/llm-request-router/llm-request-router"

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=llm-request-router \
    --chart "$router_chart" \
    --skip-deps \
    "${core_state_values[@]}" \
    --state-values-set addons.llm.enabled=true \
    --state-values-set addons.llm.pki.enabled=true \
    --state-values-set-string addons.llm.pki.mode=existingSecret \
    "$@" \
    write-values \
    --output-file-template "$values_file" \
    >"$work_dir/$case_name.router.log" 2>&1
}

# The conflict guards live in global.yaml.gotmpl, which only 02-core loads, so
# these cases cannot go through expect_failure.
expect_router_failure() {
  local case_name="$1"
  local expected_error="$2"
  shift 2

  if render_existing_secret_router "$case_name" "$@"; then
    fail "$case_name rendered successfully"
  fi
  grep -Fq "$expected_error" "$work_dir/$case_name.router.log" ||
    fail "$case_name did not return the expected error: $expected_error"
}

# The operator owns issuance, so the router mounts their Secret and the stack
# renders neither a Certificate nor the OpenBao provisioning hook.
expect_existing_secret_router() {
  local case_name="$1"
  local secret_name="$2"
  local values_file="$work_dir/$case_name.router-values.yaml"
  local manifests_file="$work_dir/$case_name.router-manifests.yaml"
  local router_chart="$stack_dir/../../helm/llm-request-router/llm-request-router"

  helm template llm-request-router "$router_chart" \
    --namespace nvcf \
    --values "$values_file" \
    >"$manifests_file"

  local rendered_cert
  rendered_cert="$(yq -rN 'select(.kind == "Certificate") | .metadata.name' "$manifests_file" | head -1)"
  test -z "$rendered_cert" ||
    fail "$case_name rendered a Certificate in existingSecret mode: $rendered_cert"

  if grep -Fq 'name: addons-llm-migrations' "$manifests_file"; then
    fail "$case_name rendered the managed OpenBao provisioning hook"
  fi

  local mounted_secret
  mounted_secret="$(yq -rN 'select(.kind == "StatefulSet") | .spec.template.spec.volumes[] | select(.name == "stargate-tls") | .secret.secretName' "$manifests_file" | head -1)"
  test "$mounted_secret" = "$secret_name" ||
    fail "$case_name mounted secret $mounted_secret, expected $secret_name"

  grep -Fq -- '--tls-cert-path=/etc/stargate/tls/tls.crt' "$manifests_file" ||
    fail "$case_name did not pass the request-router certificate path"
  grep -Fq -- '--tls-key-path=/etc/stargate/tls/tls.key' "$manifests_file" ||
    fail "$case_name did not pass the request-router private key path"
  if grep -Fq -- '--quic-insecure' "$manifests_file"; then
    fail "$case_name enabled insecure request-router transport"
  fi
}

expect_managed_router() {
  local case_name="${1:-managed-defaults}"
  local issuer_name="${2:-nvcf-openbao-pki}"
  local manage_mode="${3:-default}"
  local manifests_file="$work_dir/$case_name.router-manifests.yaml"
  local certificate_file="$work_dir/$case_name.certificate.yaml"
  local router_overrides=(
    --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local
    --state-values-set-string addons.llm.pki.image.tag=test
  )

  if test "$manage_mode" = "explicit"; then
    router_overrides+=(
      --state-values-set addons.llm.pki.clusterIssuer.enabled=true
      --state-values-set-string "addons.llm.pki.issuerName=$issuer_name"
    )
  fi

  render_router "$case_name" \
    "${router_overrides[@]}"

  sed -n '/^kind: Certificate$/,/^---$/p' \
    "$manifests_file" \
    >"$certificate_file"
  grep -Fq 'kind: "ClusterIssuer"' "$certificate_file" ||
    fail "$case_name did not render the managed ClusterIssuer kind"
  grep -Fq "name: \"$issuer_name\"" "$certificate_file" ||
    fail "$case_name did not render ClusterIssuer name $issuer_name"
  grep -Fq 'name: addons-llm-migrations' "$manifests_file" ||
    fail "$case_name did not render the managed OpenBao provisioning hook"
  grep -Fq -- '--tls-cert-path=/etc/stargate/tls/tls.crt' "$manifests_file" ||
    fail "$case_name did not enable the request-router TLS certificate"
  if grep -Fq -- '--quic-insecure' "$manifests_file"; then
    fail "$case_name enabled insecure request-router transport"
  fi
}

# Case 1: LLM disabled.
render_list llm-disabled \
  --state-values-set addons.llm.pki.enabled=true \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true
expect_enabled llm-disabled false

# Case 2: LLM enabled, PKI disabled.
render_list pki-disabled \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true
expect_enabled pki-disabled false

# Case 3: LLM and PKI enabled with the default managed ClusterIssuer.
managed_defaults=(
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
)
render_list managed-defaults "${managed_defaults[@]}"
expect_enabled managed-defaults true
jq -e '
  any(.[];
    .name == "nvcf-pki" and
    .namespace == "cert-manager" and
    .chart == "nvcf/helm-nvcf-pki" and
    .version == "0.1.0" and
    .enabled == true and
    .installed == true
  )
' "$work_dir/managed-defaults.json" >/dev/null ||
  fail "managed defaults did not render the published nvcf-pki release contract"

render_debug managed-defaults "${managed_defaults[@]}"
sed -n '/- name: nvcf-pki/,/- name: cassandra/p' \
  "$work_dir/managed-defaults.debug" \
  >"$work_dir/managed-defaults.release"

managed_release="$work_dir/managed-defaults.release"
for expected in \
  'enabled: true' \
  'name: "nvcf-openbao-pki"' \
  'server: "http://openbao-server.vault-system.svc.cluster.local:8200"' \
  'path: "services/all/pki/nvcf-service-issuing/sign/nvcf-service-server"' \
  'mountPath: "/v1/auth/jwt"' \
  'role: "cert-manager"' \
  'name: "cert-manager"' \
  'audience: "http://openbao-server.vault-system.svc.cluster.local:8200"' \
  '- vault-system/openbao-server' \
  '- cert-manager/cert-manager'; do
  grep -Fq -- "$expected" "$managed_release" ||
    fail "managed defaults did not render: $expected"
done
if grep -Fq -- '- nats-system/nats' "$managed_release"; then
  fail "nvcf-pki rendered a redundant direct NATS dependency"
fi
expect_managed_router

# Case 4: A managed issuer requires stack-managed OpenBao.
expect_failure managed-without-openbao \
  'openbao.enabled must be true when addons.llm.pki.clusterIssuer management is enabled' \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false

# Case 5: Explicit external ownership overrides default managed-issuer detection.
render_list external-clusterissuer \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=false
expect_enabled external-clusterissuer false
expect_external_router \
  external-clusterissuer \
  ClusterIssuer \
  nvcf-openbao-pki

# Case 6: An external namespaced Issuer remains external and does not require OpenBao.
render_list external-issuer \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false \
  --state-values-set-string addons.llm.pki.issuerKind=Issuer \
  --state-values-set-string addons.llm.pki.issuerName=external-pki.example.invalid
expect_enabled external-issuer false
expect_external_router \
  external-issuer \
  Issuer \
  external-pki.example.invalid \
  default

# Case 7: A managed issuer with external cert-manager keeps only the OpenBao dependency.
external_cert_manager=(
  "${managed_defaults[@]}"
  --state-values-set certManager.enabled=false
)
render_list external-cert-manager "${external_cert_manager[@]}"
expect_enabled external-cert-manager true
render_debug external-cert-manager "${external_cert_manager[@]}"
sed -n '/- name: nvcf-pki/,/- name: cassandra/p' \
  "$work_dir/external-cert-manager.debug" \
  >"$work_dir/external-cert-manager.release"
grep -Fq -- '- vault-system/openbao-server' "$work_dir/external-cert-manager.release" ||
  fail "external cert-manager mode lost the OpenBao dependency"
if grep -Fq -- '- cert-manager/cert-manager' "$work_dir/external-cert-manager.release"; then
  fail "external cert-manager mode rendered a dangling cert-manager dependency"
fi

# Case 8: A custom managed ClusterIssuer requires explicit management.
render_list custom-unmanaged \
  "${managed_defaults[@]}" \
  --state-values-set-string addons.llm.pki.issuerName=custom-managed-pki
expect_enabled custom-unmanaged false
expect_external_router \
  custom-unmanaged \
  ClusterIssuer \
  custom-managed-pki \
  default

custom_managed=(
  "${managed_defaults[@]}"
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true
  --state-values-set-string addons.llm.pki.issuerName=custom-managed-pki
)
render_list custom-managed "${custom_managed[@]}"
expect_enabled custom-managed true
render_debug custom-managed "${custom_managed[@]}"
sed -n '/- name: nvcf-pki/,/- name: cassandra/p' \
  "$work_dir/custom-managed.debug" \
  >"$work_dir/custom-managed.release"
grep -Fq 'name: "custom-managed-pki"' "$work_dir/custom-managed.release" ||
  fail "custom managed ClusterIssuer name was not passed to nvcf-pki"
expect_managed_router custom-managed custom-managed-pki explicit

# Case 9: The cluster-scoped chart cannot manage a namespaced Issuer.
expect_failure managed-namespaced-issuer \
  'addons.llm.pki.clusterIssuer management supports only issuerKind=ClusterIssuer' \
  "${managed_defaults[@]}" \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true \
  --state-values-set-string addons.llm.pki.issuerKind=Issuer \
  --state-values-set-string addons.llm.pki.issuerName=custom-managed-pki

# Cases 10 to 19: malformed issuer-management values must fail rendering.
#
# Each of these previously resolved to "not managed" without an error, which
# skipped the nvcf-pki release while the request-router Certificate still
# named ClusterIssuer/nvcf-openbao-pki. Asserting only that nvcf-pki is absent
# would pass against that defect, so every case asserts the diagnostic.
bool_error='addons.llm.pki.clusterIssuer.enabled must be a YAML boolean'
kind_error='addons.llm.pki.issuerKind must be exactly'
kind_type_error='addons.llm.pki.issuerKind must be the string'
name_error='addons.llm.pki.issuerName must be a lowercase RFC 1123 DNS subdomain'
name_type_error='addons.llm.pki.issuerName must be a string'

expect_failure cluster-issuer-null "$bool_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override cluster-issuer-null <<'YAML'
clusterIssuer:
  enabled: null
YAML
)"

expect_failure cluster-issuer-empty-string "$bool_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override cluster-issuer-empty-string <<'YAML'
clusterIssuer:
  enabled: ""
YAML
)"

expect_failure cluster-issuer-string-true "$bool_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override cluster-issuer-string-true <<'YAML'
clusterIssuer:
  enabled: "true"
YAML
)"

expect_failure cluster-issuer-string-upper-true "$bool_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override cluster-issuer-string-upper-true <<'YAML'
clusterIssuer:
  enabled: "TRUE"
YAML
)"

expect_failure issuer-kind-whitespace "$kind_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-kind-whitespace <<'YAML'
issuerKind: "   "
YAML
)"

expect_failure issuer-kind-wrong-case "$kind_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-kind-wrong-case <<'YAML'
issuerKind: clusterissuer
YAML
)"

expect_failure issuer-kind-null "$kind_type_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-kind-null <<'YAML'
issuerKind: null
YAML
)"

expect_failure issuer-name-whitespace "$name_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-name-whitespace <<'YAML'
issuerName: "   "
YAML
)"

expect_failure issuer-name-uppercase "$name_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-name-uppercase <<'YAML'
issuerName: Custom-PKI
YAML
)"

expect_failure issuer-name-null "$name_type_error" \
  "${managed_defaults[@]}" \
  --state-values-file "$(pki_override issuer-name-null <<'YAML'
issuerName: null
YAML
)"

# Cases 20 to 24: the valid forms must keep working, and none of them may
# leave a Certificate pointing at an issuer the stack was expected to install.
explicit_true_file="$(pki_override cluster-issuer-bool-true <<'YAML'
clusterIssuer:
  enabled: true
YAML
)"
render_list cluster-issuer-bool-true \
  "${managed_defaults[@]}" --state-values-file "$explicit_true_file"
expect_enabled cluster-issuer-bool-true true
render_router cluster-issuer-bool-true \
  --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local \
  --state-values-set-string addons.llm.pki.image.tag=test \
  --state-values-file "$explicit_true_file"
expect_no_dangling_issuer cluster-issuer-bool-true false

explicit_false_file="$(pki_override cluster-issuer-bool-false <<'YAML'
clusterIssuer:
  enabled: false
YAML
)"
render_list cluster-issuer-bool-false \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false \
  --state-values-file "$explicit_false_file"
expect_enabled cluster-issuer-bool-false false
render_router cluster-issuer-bool-false \
  --state-values-set openbao.enabled=false \
  --state-values-file "$explicit_false_file"
expect_no_dangling_issuer cluster-issuer-bool-false true

render_list omitted-cluster-issuer "${managed_defaults[@]}"
expect_enabled omitted-cluster-issuer true
render_router omitted-cluster-issuer \
  --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local \
  --state-values-set-string addons.llm.pki.image.tag=test
expect_no_dangling_issuer omitted-cluster-issuer false

# A valid custom external issuer must remain supported end to end.
external_valid_file="$(pki_override external-issuer-valid <<'YAML'
issuerKind: Issuer
issuerName: external-pki.example.invalid
clusterIssuer:
  enabled: false
YAML
)"
render_list external-issuer-valid \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false \
  --state-values-file "$external_valid_file"
expect_enabled external-issuer-valid false
render_router external-issuer-valid \
  --state-values-set openbao.enabled=false \
  --state-values-file "$external_valid_file"
expect_no_dangling_issuer external-issuer-valid true

# Managed mode with external cert-manager must still install the issuer.
render_list managed-external-cert-manager \
  "${managed_defaults[@]}" \
  --state-values-set certManager.enabled=false \
  --state-values-file "$explicit_true_file"
expect_enabled managed-external-cert-manager true
render_router managed-external-cert-manager \
  --state-values-set certManager.enabled=false \
  --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local \
  --state-values-set-string addons.llm.pki.image.tag=test \
  --state-values-file "$explicit_true_file"
expect_no_dangling_issuer managed-external-cert-manager false

# Managed mode still requires stack-managed OpenBao under an explicit boolean.
expect_failure managed-bool-without-openbao \
  'openbao.enabled must be true when addons.llm.pki.clusterIssuer management is enabled' \
  "${managed_defaults[@]}" \
  --state-values-set openbao.enabled=false \
  --state-values-file "$explicit_true_file"

# Cases 25 and 26: the ownership decision must hold across every state in
# helmfile.d, not only in the state that gates the release. A second
# declaration elsewhere installs a ClusterIssuer under the operator's own
# external issuer name, pointed at an OpenBao the operator did not deploy. The
# nvcf-pki chart keeps that object on uninstall and rollback, so the router
# Certificate never issues until it is deleted by hand.
render_list_all managed-defaults-all \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.enabled=true \
  --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local \
  --state-values-set-string addons.llm.pki.image.tag=test \
  "${router_dns_names[@]}"
expect_declared_all managed-defaults-all 1

render_list_all external-issuer-all \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.enabled=true \
  --state-values-set-string addons.llm.pki.issuerName=external-llm-pki \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=false \
  --state-values-set openbao.enabled=false \
  "${router_dns_names[@]}"
expect_declared_all external-issuer-all 0

# Cases 27 to 32: existingSecret mode. The operator owns issuance, renewal,
# rotation, and recovery, so the stack must add no issuer or cert-manager
# ownership and must not require OpenBao.
existing_secret_overrides=(
  --state-values-set openbao.enabled=false
  --state-values-set-string addons.llm.pki.mode=existingSecret
  --state-values-set-string addons.llm.pki.secretName=operator-quic-tls
)

render_list existing-secret \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.enabled=true \
  "${existing_secret_overrides[@]}"
expect_enabled existing-secret false

render_existing_secret_router existing-secret \
  --state-values-set openbao.enabled=false \
  --state-values-set-string addons.llm.pki.secretName=operator-quic-tls ||
  fail "existing-secret router render failed"
expect_existing_secret_router existing-secret operator-quic-tls
render_list_all existing-secret-all \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.enabled=true \
  "${existing_secret_overrides[@]}"
expect_declared_all existing-secret-all 0

# Values that only steer stack-managed issuance are conflicts, not no-ops.
expect_router_failure existing-secret-managed-issuer \
  'addons.llm.pki.clusterIssuer.enabled must be false or unset when addons.llm.pki.mode is existingSecret' \
  --state-values-set-string addons.llm.pki.secretName=operator-quic-tls \
  --state-values-set addons.llm.pki.clusterIssuer.enabled=true

expect_router_failure existing-secret-dns-names \
  'addons.llm.pki.dnsNames applies only to a stack-issued Certificate and must be empty when addons.llm.pki.mode is existingSecret' \
  --state-values-set-string addons.llm.pki.secretName=operator-quic-tls \
  --state-values-set-string 'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local'

expect_router_failure existing-secret-allowed-domains \
  'addons.llm.pki.allowedDomains constrains the managed OpenBao signing role only and must be empty when addons.llm.pki.mode is existingSecret' \
  --state-values-set-string addons.llm.pki.secretName=operator-quic-tls \
  --state-values-set-string addons.llm.pki.allowedDomains=nvcf.svc.cluster.local

# Without a Secret name the chart would silently leave the router on plaintext
# QUIC, so the stack must fail at render instead.
expect_router_failure existing-secret-missing-name \
  'addons.llm.pki.secretName is required when addons.llm.pki.mode is existingSecret'

# An unknown mode must fail in the dependency state too, so a typo cannot skip
# the issuer release while the core state still issues a Certificate.
expect_failure existing-secret-unknown-mode \
  'addons.llm.pki.mode must be exactly "certManager" or "existingSecret", got "existingsecret"' \
  --state-values-set addons.llm.enabled=true \
  --state-values-set addons.llm.pki.enabled=true \
  --state-values-set-string addons.llm.pki.mode=existingsecret

echo "check-llm-pki-issuer: all checks passed"
