#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Reads the NVCF root CA public certificate from OpenBao and merges the
# transportTLS bundle configuration into agentConfig.mergeConfig in a
# compute-plane Helmfile environment file. The fingerprint follows
# the canonical nvcf-trust-bundle-v1 algorithm (same as nvcf-cli and
# NVCA, which recomputes it and fails closed on divergence).
#
# OpenBao exposes PKI CA certificates without authentication. The helper does
# not read the root-token Secret, so a failed or replaced local port-forward
# cannot disclose a privileged credential.
#
# Usage: write-transport-trust-env.sh <compute-env-file> <kube-context>

set -euo pipefail

ENV_FILE="${1:?usage: write-transport-trust-env.sh <compute-env-file> <kube-context>}"
KUBE_CONTEXT="${2:?usage: write-transport-trust-env.sh <compute-env-file> <kube-context>}"
OPENBAO_NAMESPACE="${OPENBAO_NAMESPACE:-vault-system}"
OPENBAO_SERVICE="${OPENBAO_SERVICE:-openbao-server}"
ROOT_PKI_PATH="${ROOT_PKI_PATH:-services/all/pki/root}"
CURL_CONNECT_TIMEOUT_SECONDS="${CURL_CONNECT_TIMEOUT_SECONDS:-2}"
CURL_MAX_TIME_SECONDS="${CURL_MAX_TIME_SECONDS:-5}"

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

pf_pid=""
tmp=""
ca_file=""
pf_log=""
curl_error=""
cleanup() {
  if [[ -n "$pf_pid" ]]; then
    kill "$pf_pid" 2>/dev/null || true
    wait "$pf_pid" 2>/dev/null || true
  fi
  rm -f -- "$pf_log" "$curl_error"
  [[ -n "$tmp" ]] && rm -f -- "$tmp"
  [[ -n "$ca_file" ]] && rm -f -- "$ca_file"
}
trap cleanup EXIT

# Let kubectl bind an unused loopback port and capture the selected port from
# its readiness line. Capture both output streams because kubectl writes the
# readiness line to stdout and startup diagnostics to stderr.
pf_log="$(mktemp "${TMPDIR:-/tmp}/openbao-port-forward.XXXXXX")"
kubectl port-forward -n "$OPENBAO_NAMESPACE" "svc/$OPENBAO_SERVICE" \
  :8200 --address=127.0.0.1 --context "$KUBE_CONTEXT" \
  >"$pf_log" 2>&1 &
pf_pid=$!
local_port=""
for _ in $(seq 1 50); do
  local_port="$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) -> 8200$/\1/p' "$pf_log" | head -n 1)"
  if [[ -n "$local_port" ]]; then
    if ! kill -0 "$pf_pid" 2>/dev/null; then
      wait "$pf_pid" 2>/dev/null || pf_rc=$?
      pf_pid=""
      echo "OpenBao port-forward exited before becoming ready (rc=${pf_rc:-1})" >&2
      sed -n '1,5p' "$pf_log" >&2
      exit 1
    fi
    break
  fi
  if ! kill -0 "$pf_pid" 2>/dev/null; then
    wait "$pf_pid" 2>/dev/null || pf_rc=$?
    pf_pid=""
    echo "OpenBao port-forward exited before becoming ready (rc=${pf_rc:-1})" >&2
    sed -n '1,5p' "$pf_log" >&2
    exit 1
  fi
  sleep 0.1
done
if [[ -z "$local_port" ]]; then
  echo "OpenBao port-forward did not become ready" >&2
  sed -n '1,5p' "$pf_log" >&2
  exit 1
fi

curl_error="$(mktemp "${TMPDIR:-/tmp}/openbao-curl-error.XXXXXX")"

ca_pem=""
for _ in $(seq 1 20); do
  if ! kill -0 "$pf_pid" 2>/dev/null; then
    wait "$pf_pid" 2>/dev/null || pf_rc=$?
    pf_pid=""
    echo "OpenBao port-forward exited while reading the public CA (rc=${pf_rc:-1})" >&2
    sed -n '1,5p' "$pf_log" >&2
    break
  fi
  response=""
  if response="$(curl --silent --show-error --fail \
      --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" \
      --max-time "$CURL_MAX_TIME_SECONDS" \
      "http://127.0.0.1:$local_port/v1/$ROOT_PKI_PATH/cert/ca" 2>"$curl_error")"; then
    if ! kill -0 "$pf_pid" 2>/dev/null; then
      wait "$pf_pid" 2>/dev/null || pf_rc=$?
      pf_pid=""
      echo "OpenBao port-forward exited while reading the public CA (rc=${pf_rc:-1})" >&2
      sed -n '1,5p' "$pf_log" >&2
      ca_pem=""
      break
    fi
    if ca_pem="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["certificate"])' <<<"$response" 2>"$curl_error")"; then
      break
    fi
  fi
  ca_pem=""
  sleep 1
done
if [[ -z "$ca_pem" ]] || ! grep -q "BEGIN CERTIFICATE" <<<"$ca_pem"; then
  echo "failed to read root CA PEM from OpenBao at $ROOT_PKI_PATH/cert/ca" >&2
  if [[ -s "$curl_error" ]]; then
    echo "last request error:" >&2
    sed -n '1,5p' "$curl_error" >&2
  fi
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

