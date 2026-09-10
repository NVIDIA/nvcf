#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

local_cluster_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "${local_cluster_dir}/../.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

mkdir -p "$test_dir/bin"
call_log="$test_dir/calls.log"
: >"$call_log"

cat >"$test_dir/bin/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'helm %s\n' "$*" >>"$TEST_CALL_LOG"
EOF

cat >"$test_dir/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl %s\n' "$*" >>"$TEST_CALL_LOG"

case "${1:-}:${2:-}" in
  cluster-info:)
    ;;
  get:crd)
    [[ "${3:-}" == "backendtrafficpolicies.gateway.envoyproxy.io" ]]
    if [[ "$*" == *'.name=="v1alpha1"'* ]]; then
      printf 'v1alpha1\t%s\t%s\n' \
        "${MOCK_V1ALPHA1_SERVED-}" "${MOCK_V1ALPHA1_REQUEST_TIMEOUT_TYPE-}"
    else
      if [[ "${MOCK_V1ALPHA1_SERVED-}" == true ]]; then
        printf '%s\n' "${MOCK_V1ALPHA1_REQUEST_TIMEOUT_TYPE-}"
      fi
      if [[ "${MOCK_V1BETA1_SERVED-}" == true ]]; then
        printf '%s\n' "${MOCK_V1BETA1_REQUEST_TIMEOUT_TYPE-}"
      fi
    fi
    ;;
  apply:-k)
    ;;
  get:gatewayclass)
    if [[ "$*" == *jsonpath* ]]; then
      printf 'True\n'
    fi
    ;;
  *)
    echo "unexpected kubectl command: $*" >&2
    exit 64
    ;;
esac
EOF

chmod +x "$test_dir/bin/helm" "$test_dir/bin/kubectl"
export PATH="$test_dir/bin:$PATH"
export TEST_CALL_LOG="$call_log"

setup_script="$local_cluster_dir/scripts/setup-gateway-api.sh"
docs_file="$repo_dir/docs/user/gateway-routing.md"
setup_version="$(sed -n 's/^ENVOY_GATEWAY_VERSION="\([^"]*\)"$/\1/p' "$setup_script")"
docs_version="$(
  awk '
    /helm upgrade --install eg oci:\/\/docker.io\/envoyproxy\/gateway-helm/ { in_install = 1 }
    in_install && /--version/ { print $2; exit }
  ' "$docs_file"
)"

[[ -n "$setup_version" ]] || fail "setup script does not pin an Envoy Gateway version"
[[ "$docs_version" == "$setup_version" ]] ||
  fail "gateway guide pins $docs_version, but local setup pins $setup_version"

minimum_version="v1.5.0"
lowest_version="$(printf '%s\n%s\n' "$minimum_version" "$setup_version" | sort -V | head -1)"
[[ "$lowest_version" == "$minimum_version" ]] ||
  fail "Envoy Gateway $setup_version predates requestTimeout support"

export MOCK_V1ALPHA1_SERVED=true
export MOCK_V1ALPHA1_REQUEST_TIMEOUT_TYPE=string
export MOCK_V1BETA1_SERVED=true
export MOCK_V1BETA1_REQUEST_TIMEOUT_TYPE=
"$setup_script" >"$test_dir/success.out"
grep -Fq "helm upgrade --install eg" "$call_log" ||
  fail "setup did not install Envoy Gateway"
grep -Fq -- "--version $setup_version" "$call_log" ||
  fail "setup did not use the pinned Envoy Gateway version"
grep -Fq "kubectl get crd backendtrafficpolicies.gateway.envoyproxy.io" "$call_log" ||
  fail "setup did not inspect the BackendTrafficPolicy CRD"
grep -Fq '.spec.versions[?(@.name=="v1alpha1")]' "$call_log" ||
  fail "setup did not inspect the v1alpha1 BackendTrafficPolicy schema"
grep -Fq "kubectl apply -k" "$call_log" ||
  fail "timeout-capable CRD did not proceed to Gateway creation"
grep -Fq "supports disabling the LLM worker request timeout" "$test_dir/success.out" ||
  fail "successful capability check was not reported"

: >"$call_log"
export MOCK_V1ALPHA1_REQUEST_TIMEOUT_TYPE=
export MOCK_V1BETA1_REQUEST_TIMEOUT_TYPE=string
if "$setup_script" >"$test_dir/failure.out" 2>&1; then
  fail "setup accepted v1alpha1 without requestTimeout because another served version provided it"
fi
grep -Fq "installed Envoy Gateway CRD does not support BackendTrafficPolicy spec.timeout.http.requestTimeout" "$test_dir/failure.out" ||
  fail "missing v1alpha1 requestTimeout did not return the expected error"
if grep -Fq "kubectl apply -k" "$call_log"; then
  fail "setup created Gateway resources after the capability check failed"
fi

echo "Envoy Gateway timeout compatibility tests passed."
