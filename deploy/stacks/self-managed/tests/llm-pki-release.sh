#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
states_dir="$work_dir/states"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-pki-release: $*" >&2
  exit 1
}

mkdir -p "$states_dir"

# The whole helmfile.d directory, because `make` applies every state in one
# helmfile invocation. Checking a single state file cannot see a release that a
# neighboring state also declares.
render_debug() {
  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d" \
    --log-level debug \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --state-values-set addons.llm.enabled=true \
    --state-values-set addons.llm.pki.enabled=true \
    --state-values-set-string addons.llm.pki.allowedDomains=cluster.local \
    --state-values-set-string addons.llm.pki.image.tag=test \
    --state-values-set-string 'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
    list --skip-charts --output json 2>&1
}

# The managed issuer hook needs the same OpenBao migrations image as the
# control-plane chart. An environment should not have to duplicate its tag.
render_pki_image_case() {
  local case_name="$1"
  shift

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --output-file-template "$work_dir/$case_name-{{ .Release.Name }}.yaml" \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --state-values-set addons.llm.enabled=true \
    --state-values-set addons.llm.pki.enabled=true \
    --state-values-set-string addons.llm.pki.allowedDomains=cluster.local \
    --state-values-set-string 'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
    --state-values-set-string 'addons.llm.pki.dnsNames[1]=*.llm-request-router-headless.nvcf.svc.cluster.local' \
    "$@" \
    write-values --selector name=llm-request-router 2>&1
}

assert_pki_image_case() {
  local case_name="$1"
  local expected_tag="$2"
  local values_file="$work_dir/$case_name-llm-request-router.yaml"
  local manifests_file="$work_dir/$case_name-manifests.yaml"
  local router_chart="$stack_dir/../../helm/llm-request-router/llm-request-router"
  local expected_image="nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-openbao-migrations:$expected_tag"
  local actual_tag actual_image

  actual_tag="$(yq -r '.llmRequestRouter.pki.image.tag' "$values_file")"
  test "$actual_tag" = "$expected_tag" ||
    fail "$case_name resolved PKI image tag $actual_tag, expected $expected_tag"

  helm template llm-request-router "$router_chart" \
    --namespace nvcf \
    --values "$values_file" \
    >"$manifests_file"

  actual_image="$(
    yq ea -r '
      select(.kind == "Job" and .metadata.name == "addons-llm-migrations") |
      .spec.template.spec.containers[] |
      select(.name == "addons-llm-migrations") |
      .image
    ' "$manifests_file"
  )"
  test "$actual_image" = "$expected_image" ||
    fail "$case_name rendered migrations image $actual_image, expected $expected_image"
}

# The rendered state is the only place that carries both the release
# declarations and their needs edges. `list` reports enablement but drops
# needs, and `build` cannot run offline because it pulls every chart. Debug
# logging prints each rendered state as line-numbered YAML, one block per
# template part, including the states pulled in through nested helmfiles.
# Split the blocks back out per state so each one can be checked on its own.
split_states() {
  awk -v out="$states_dir" '
    match($0, /^rendering result of "[^"]+":$/) {
      state = $0
      sub(/^rendering result of "/, "", state)
      sub(/":$/, "", state)
      sub(/\.part\.[0-9]+$/, "", state)
      display = state
      sub(/^.*\//, "", display)
      gsub("/", "_", state)
      file = out "/" state ".yaml"
      print display >(file ".name")
      print "---" >file
      capture = 1
      next
    }
    capture && /^ *[0-9]+: / { sub(/^ *[0-9]+: ?/, ""); print >file; next }
    capture { capture = 0 }
  '
}

# One line per release: namespace, name, and its needs edges. A release with
# no explicit namespace is addressed by bare name in a needs entry, so key it
# the same way helmfile does.
state_table() {
  yq ea -N '
    .releases[]? |
    (.namespace // "") + "|" + .name + "|" + ((.needs // []) | join(","))
  ' "$1" |
    awk -F '|' '{ key = ($1 == "" ? $2 : $1 "/" $2); print key "\t" $3 }'
}

if ! render_pki_image_case default-image-tag \
  --state-values-set-string openbao.migrations.image.tag= \
  >"$work_dir/default-image-tag.log"; then
  cat "$work_dir/default-image-tag.log" >&2
  fail "managed LLM PKI must render when both PKI image tags are omitted"
fi
# Read the compatible-stack-default tag from global.yaml.gotmpl itself rather
# than hardcoding it, so this check does not need updating every time the
# floor is bumped.
default_pki_tag="$(
  (grep '\$pkiImageTag :=' "$stack_dir/global.yaml.gotmpl" || true) |
    sed 's/.*default "\([^"]*\)".*/\1/'
)"
test -n "$default_pki_tag" ||
  fail "could not read the default LLM PKI image tag from global.yaml.gotmpl"
assert_pki_image_case default-image-tag "$default_pki_tag"

render_pki_image_case legacy-image-tag \
  --state-values-set-string openbao.migrations.image.tag=legacy-tag \
  >"$work_dir/legacy-image-tag.log"
assert_pki_image_case legacy-image-tag legacy-tag

render_pki_image_case explicit-image-tag \
  --state-values-set-string openbao.migrations.image.tag=legacy-tag \
  --state-values-set-string addons.llm.pki.image.tag=explicit-tag \
  >"$work_dir/explicit-image-tag.log"
assert_pki_image_case explicit-image-tag explicit-tag

if ! render_debug >"$work_dir/debug.log"; then
  cat "$work_dir/debug.log" >&2
  fail "helmfile could not render helmfile.d"
fi
split_states <"$work_dir/debug.log"

state_files=("$states_dir"/*.yaml)
test -f "${state_files[0]}" ||
  fail "recovered no rendered Helmfile states from the debug render"

declarations=0
for state_file in "${state_files[@]}"; do
  state_name="$(head -1 "$state_file.name")"
  state_table "$state_file" >"$state_file.table"
  cut -f1 "$state_file.table" >"$state_file.releases"

  # Helmfile applies each state in turn, so a needs entry can only order
  # releases declared inside the same state. An edge that names a release from
  # another state file is dangling.
  while IFS=$'\t' read -r from needs; do
    test -n "$needs" || continue
    for target in ${needs//,/ }; do
      grep -Fxq "$target" "$state_file.releases" ||
        fail "$state_name declares the needs edge $from -> $target, but no release in $state_name provides $target"
    done
  done <"$state_file.table"

  matches="$(grep -Ec '(^|/)nvcf-pki$' "$state_file.releases" || true)"
  in_cert_manager="$(grep -Fxc 'cert-manager/nvcf-pki' "$state_file.releases" || true)"
  test "$matches" = "$in_cert_manager" ||
    fail "$state_name declares nvcf-pki outside the cert-manager namespace"
  declarations=$((declarations + in_cert_manager))
done

# The LLM PKI issuer is a single cluster-scoped ClusterIssuer. Two states that
# both declare it will disagree about when to install it, and the one with the
# weaker gate silently reinstalls what the other deliberately skipped.
test "$declarations" = "1" ||
  fail "expected exactly one nvcf-pki release in cert-manager across helmfile.d, found $declarations"

echo "llm-pki-release: all checks passed"
