#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

extract_auto_unseal_script() {
  local values_file="$1"
  local destination="$2"

  awk '
    /^            echo "Starting auto-unseal monitor\.\.\."$/ { capture = 1 }
    capture {
      line = $0
      sub(/^            /, "", line)
      print line
    }
    capture && /^            done$/ { exit }
  ' "${values_file}" >"${destination}"

  grep -Fq 'echo "Starting auto-unseal monitor..."' "${destination}"
  grep -Fq "if bao operator unseal \"\$UNSEAL_KEY\"; then" "${destination}"
}

run_retry_case() {
  local values_file="$1"
  local case_name="$2"
  local case_dir="${work_dir}/${case_name}"
  local script_file="${case_dir}/auto-unseal.sh"
  local output_file="${case_dir}/output.log"
  local process_id

  mkdir -p "${case_dir}/bin" "${case_dir}/unseal"
  printf '%s\n' 'test-unseal-key' >"${case_dir}/unseal/unseal_key"

  extract_auto_unseal_script "${values_file}" "${script_file}"
  sed -i.bak "s#/vault/userconfig/unseal#${case_dir}/unseal#g" "${script_file}"

  cat >"${case_dir}/bin/bao" <<'EOF'
#!/bin/sh
count_file="${AUTO_UNSEAL_TEST_DIR}/bao-count"
count=0
if [ -f "${count_file}" ]; then
  count=$(cat "${count_file}")
fi
count=$((count + 1))
printf '%s\n' "${count}" >"${count_file}"
printf 'attempt:%s\n' "${count}"
if [ "${count}" -eq 1 ]; then
  printf '%s\n' 'simulated listener-not-ready failure' >&2
  exit 1
fi
printf '%s\n' 'simulated unseal success'
EOF

  cat >"${case_dir}/bin/sleep" <<'EOF'
#!/bin/sh
printf 'sleep:%s\n' "$1"
/bin/sleep 0.01
EOF
  chmod +x "${case_dir}/bin/bao" "${case_dir}/bin/sleep"

  AUTO_UNSEAL_TEST_DIR="${case_dir}" \
    HOSTNAME="openbao-test" \
    PATH="${case_dir}/bin:${PATH}" \
    /bin/sh "${script_file}" >"${output_file}" 2>&1 &
  process_id=$!

  for _ in $(seq 1 200); do
    if grep -Fq 'OpenBao unsealed' "${output_file}"; then
      break
    fi
    /bin/sleep 0.01
  done

  kill "${process_id}" 2>/dev/null || true
  wait "${process_id}" 2>/dev/null || true

  grep -Fq 'OpenBao unseal attempt failed; retrying in 10 seconds...' "${output_file}"
  grep -Fq 'OpenBao unsealed' "${output_file}"

  local first_attempt_line second_attempt_line failure_line success_line
  first_attempt_line=$(grep -n -m1 '^attempt:1$' "${output_file}" | cut -d: -f1)
  second_attempt_line=$(grep -n -m1 '^attempt:2$' "${output_file}" | cut -d: -f1)
  failure_line=$(grep -n -m1 '^OpenBao unseal attempt failed;' "${output_file}" | cut -d: -f1)
  success_line=$(grep -n -m1 '^OpenBao unsealed$' "${output_file}" | cut -d: -f1)

  test "${first_attempt_line}" -lt "${failure_line}"
  test "${failure_line}" -lt "${second_attempt_line}"
  test "${second_attempt_line}" -lt "${success_line}"

  test "$(grep '^sleep:' "${output_file}" | sed -n '1p')" = 'sleep:10'
  test "$(grep '^sleep:' "${output_file}" | sed -n '2p')" = 'sleep:60'
}

run_retry_case \
  "${repo_root}/deploy/helm/openbao/helm/values.yaml" \
  chart-values
run_retry_case \
  "${repo_root}/deploy/helm/openbao/upgrade/values-upgrades.yaml" \
  upgrade-values

echo "OpenBao auto-unseal sidecar retry tests passed"
