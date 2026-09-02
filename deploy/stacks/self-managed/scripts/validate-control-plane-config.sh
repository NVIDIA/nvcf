#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

control_plane_id="${CONTROL_PLANE_ID:-}"
control_plane_domain="${CONTROL_PLANE_DOMAIN:-}"

# An empty ID selects the legacy single-control-plane behavior.
if [[ -z "$control_plane_id" ]]; then
  exit 0
fi

dns_label_regex='^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
dns_name_regex='^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$'

if [[ "$control_plane_id" == default ]] ||
  (( ${#control_plane_id} > 20 )) ||
  [[ ! "$control_plane_id" =~ $dns_label_regex ]]; then
  echo "Error: CONTROL_PLANE_ID must be a DNS-1123 label of at most 20 characters and must not be 'default'." >&2
  exit 1
fi

if [[ -z "$control_plane_domain" ]]; then
  echo "Error: CONTROL_PLANE_DOMAIN is required for a named control plane." >&2
  exit 1
fi
if (( ${#control_plane_domain} > 253 )) ||
  [[ ! "$control_plane_domain" =~ $dns_name_regex ]]; then
  echo "Error: CONTROL_PLANE_DOMAIN must be a lowercase DNS name." >&2
  exit 1
fi

for gateway in \
  "${CONTROL_PLANE_SHARED_GATEWAY:-${control_plane_id}-shared-gw}" \
  "${CONTROL_PLANE_GRPC_GATEWAY:-${control_plane_id}-grpc-gw}" \
  "${CONTROL_PLANE_NATS_GATEWAY:-${control_plane_id}-nats-gw}"; do
  if (( ${#gateway} > 63 )) ||
    [[ ! "$gateway" =~ $dns_label_regex ]] ||
    [[ "$gateway" != "${control_plane_id}-"* ]]; then
    echo "Error: named control-plane Gateway '$gateway' must be a DNS-1123 label starting with '${control_plane_id}-'." >&2
    exit 1
  fi
done