# Merge transport trust into the existing agent configuration. The PKI
# feature switches the tunnel to secure mode, so remove the local-only
# insecure flag while preserving every unrelated mergeConfig field.
tmp="$(mktemp "${TMPDIR:-/tmp}/compute-env.XXXXXX")"
ca_file="$(mktemp "${TMPDIR:-/tmp}/transport-ca.XXXXXX")"
printf '%s\n' "$ca_pem" >"$ca_file"
python3 - "$ENV_FILE" "$tmp" "$ca_file" "$fingerprint" <<'PYEOF'
import pathlib
import sys

env_path = pathlib.Path(sys.argv[1])
output_path = pathlib.Path(sys.argv[2])
ca_path = pathlib.Path(sys.argv[3])
fingerprint = sys.argv[4]
lines = env_path.read_text().splitlines(keepends=True)


def indentation(line):
    return len(line) - len(line.lstrip(" "))


def content(line):
    stripped = line.strip()
    return bool(stripped) and not stripped.startswith("#")


agent_indexes = [index for index, line in enumerate(lines) if line.rstrip() == "agentConfig:"]
if len(agent_indexes) != 1:
    raise SystemExit(f"expected exactly one top-level agentConfig block in {env_path}")
agent_start = agent_indexes[0]
agent_end = len(lines)
for index in range(agent_start + 1, len(lines)):
    if content(lines[index]) and indentation(lines[index]) == 0:
        agent_end = index
        break

agent_child_indents = [
    indentation(lines[index])
    for index in range(agent_start + 1, agent_end)
    if content(lines[index])
]
if not agent_child_indents:
    raise SystemExit(f"agentConfig has no fields in {env_path}")
agent_child_indent = min(agent_child_indents)

merge_indexes = [
    index
    for index in range(agent_start + 1, agent_end)
    if indentation(lines[index]) == agent_child_indent
    and lines[index].strip() in {"mergeConfig: |", "mergeConfig: |-", "mergeConfig: |+"}
]
if len(merge_indexes) != 1:
    raise SystemExit(f"expected agentConfig.mergeConfig to be one literal block scalar in {env_path}")
merge_start = merge_indexes[0]
merge_indent = indentation(lines[merge_start])
merge_end = agent_end
for index in range(merge_start + 1, agent_end):
    if content(lines[index]) and indentation(lines[index]) <= merge_indent:
        merge_end = index
        break

content_indents = [
    indentation(line)
    for line in lines[merge_start + 1:merge_end]
    if line.strip()
]
merge_content_indent = min(content_indents, default=merge_indent + 2)
if merge_content_indent <= merge_indent:
    raise SystemExit(f"agentConfig.mergeConfig has invalid indentation in {env_path}")

merge_lines = []
for line in lines[merge_start + 1:merge_end]:
    if line.strip() and indentation(line) < merge_content_indent:
        raise SystemExit(f"agentConfig.mergeConfig has invalid indentation in {env_path}")
    merge_lines.append(line[merge_content_indent:] if line.strip() else line)

workload_indexes = [
    index
    for index, line in enumerate(merge_lines)
    if indentation(line) == 0 and line.strip() == "workload:"
]
if len(workload_indexes) > 1:
    raise SystemExit(f"agentConfig.mergeConfig contains multiple workload blocks in {env_path}")

trust_lines = [
    "  transportTLS:\n",
    "    trustMode: bundle\n",
    f"    trustBundleFingerprint: {fingerprint}\n",
    "    trustBundlePem: |\n",
]
trust_lines.extend(f"      {line}\n" for line in ca_path.read_text().splitlines())

if not workload_indexes:
    if merge_lines and merge_lines[-1].strip():
        merge_lines.append("\n")
    merge_lines.extend(["workload:\n", *trust_lines])
else:
    workload_start = workload_indexes[0]
    workload_end = len(merge_lines)
    for index in range(workload_start + 1, len(merge_lines)):
        if content(merge_lines[index]) and indentation(merge_lines[index]) == 0:
            workload_end = index
            break

    preserved = []
    index = workload_start + 1
    while index < workload_end:
        line = merge_lines[index]
        if indentation(line) == 2 and line.strip().startswith("stargateQUICInsecure:"):
            index += 1
            continue
        if indentation(line) == 2 and line.strip() == "transportTLS:":
            index += 1
            while index < workload_end:
                if content(merge_lines[index]) and indentation(merge_lines[index]) <= 2:
                    break
                index += 1
            continue
        preserved.append(line)
        index += 1

    merge_lines = [
        *merge_lines[:workload_start + 1],
        *preserved,
        *trust_lines,
        *merge_lines[workload_end:],
    ]

merge_prefix = " " * merge_content_indent
indented_merge = [f"{merge_prefix}{line}" if line.strip() else line for line in merge_lines]
output_path.write_text("".join([
    *lines[:merge_start + 1],
    *indented_merge,
    *lines[merge_end:],
]))
PYEOF
mv "$tmp" "$ENV_FILE"

echo "wrote transportTLS bundle config (fingerprint $fingerprint) to $ENV_FILE"
