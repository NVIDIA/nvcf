#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"
test_stack="${tmp_dir}/path with spaces/stack"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${test_stack}/environments"
cp "${stack_root}/Makefile.dist" "${test_stack}/Makefile"
if [[ -d "${stack_root}/scripts" ]]; then
  cp -R "${stack_root}/scripts" "${test_stack}/"
fi

cat > "${test_stack}/environments/base.yaml" <<'EOF'
global:
  controlPlane:
    id: plane-env
EOF
cat > "${test_stack}/environments/legacy.yaml" <<'EOF'
global:
  controlPlane:
    id: ""
EOF
cat > "${test_stack}/environments/reserved.yaml" <<'EOF'
global:
  controlPlane:
    id: default
EOF

cat >> "${test_stack}/Makefile" <<'EOF'

.PHONY: print-control-plane-id
print-control-plane-id: check-control-plane-id
	@printf '%s\n' "$${CONTROL_PLANE_ID:-}"
EOF

assert_equal() {
  local expected="$1"
  local actual="$2"
  local description="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    printf 'expected %s to be %q, got %q\n' "${description}" "${expected}" "${actual}" >&2
    exit 1
  fi
}

resolved_from_environment="$(make --no-print-directory -s -C "${test_stack}" print-control-plane-id)"
assert_equal plane-env "${resolved_from_environment}" "environment control-plane identity"

resolved_legacy="$(
  make --no-print-directory -s -C "${test_stack}" print-control-plane-id HELMFILE_ENV=legacy
)"
assert_equal "" "${resolved_legacy}" "empty legacy control-plane identity"

resolved_from_command_line="$({
  make --no-print-directory -s -C "${test_stack}" print-control-plane-id \
    CONTROL_PLANE_ID=plane-cli
})"
assert_equal plane-cli "${resolved_from_command_line}" "command-line control-plane identity"

twenty_character_id=12345678901234567890
resolved_twenty_character_id="$({
  make --no-print-directory -s -C "${test_stack}" print-control-plane-id \
    "CONTROL_PLANE_ID=${twenty_character_id}"
})"
assert_equal "${twenty_character_id}" "${resolved_twenty_character_id}" \
  "20-character control-plane identity"

if make --no-print-directory -s -C "${test_stack}" check-control-plane-id \
  CONTROL_PLANE_ID=123456789012345678901 > /dev/null 2>&1; then
  echo "expected a 21-character CONTROL_PLANE_ID to be rejected" >&2
  exit 1
fi

if make --no-print-directory -s -C "${test_stack}" check-control-plane-id \
  CONTROL_PLANE_ID=default > /dev/null 2>&1; then
  echo "expected reserved CONTROL_PLANE_ID=default to be rejected" >&2
  exit 1
fi

if make --no-print-directory -s -C "${test_stack}" check-control-plane-id \
  HELMFILE_ENV=reserved > /dev/null 2>&1; then
  echo "expected reserved environment controlPlane.id=default to be rejected" >&2
  exit 1
fi

if make --no-print-directory -s -C "${test_stack}" check-control-plane-id \
  "CONTROL_PLANE_ID=plane-a'quoted" > /dev/null 2>&1; then
  echo "expected quoted CONTROL_PLANE_ID to be rejected literally" >&2
  exit 1
fi

make_expression_marker="${tmp_dir}/make-expression-executed"
injected_control_plane_id="\$(shell touch ${make_expression_marker})"
if make --no-print-directory -s -C "${test_stack}" check-control-plane-id \
  "CONTROL_PLANE_ID=${injected_control_plane_id}" > /dev/null 2>&1; then
  echo "expected Make-expression CONTROL_PLANE_ID to be rejected" >&2
  exit 1
fi
if [[ -e "${make_expression_marker}" ]]; then
  echo "CONTROL_PLANE_ID expanded and executed a Make shell expression" >&2
  exit 1
fi

dev_make_expression_marker="${tmp_dir}/dev-make-expression-executed"
dev_injected_control_plane_id="\$(shell touch ${dev_make_expression_marker})"
if make --no-print-directory -s -C "${stack_root}" check-control-plane-id \
  "CONTROL_PLANE_ID=${dev_injected_control_plane_id}" > /dev/null 2>&1; then
  echo "expected the development Make entrypoint to reject a Make-expression CONTROL_PLANE_ID" >&2
  exit 1
fi
if [[ -e "${dev_make_expression_marker}" ]]; then
  echo "development Makefile expanded and executed a Make shell expression" >&2
  exit 1
fi

missing_helper_stack="${tmp_dir}/missing-helper-stack"
missing_helper_tmp="${tmp_dir}/missing-helper-tmp"
mkdir -p "${missing_helper_stack}/environments" "${missing_helper_tmp}"
cp "${stack_root}/Makefile.dist" "${missing_helper_stack}/Makefile"
cp "${stack_root}/environments/base.yaml" "${missing_helper_stack}/environments/"
TMPDIR="${missing_helper_tmp}" make --no-print-directory -s -C "${missing_helper_stack}" \
  check-control-plane-id CONTROL_PLANE_ID=plane-cli > /dev/null 2>&1 || true
if find "${missing_helper_tmp}" -type f -name 'nvcf-control-plane-id.*' -print -quit | grep -q .; then
  echo "Make left a raw CONTROL_PLANE_ID tempfile behind when the resolver was unavailable" >&2
  exit 1
fi

echo "validated literal control-plane identity capture and sanitized resolution"
