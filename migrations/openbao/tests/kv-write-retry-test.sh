#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Integration test for the migration helper functions in
# migrations/utils/functions.sh against a real OpenBao server. Opens the
# kv-v2 storage-upgrade window (the 400 rejection) deterministically and
# asserts the helpers retry through it, preserve skip semantics, and fail
# fast on other errors.
#
# Requires Docker. The OpenBao version is read from the migrations image
# Dockerfile so the test tracks the shipped server version. Set UTILS_DIR
# to point the test at a different copy of the helper functions (useful to
# demonstrate the failure against an older revision).

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
utils_dir="${UTILS_DIR:-${script_dir}/../migrations/utils}"

bao_version="$(sed -n 's|^FROM openbao/openbao:||p' "${script_dir}/../Dockerfile" | head -1)"
if [ -z "${bao_version}" ]; then
  echo "could not read the OpenBao version from ${script_dir}/../Dockerfile" >&2
  exit 1
fi

container="nvcf-openbao-kv-test-$$"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/nvcf-openbao-kv-test.XXXXXX")"
cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  rm -rf "${tmpdir}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Raft storage rather than dev mode: production runs raft, and the upgrade
# window scales with storage latency, so raft keeps the window realistic.
cat > "${tmpdir}/config.hcl" <<'EOF'
ui = false
disable_mlock = true

listener "tcp" {
  address     = "127.0.0.1:8200"
  tls_disable = 1
}

storage "raft" {
  path    = "/openbao/data"
  node_id = "node1"
}

cluster_addr = "http://127.0.0.1:8201"
api_addr     = "http://127.0.0.1:8200"
EOF

echo "Starting OpenBao ${bao_version} (container ${container})..."
docker run -d --name "${container}" -u root --entrypoint /bin/sh \
  -v "${tmpdir}/config.hcl":/test/config.hcl:ro \
  -v "${script_dir}/inner-kv-write-retry.sh":/test/inner.sh:ro \
  -v "${utils_dir}":/test/utils:ro \
  "openbao/openbao:${bao_version}" \
  -c 'mkdir -p /openbao/data && exec bao server -config=/test/config.hcl' >/dev/null

export_addr=(-e BAO_ADDR=http://127.0.0.1:8200)

ready=0
for _ in $(seq 1 30); do
  rc=0
  docker exec "${export_addr[@]}" "${container}" bao status >/dev/null 2>&1 || rc=$?
  # 0 = up and unsealed, 2 = up and sealed; both mean the API is serving.
  if [ "${rc}" = "0" ] || [ "${rc}" = "2" ]; then
    ready=1
    break
  fi
  sleep 1
done
if [ "${ready}" != "1" ]; then
  docker logs "${container}" >&2
  echo "OpenBao did not become responsive" >&2
  exit 1
fi

docker exec "${export_addr[@]}" "${container}" \
  bao operator init -key-shares=1 -key-threshold=1 -format=json > "${tmpdir}/init.json"
unseal_key="$(python3 -c "import json,sys; print(json.load(sys.stdin)['unseal_keys_b64'][0])" < "${tmpdir}/init.json")"
root_token="$(python3 -c "import json,sys; print(json.load(sys.stdin)['root_token'])" < "${tmpdir}/init.json")"
docker exec "${export_addr[@]}" "${container}" bao operator unseal "${unseal_key}" >/dev/null

active=0
for _ in $(seq 1 30); do
  if docker exec "${container}" sh -c \
    'wget -qO- http://127.0.0.1:8200/v1/sys/health 2>/dev/null | grep -q "\"standby\":false"'; then
    active=1
    break
  fi
  sleep 1
done
if [ "${active}" != "1" ]; then
  docker logs "${container}" >&2
  echo "OpenBao did not become the active node" >&2
  exit 1
fi

# The stock image lacks bash, jq, and curl, which the helpers and the
# scenarios need. The migrations image installs the same set.
if ! docker exec -u root "${container}" apk add --no-cache bash jq curl >/dev/null; then
  echo "apk add failed in the test container" >&2
  exit 1
fi

docker exec "${export_addr[@]}" -e BAO_TOKEN="${root_token}" -e KEYS="${KEYS:-400}" \
  "${container}" bash /test/inner.sh
