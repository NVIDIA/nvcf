#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

url="${1:?gateway route URL is required}"
timeout_seconds="${GATEWAY_ROUTE_TIMEOUT_SECONDS:-60}"
retry_interval_seconds="${GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS:-2}"

if ! [[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "ERROR GATEWAY_ROUTE_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 2
fi
if ! [[ "$retry_interval_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "ERROR GATEWAY_ROUTE_RETRY_INTERVAL_SECONDS must be a positive integer" >&2
  exit 2
fi

elapsed=0
while ((elapsed < timeout_seconds)); do
  if curl -sSf --connect-timeout 5 --max-time 10 "$url" >/dev/null 2>&1; then
    exit 0
  fi
  echo "INFO Gateway route not reachable yet, waiting... (${elapsed}/${timeout_seconds} seconds)"
  sleep "$retry_interval_seconds"
  elapsed=$((elapsed + retry_interval_seconds))
done

echo "ERROR Gateway route did not become reachable within ${timeout_seconds} seconds" >&2
exit 1
