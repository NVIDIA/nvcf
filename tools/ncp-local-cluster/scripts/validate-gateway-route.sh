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

start_time="$(date +%s)"
deadline=$((start_time + timeout_seconds))

while :; do
  now="$(date +%s)"
  remaining=$((deadline - now))
  if ((remaining <= 0)); then
    break
  fi

  if curl -sSf --connect-timeout 5 --max-time "$remaining" "$url" >/dev/null 2>&1; then
    exit 0
  fi

  now="$(date +%s)"
  remaining=$((deadline - now))
  if ((remaining <= 0)); then
    break
  fi

  elapsed=$((timeout_seconds - remaining))
  echo "INFO Gateway route not reachable yet, waiting... (${elapsed}/${timeout_seconds} seconds)"
  sleep_seconds="$retry_interval_seconds"
  if ((sleep_seconds > remaining)); then
    sleep_seconds="$remaining"
  fi
  sleep "$sleep_seconds"
done

echo "ERROR Gateway route did not become reachable within ${timeout_seconds} seconds" >&2
exit 1
