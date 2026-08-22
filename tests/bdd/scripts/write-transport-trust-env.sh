#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Reads the NVCF root CA public certificate from OpenBao and replaces
# the agentConfig block of a compute-plane Helmfile environment file
# with the transportTLS bundle configuration. The fingerprint follows
# the canonical nvcf-trust-bundle-v1 algorithm (same as nvcf-cli and
# NVCA, which recomputes it and fails closed on divergence).
#
# The CA is public material; the OpenBao root token is used only
# inside this script and never appears in the BDD command logs.
#
# Usage: write-transport-trust-env.sh <compute-env-file>

set -euo pipefail

ENV_FILE="${1:?usage: write-transport-trust-env.sh <compute-env-file>}"
OPENBAO_NAMESPACE="${OPENBAO_NAMESPACE:-vault-system}"
OPENBAO_SERVICE="${OPENBAO_SERVICE:-openbao-server}"
OPENBAO_SECRET_NAME="${OPENBAO_SECRET_NAME:-openbao-server-root-token}"
ROOT_PKI_PATH="${ROOT_PKI_PATH:-services/all/pki/root}"
LOCAL_PORT="${LOCAL_PORT:-18200}"

for tool in kubectl curl openssl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

if [[ ! -f "$ENV_FILE" ]]; then
  echo "compute environment file not found: $ENV_FILE" >&2
  exit 1
fi
if [[ "$(grep -c '^agentConfig:' "$ENV_FILE")" != "1" ]]; then
  echo "expected exactly one top-level agentConfig block in $ENV_FILE" >&2
  exit 1
fi

root_token="$(kubectl get secret "$OPENBAO_SECRET_NAME" -n "$OPENBAO_NAMESPACE" \
  -o jsonpath='{.data.root_token}' | base64 -d)"
if [[ -z "$root_token" ]]; then
  echo "empty OpenBao root token from secret $OPENBAO_NAMESPACE/$OPENBAO_SECRET_NAME" >&2
  exit 1
fi

# Port-forward instead of kubectl run: no pod churn and no attach race.
kubectl port-forward -n "$OPENBAO_NAMESPACE" "svc/$OPENBAO_SERVICE" \
  "$LOCAL_PORT:8200" >/dev/null 2>&1 &
pf_pid=$!
tmp=""
# The token travels via a mode-600 curl config, never in argv.
curl_config="$(mktemp "${TMPDIR:-/tmp}/openbao-curl.XXXXXX")"
chmod 600 "$curl_config"
printf 'header = "X-Vault-Token: %s"\n' "$root_token" >"$curl_config"
trap 'kill "$pf_pid" 2>/dev/null || true; rm -f "$curl_config"; [[ -n "$tmp" ]] && rm -f "$tmp"' EXIT

ca_pem=""
for _ in $(seq 1 20); do
  if ca_pem="$(curl -sSf --config "$curl_config" \
      "http://127.0.0.1:$LOCAL_PORT/v1/$ROOT_PKI_PATH/cert/ca" 2>/dev/null \
      | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["certificate"])')"; then
    break
  fi
  ca_pem=""
  sleep 1
done
if [[ -z "$ca_pem" ]] || ! grep -q "BEGIN CERTIFICATE" <<<"$ca_pem"; then
  echo "failed to read root CA PEM from OpenBao at $ROOT_PKI_PATH/cert/ca" >&2
  exit 1
fi

# Canonical fingerprint: sorted deduplicated lowercase-hex
# sha256(DER) per cert, under a version header, then sha256.
cert_hashes="$(python3 - "$ca_pem" <<'PYEOF'
import subprocess, sys

pem = sys.argv[1]
blocks, current, inside = [], [], False
for line in pem.splitlines():
    if "-----BEGIN CERTIFICATE-----" in line:
        inside, current = True, [line]
    elif "-----END CERTIFICATE-----" in line and inside:
        current.append(line)
        blocks.append("\n".join(current) + "\n")
        inside = False
    elif inside:
        current.append(line)

hashes = []
for block in blocks:
    der = subprocess.run(
        ["openssl", "x509", "-outform", "DER"],
        input=block.encode(), capture_output=True, check=True).stdout
    digest = subprocess.run(
        ["openssl", "dgst", "-sha256", "-r"],
        input=der, capture_output=True, check=True).stdout.split()[0].decode()
    if digest not in hashes:
        hashes.append(digest)

for h in sorted(hashes):
    print(h)
PYEOF
)"
if [[ -z "$cert_hashes" ]]; then
  echo "no certificate hashes computed from the root CA PEM" >&2
  exit 1
fi
fingerprint="sha256:$(printf 'nvcf-trust-bundle-v1\n%s\n' "$cert_hashes" \
  | openssl dgst -sha256 -r | cut -d' ' -f1)"

# Replace the agentConfig block wherever it sits. The suite's yaml
# editing step re-marshals the file with sorted top-level keys, so the
# block's position is not stable: drop lines from ^agentConfig: until
# the next top-level key, keep everything else, and append the new
# block at the end.
tmp="$(mktemp "${TMPDIR:-/tmp}/compute-env.XXXXXX")"
awk '
  /^agentConfig:/ { skipping = 1; next }
  skipping && /^[^ \t#]/ { skipping = 0 }
  !skipping { print }
' "$ENV_FILE" >"$tmp"
if grep -q '^agentConfig:' "$tmp"; then
  echo "failed to strip the agentConfig block from $ENV_FILE" >&2
  exit 1
fi
if ! grep -q '^global:' "$tmp"; then
  echo "agentConfig strip removed unrelated top-level keys from $ENV_FILE" >&2
  exit 1
fi
{
  echo "agentConfig:"
  echo "  mergeConfig: |"
  echo "    cluster:"
  echo "      validationPolicy:"
  echo "        name: Unrestricted"
  echo "    workload:"
  echo "      transportTLS:"
  echo "        trustMode: bundle"
  echo "        trustBundleFingerprint: $fingerprint"
  echo "        trustBundlePem: |"
  while IFS= read -r line; do
    echo "          $line"
  done <<<"$ca_pem"
} >>"$tmp"
mv "$tmp" "$ENV_FILE"

echo "wrote transportTLS bundle config (fingerprint $fingerprint) to $ENV_FILE"
