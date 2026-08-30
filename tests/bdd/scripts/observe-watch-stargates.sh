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
for tool in kubectl base64 grpcurl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$script_dir/../../.." && pwd -P)"
proto_path="$repo_root/src/libraries/rust/stargate/crates/proto/proto"
ca_file="$(mktemp "${TMPDIR:-/tmp}/nvcf-bdd-watch-ca.XXXXXX")"
trap 'rm -f "$ca_file"' EXIT

kubectl --context "$kube_context" get secret "$ca_secret" -n "$namespace" \
  -o 'jsonpath={.data.ca\.crt}' | base64 -d >"$ca_file"
if [[ ! -s "$ca_file" ]]; then
  echo "CA secret $namespace/$ca_secret did not contain ca.crt" >&2
  exit 1
fi

set +e
output="$(grpcurl \
  -max-time "$duration_seconds" \
  -cacert "$ca_file" \
  -authority "$tls_authority" \
  -import-path "$proto_path" \
  -proto stargate.proto \
  "$endpoint" \
  stargate.StargateControlPlane/WatchStargates 2>&1)"
grpcurl_status=$?
set -e

printf '%s\n' "$output"

if [[ "$grpcurl_status" -eq 0 ]]; then
  echo "WatchStargates ended before the observation deadline" >&2
  exit 1
fi
if ! grep -Eq '^[[:space:]]*\{' <<<"$output"; then
  echo "WatchStargates did not return a streamed snapshot" >&2
  exit 1
fi
if ! grep -Eiq 'DeadlineExceeded|context deadline exceeded' <<<"$output"; then
  echo "WatchStargates failed before the expected observation deadline" >&2
  exit 1
fi

