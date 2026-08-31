#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ $# -ne 6 ]]; then
  echo "usage: $0 <endpoint> <tls-authority> <ca-secret> <namespace> <kube-context> <duration-seconds>" >&2
  exit 64
fi

endpoint="$1"
tls_authority="$2"
ca_secret="$3"
namespace="$4"
kube_context="$5"
duration_seconds="$6"

for value_name in endpoint tls_authority ca_secret namespace kube_context; do
  if [[ -z "${!value_name}" ]]; then
    echo "$value_name must be non-empty" >&2
    exit 64
  fi
done
if ! [[ "$duration_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "duration-seconds must be a positive integer, got: $duration_seconds" >&2
  exit 64
fi
for tool in kubectl base64 grpcurl jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

# Propagate W3C trace context on the outbound gRPC call so the observation
# joins the same trace as the product spans it exercises. Each invocation
# generates its own trace and span identifiers.
random_hex() {
  local want=$(( $1 * 2 ))
  local hex=""
  while [[ ${#hex} -lt $want ]]; do
    hex+="$(printf '%04x' $(( RANDOM % 65536 )))"
  done
  printf '%s' "${hex:0:$want}"
}
trace_id="$(random_hex 16)"
span_id="$(random_hex 8)"
if [[ "$trace_id" =~ ^0+$ ]]; then
  trace_id="${trace_id:0:31}1"
fi
if [[ "$span_id" =~ ^0+$ ]]; then
  span_id="${span_id:0:15}1"
fi
traceparent="00-${trace_id}-${span_id}-01"
echo "WatchStargates traceparent: $traceparent" >&2

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$script_dir/../../.." && pwd -P)"
proto_path="$repo_root/src/libraries/rust/stargate/crates/proto/proto"
ca_file="$(mktemp "${TMPDIR:-/tmp}/nvcf-bdd-watch-ca.XXXXXX")"
stdout_file="$(mktemp "${TMPDIR:-/tmp}/nvcf-bdd-watch-stdout.XXXXXX")"
stderr_file="$(mktemp "${TMPDIR:-/tmp}/nvcf-bdd-watch-stderr.XXXXXX")"
trap 'rm -f "$ca_file" "$stdout_file" "$stderr_file"' EXIT

kubectl --context "$kube_context" get secret "$ca_secret" -n "$namespace" \
  -o 'jsonpath={.data.ca\.crt}' | base64 -d >"$ca_file"
if [[ ! -s "$ca_file" ]]; then
  echo "CA secret $namespace/$ca_secret did not contain ca.crt" >&2
  exit 1
fi

set +e
started_at="$(date +%s)"
grpcurl \
  -max-time "$duration_seconds" \
  -emit-defaults \
  -cacert "$ca_file" \
  -authority "$tls_authority" \
  -H "traceparent: $traceparent" \
  -import-path "$proto_path" \
  -proto stargate.proto \
  "$endpoint" \
  stargate.StargateControlPlane/WatchStargates >"$stdout_file" 2>"$stderr_file"
grpcurl_status=$?
finished_at="$(date +%s)"
set -e

cat "$stdout_file"
cat "$stderr_file" >&2

if [[ "$grpcurl_status" -eq 0 ]]; then
  echo "WatchStargates ended before the observation deadline" >&2
  exit 1
fi
if ! jq -se 'length > 0 and all(.[]; type == "object" and (has("stargates") or has("watchStargateUrls")))' "$stdout_file" >/dev/null; then
  echo "WatchStargates did not return a streamed snapshot" >&2
  exit 1
fi
if ! grep -Eiq 'DeadlineExceeded|context deadline exceeded' "$stderr_file"; then
  echo "WatchStargates failed before the expected observation deadline" >&2
  exit 1
fi
elapsed_seconds=$(( finished_at - started_at ))
if [[ "$elapsed_seconds" -lt "$duration_seconds" ]]; then
  echo "WatchStargates ended after ${elapsed_seconds}s before the ${duration_seconds}s observation deadline" >&2
  exit 1
fi
