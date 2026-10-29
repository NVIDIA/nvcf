#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# The service-pki addon runs from two owners: the self-managed stack and the
# llm-request-router hook. Each must write only its own signing role, and both
# must write the same cert-manager policy so neither run revokes the other's
# sign path.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
addon="$script_dir/../addons/service-pki/setup_service_pki.sh"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

mkdir -p "$work_dir/bin"
cat >"$work_dir/bin/bao" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$BAO_CALLS"
STUB
chmod +x "$work_dir/bin/bao"
export PATH="$work_dir/bin:$PATH"

run_addon() {
  local name="$1"
  shift
  export BAO_CALLS="$work_dir/calls-$name.log"
  : >"$BAO_CALLS"
  ( env "$@" bash -c "source '$addon'" ) >"$work_dir/out-$name.log" 2>&1 ||
    { echo "service-pki-worker-callback-role: the addon failed in case $name" >&2; cat "$work_dir/out-$name.log" >&2; exit 1; }
}

fail() {
  echo "service-pki-worker-callback-role: $*" >&2
  exit 1
}

role_path="services/all/pki/nvcf-service-issuing/roles"

# 1. LLM hook: writes nvcf-service-server only.
run_addon llm NVCF_SERVICE_PKI_ALLOWED_DOMAINS=example.com,cluster.local
grep -q "^write $role_path/nvcf-service-server allowed_domains=example.com,cluster.local " "$work_dir/calls-llm.log" ||
  fail "llm: the service role was not written"
if grep -q "$role_path/nvcf-worker-callback" "$work_dir/calls-llm.log"; then
  fail "llm: the worker callback role was written"
fi

# 2. Stack: also writes the worker callback role with its own domains.
run_addon stack NVCF_SERVICE_PKI_ALLOWED_DOMAINS=example.com,cluster.local \
  NVCF_WORKER_CALLBACK_PKI_ALLOWED_DOMAINS=grpc-headless.nvcf.svc.cluster.local
grep -q "^write $role_path/nvcf-worker-callback allowed_domains=grpc-headless.nvcf.svc.cluster.local allow_subdomains=true allow_bare_domains=false allow_wildcard_certificates=true allow_ip_sans=false .* server_flag=true client_flag=false " "$work_dir/calls-stack.log" ||
  fail "stack: the worker callback role was not written with the expected constraints"

# 3. The worker role name is fixed: an override would let the two owners write
#    different policies, and the last run would drop the other's sign path.
run_addon override NVCF_SERVICE_PKI_ALLOWED_DOMAINS=example.com,cluster.local \
  NVCF_WORKER_CALLBACK_PKI_ALLOWED_DOMAINS=grpc-headless.nvcf.svc.cluster.local \
  NVCF_WORKER_CALLBACK_PKI_ROLE_NAME=custom-worker-role
if grep -q 'custom-worker-role' "$work_dir/calls-override.log"; then
  fail "override: the worker callback role name was overridden"
fi

# 4. Every run grants cert-manager both sign paths.
for name in llm stack override; do
  for role in nvcf-service-server nvcf-worker-callback; do
    grep -q "path \"services/all/pki/nvcf-service-issuing/sign/$role\"" "$work_dir/calls-$name.log" ||
      fail "$name: the cert-manager policy does not grant sign/$role"
  done
done

echo "service-pki-worker-callback-role: all checks passed"
