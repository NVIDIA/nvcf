#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <gateway-hostname>" >&2
  exit 64
fi

gateway_hostname="$1"
gateway_ipv4="${EKS_GATEWAY_IPV4:-}"

if [[ -z "$gateway_ipv4" ]]; then
  if ! command -v host >/dev/null 2>&1; then
    echo "required tool not found: host" >&2
    exit 127
  fi
  # A newly provisioned NLB can return an address once and then briefly
  # return an empty response while DNS propagation converges. Retry here
  # instead of assuming the preceding DNS wait makes every lookup stable.
  for attempt in {1..36}; do
    gateway_ipv4="$(host "$gateway_hostname" 2>/dev/null | awk '/has address/ { print $4; exit }' || true)"
    if [[ "$gateway_ipv4" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
      break
    fi
    if [[ "$attempt" -lt 36 ]]; then
      sleep 5
    fi
  done
fi

if ! [[ "$gateway_ipv4" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  echo "could not resolve an IPv4 address for $gateway_hostname" >&2
  exit 1
fi

printf '%s.nip.io\n' "${gateway_ipv4//./-}"
