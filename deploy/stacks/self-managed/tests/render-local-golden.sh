#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

target_dir="${1:?target render directory is required}"
output_dir_template="${2:?helmfile output-dir-template is required}"

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/nvcf-self-managed-golden.XXXXXX")"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
chart_repo_dir="$work_dir/chart-repo"
chart_build_dir="$work_dir/chart-build"
render_dir="$work_dir/out"
environment_name="local"
chart_repo_pid=""

cleanup() {
  if test -n "$chart_repo_pid"; then
    kill "$chart_repo_pid" >/dev/null 2>&1 || true
  fi
  if test "${KEEP_GOLDEN_WORK_DIR:-}" = "1"; then
    echo "render-local-golden: kept work dir at $work_dir"
  else
    rm -rf "$work_dir"
  fi
}
trap cleanup EXIT

fail() {
  echo "render-local-golden: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

mkdir -p "$test_stacks_dir" "$chart_repo_dir" "$chart_build_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
cp "$stack_dir/testdata/environments/local.yaml" \
  "$test_stack_dir/environments/$environment_name.yaml"
cp "$test_stack_dir/secrets/secrets.yaml.template" \
  "$test_stack_dir/secrets/$environment_name-secrets.yaml"

for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

export HELM_REPOSITORY_CONFIG="$work_dir/helm/repositories.yaml"
export HELM_REPOSITORY_CACHE="$work_dir/helm/repository-cache"
export HELM_REGISTRY_CONFIG="$work_dir/helm/registry.json"
export HELM_CACHE_HOME="$work_dir/helm/cache"
export HELM_CONFIG_HOME="$work_dir/helm/config"
export HELM_DATA_HOME="$work_dir/helm/data"
mkdir -p "$(dirname "$HELM_REPOSITORY_CONFIG")" \
  "$HELM_REPOSITORY_CACHE" "$HELM_CACHE_HOME" "$HELM_CONFIG_HOME" "$HELM_DATA_HOME"
printf 'repositories: []\n' >"$HELM_REPOSITORY_CONFIG"

add_chart_dependency_repos() {
  local chart_dir="$1"
  local repo_name repo_url

  while IFS=$'\t' read -r repo_name repo_url; do
    test -n "$repo_name" || continue
    helm repo add "$repo_name" "$repo_url" >/dev/null
  done < <(
    yq -r '
      .dependencies[]? |
      select(.repository != null) |
      select((.repository | test("^file://") | not) and (.repository | test("^oci://") | not)) |
      [.name, .repository] | @tsv
    ' "$chart_dir/Chart.yaml"
  )
}

package_chart() {
  local chart_name="$1"
  local chart_version="$2"
  local chart_path="$3"
  local source_dir="$repo_dir/$chart_path"
  local chart_copy="$chart_build_dir/$chart_name"
  local actual_name dependency_count

  test -f "$source_dir/Chart.yaml" ||
    fail "chart source not found: $chart_path"

  actual_name="$(yq -r '.name' "$source_dir/Chart.yaml")"
  test "$actual_name" = "$chart_name" ||
    fail "$chart_path has chart name $actual_name, expected $chart_name"

  cp -R "$source_dir" "$chart_copy"
  dependency_count="$(yq -r '(.dependencies // []) | length' "$chart_copy/Chart.yaml")"
  if test "$dependency_count" != "0"; then
    add_chart_dependency_repos "$chart_copy"
    helm dependency build "$chart_copy" >/dev/null
  fi

  helm package "$chart_copy" --version "$chart_version" \
    --destination "$chart_repo_dir" >/dev/null
}

release_version() {
  local release_name="$1"
  local versions version_count

  versions="$(
    awk -v release="$release_name" '
      function trim(s) {
        sub(/^[[:space:]]+/, "", s)
        sub(/[[:space:]]+$/, "", s)
        return s
      }
      function strip_value(s) {
        sub(/[[:space:]]*#.*/, "", s)
        s = trim(s)
        gsub(/^"/, "", s)
        gsub(/"$/, "", s)
        return s
      }
      /^[[:space:]]*-[[:space:]]name:[[:space:]]*/ {
        in_release = 0
        line = $0
        sub(/^[[:space:]]*-[[:space:]]name:[[:space:]]*/, "", line)
        line = strip_value(line)
        if (line == release) {
          in_release = 1
        }
        next
      }
      in_release && /^[[:space:]]*version:[[:space:]]*/ {
        line = $0
        sub(/^[[:space:]]*version:[[:space:]]*/, "", line)
        print strip_value(line)
        in_release = 0
      }
    ' "$stack_dir"/helmfile.d/*.yaml.gotmpl
  )"

  test -n "$versions" ||
    fail "could not find Helmfile version for release $release_name"

  version_count="$(printf '%s\n' "$versions" | wc -l | tr -d ' ')"
  test "$version_count" = "1" ||
    fail "expected one Helmfile version for release $release_name, found $version_count: $versions"

  printf '%s' "$versions"
}

normalize_known_nondeterminism() {
  local shared_worker_secret auth_callout_secret

  shared_worker_secret="$render_dir/01-dependencies.yaml-nats/helm-nvcf-nats/templates/nkey-secret.yaml"
  auth_callout_secret="$render_dir/01-dependencies.yaml-nats/helm-nvcf-nats/templates/nats-auth-callout-nkeys-secret.yaml"

  test -f "$shared_worker_secret" ||
    fail "expected generated NATS shared-worker Secret render not found"
  test -f "$auth_callout_secret" ||
    fail "expected generated NATS auth-callout Secret render not found"

  yq -i '
    .data."user.key" = "U1VBQk1QV0xVQktCTkdIT0NKNEtURlhXWUJNNkxOSEVIS0FJNEQySVRRVlNTNkY1R1RNWlJZNk1RTQ==" |
    .data."user.pub" = "VUJZUjU0QlczT0s1RlhOU0tCUDRWS1RHSElDTjQ2QUw1TUdQSEwyQlBUTENSRTZVSDVMSFBWSFE="
  ' "$shared_worker_secret"

  yq -i '
    .data.user_pub = "VUI3SU5SU0dJTkhaSlhCRUU1SjdZRTNYNk5ZSE8zWk1PWlhKSUZFUTRZQUpOVktKNExWUlZFM0c=" |
    .data.nkey_seed = "U1VBTkJFNUtWVDVBQUVUSExYM0RaRkVJTkpJT1lXQUZYSzRFMlFHUkRWRFdQUlJMTkVHSlhESlRJUQ==" |
    .data.account_pub = "QURINURNSlBYNlNUWFpUTzNTTDdXRlBaUjZKQ0ZWRzdWR0Q2QTJVRFNVTFA1NUtNSVU1WEQ2Q0M=" |
    .data.nkey_signature = "U0FBQ1haTTRYQzZEQTRUSEFKTTJERzdKTkVKU0lZN1BNTlgzU1EyTEFOQk1RTlAzVjJETlgyQU9BTQ=="
  ' "$auth_callout_secret"
}

chart_sources=(
  "nats|helm-nvcf-nats|deploy/helm/nats"
  "cert-manager|helm-nvcf-cert-manager|deploy/helm/cert-manager"
  "openbao-server|helm-nvcf-openbao-server|deploy/helm/openbao/helm"
  "nvcf-pki|helm-nvcf-pki|deploy/helm/nvcf-pki"
  "cassandra|helm-nvcf-cassandra|deploy/helm/cassandra/helm"
  "api-keys|helm-nvcf-api-keys|deploy/helm/api-keys-colocated/api-keys"
  "sis|helm-nvcf-sis|deploy/helm/icms/icms-api"
  "api|helm-nvcf-api|deploy/helm/cloud-functions/nvcf-api"
  "nvct-api|helm-nvcf-nvct-api|deploy/helm/cloud-tasks/nvct-api"
  "invocation-service|helm-nvcf-invocation-service|deploy/helm/http-invocation/nvcf-invocation-service"
  "grpc-proxy|helm-nvcf-grpc-proxy|deploy/helm/grpc-proxy/grpc-proxy"
  "ratelimiter|helm-nvcf-rate-limiter|deploy/helm/ratelimiter/nvcf-ratelimiter"
  "ess-api|helm-nvcf-ess-api|deploy/helm/encrypted-secret-store/ess-api"
  "notary-service|helm-nvcf-notary-service|deploy/helm/notary/nvcf-notary-service"
  "admin-issuer-proxy|helm-admin-token-issuer-proxy|deploy/helm/admin-token-issuer-proxy/chart"
  "reval|helm-reval|deploy/helm/helm-reval"
  "nats-auth-callout-service|helm-nvcf-nats-auth-callout-service|deploy/helm/nats-auth-callout"
  "llm-request-router|helm-nvcf-llm-request-router|deploy/helm/llm-request-router/llm-request-router"
  "llm-api-gateway|helm-nvcf-llm-api-gateway|deploy/helm/llm-api-gateway/llm-api-gateway"
  "vanity-gateway|helm-nvcf-vanity-gateway|deploy/helm/vanity-gateway/helm-nvcf-vanity-gateway"
  "nvcf-ui|helm-nvcf-ui|src/uis/nvcf-ui/helm"
  "ingress|nvcf-gateway-routes|deploy/helm/gateway-routes/chart"
  "function-autoscaler|helm-nvcf-function-autoscaler|deploy/helm/function-autoscaler"
)

for package_spec in "${chart_sources[@]}"; do
  IFS='|' read -r release_name chart_name chart_path <<<"$package_spec"
  chart_version="$(release_version "$release_name")"
  package_chart "$chart_name" "$chart_version" "$chart_path"
done
helm repo index "$chart_repo_dir"

chart_repo_port="$(
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
)"
python3 -m http.server "$chart_repo_port" \
  --bind 127.0.0.1 \
  --directory "$chart_repo_dir" \
  >"$work_dir/chart-repo-server.log" 2>&1 &
chart_repo_pid="$!"
chart_repo_url="http://127.0.0.1:$chart_repo_port"

for _ in $(seq 1 50); do
  if python3 - "$chart_repo_url/index.yaml" <<'PY' >/dev/null 2>&1
import sys
import urllib.request

urllib.request.urlopen(sys.argv[1], timeout=1).read()
PY
  then
    break
  fi
  sleep 0.1
done
python3 - "$chart_repo_url/index.yaml" <<'PY' >/dev/null 2>&1 ||
import sys
import urllib.request

urllib.request.urlopen(sys.argv[1], timeout=1).read()
PY
  fail "temporary chart repository did not start"

chart_repo_url="$chart_repo_url" \
  yq -i '
    .global.helm.sources.url = strenv(chart_repo_url) |
    del(.global.helm.sources.registry) |
    del(.global.helm.sources.repository)
  ' "$test_stack_dir/environments/$environment_name.yaml"

rm -rf "$target_dir"
mkdir -p "$target_dir"

(
  cd "$test_stack_dir"
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    make template DEV_MODE=1 OUTPUT_DIR="$render_dir" \
      OUTPUT_DIR_TEMPLATE="$output_dir_template"
)

normalize_known_nondeterminism
cp -R "$render_dir"/. "$target_dir"
echo "render-local-golden: rendered manifests into $target_dir"
