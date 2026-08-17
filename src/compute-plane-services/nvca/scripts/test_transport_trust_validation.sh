#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

cat > "${test_dir}/invalid-direct.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: true
      transportTLS:
        trustMode: bundle
EOF

cat > "${test_dir}/invalid-secret.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: true
operatorConfig:
  workload:
    transportTLS:
      trustBundle:
        secretKeyRef:
          name: nvcf-trust
EOF

cat > "${test_dir}/valid-system.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: true
      transportTLS:
        trustMode: system
EOF

cat > "${test_dir}/valid-bundle.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: false
      transportTLS:
        trustMode: bundle
EOF

cat > "${test_dir}/valid-bundle-unset.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      transportTLS:
        trustMode: bundle
EOF

cat > "${test_dir}/valid-secret-bundle.yaml" <<'EOF'
agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: false
operatorConfig:
  workload:
    transportTLS:
      trustBundle:
        secretKeyRef:
          name: nvcf-trust
EOF

cat > "${test_dir}/valid-secret-bundle-unset.yaml" <<'EOF'
operatorConfig:
  workload:
    transportTLS:
      trustBundle:
        secretKeyRef:
          name: nvcf-trust
EOF

validation_error="workload.stargateQUICInsecure=true cannot be used with workload.transportTLS.trustMode=bundle"

assert_invalid() {
  local chart_dir="$1"
  local label="$2"
  local values_file="$3"
  local output_file="${test_dir}/output"

  if helm template test-release "${chart_dir}" --set ngcConfig.serviceKey=fakekey \
    --values "${values_file}" > "${output_file}" 2>&1; then
    echo "Expected ${label} to reject bundle transport trust with QUIC insecure" >&2
    return 1
  fi
  if ! grep -Fq "${validation_error}" "${output_file}"; then
    echo "Expected ${label} validation error to explain the incompatible settings" >&2
    cat "${output_file}" >&2
    return 1
  fi
  if ! grep -Fq "set workload.stargateQUICInsecure=false or use trustMode=system" "${output_file}"; then
    echo "Expected ${label} validation error to include remediation" >&2
    cat "${output_file}" >&2
    return 1
  fi
}

assert_valid() {
  local chart_dir="$1"
  local label="$2"
  local values_file="$3"

  if ! helm template test-release "${chart_dir}" --set ngcConfig.serviceKey=fakekey \
    --values "${values_file}" > /dev/null; then
    echo "Expected ${label} to render" >&2
    return 1
  fi
}

for chart_dir in \
  "${repo_root}/deployments/nvca-operator" \
  "${repo_root}/../../../deploy/helm/nvca-operator/nvca-operator"; do
  chart_label="$(basename "$(dirname "${chart_dir}")")/$(basename "${chart_dir}")"
  assert_invalid "${chart_dir}" "${chart_label} direct bundle input" "${test_dir}/invalid-direct.yaml"
  assert_invalid "${chart_dir}" "${chart_label} secret-backed bundle input" "${test_dir}/invalid-secret.yaml"
  assert_valid "${chart_dir}" "${chart_label} system mode with QUIC insecure" "${test_dir}/valid-system.yaml"
  assert_valid "${chart_dir}" "${chart_label} bundle mode with QUIC insecure false" "${test_dir}/valid-bundle.yaml"
  assert_valid "${chart_dir}" "${chart_label} bundle mode with QUIC insecure unset" "${test_dir}/valid-bundle-unset.yaml"
  assert_valid "${chart_dir}" "${chart_label} secret-backed bundle with QUIC insecure false" \
    "${test_dir}/valid-secret-bundle.yaml"
  assert_valid "${chart_dir}" "${chart_label} secret-backed bundle with QUIC insecure unset" \
    "${test_dir}/valid-secret-bundle-unset.yaml"
done
