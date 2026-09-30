#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ $# -lt 7 || $(( ( $# - 4 ) % 3 )) -ne 0 ]]; then
  echo "usage: $0 <function-name> <container> <kube-context> <timeout> <metric> <comparison> <count> [...]" >&2
  exit 64
fi

function_name="$1"
container_name="$2"
kube_context="$3"
timeout="$4"
shift 4

if [[ -z "$function_name" || -z "$container_name" || -z "$kube_context" ]]; then
  echo "function-name, container, and kube-context must be non-empty" >&2
  exit 64
fi
if ! [[ "$timeout" =~ ^([1-9][0-9]*)(s|m|h)$ ]]; then
  echo "timeout must be a positive duration ending in s, m, or h, got: $timeout" >&2
  exit 64
fi
case "${BASH_REMATCH[2]}" in
  s) timeout_seconds="${BASH_REMATCH[1]}" ;;
  m) timeout_seconds=$(( BASH_REMATCH[1] * 60 )) ;;
  h) timeout_seconds=$(( BASH_REMATCH[1] * 3600 )) ;;
esac
for tool in kubectl jq awk; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

metric_names=()
comparisons=()
expected_counts=()
while [[ $# -gt 0 ]]; do
  metric="$1"
  comparison="$2"
  expected_count="$3"
  shift 3

  if ! [[ "$metric" =~ ^[a-zA-Z_:][a-zA-Z0-9_:]*$ ]]; then
    echo "invalid Prometheus metric name: $metric" >&2
    exit 64
  fi
  if [[ "$comparison" != "exactly" && "$comparison" != "at least" ]]; then
    echo "comparison must be exactly or at least, got: $comparison" >&2
    exit 64
  fi
  if ! [[ "$expected_count" =~ ^[0-9]+$ ]]; then
    echo "count must be a non-negative integer, got: $expected_count" >&2
    exit 64
  fi

  metric_names+=("$metric")
  comparisons+=("$comparison")
  expected_counts+=("$expected_count")
done

connected_series_count() {
  local metric="$1"
  awk -v metric="$metric" '
    {
      series = $1
      if ((series == metric || index(series, metric "{") == 1) && $2 == "1") {
        count++
      }
    }
    END { print count + 0 }
  '
}

deadline=$(( $(date +%s) + timeout_seconds ))
last_summary="no running pod for function $function_name containing container $container_name"

while true; do
  if ! pods_json="$(kubectl --context "$kube_context" get pods -A -o json 2>&1)"; then
    last_summary="pod discovery failed: $pods_json"
  elif ! pod_rows="$(printf '%s\n' "$pods_json" | jq -r --arg function "$function_name" --arg container "$container_name" '
    .items[]?
      | select(.metadata.deletionTimestamp == null)
      | select(.status.phase == "Running")
      | select(.metadata.annotations["function-name"] == $function)
      | select(any(.spec.containers[]?; .name == $container))
      | [.metadata.namespace, .metadata.name]
      | @tsv
  ' 2>&1)"; then
    last_summary="pod discovery response could not be parsed: $pod_rows"
  elif [[ -z "$pod_rows" ]]; then
    last_summary="no running pod for function $function_name containing container $container_name"
  else
    all_match=true
    summaries=()

    while IFS=$'\t' read -r namespace pod_name; do
      if ! metrics="$(kubectl --context "$kube_context" get --raw "/api/v1/namespaces/$namespace/pods/$pod_name:9089/proxy/metrics" 2>&1)"; then
        summaries+=("$namespace/$pod_name metrics scrape failed: $metrics")
        all_match=false
        continue
      fi
      if [[ -z "$metrics" ]]; then
        summaries+=("$namespace/$pod_name metrics scrape returned an empty response")
        all_match=false
        continue
      fi

      for index in "${!metric_names[@]}"; do
        metric="${metric_names[$index]}"
        comparison="${comparisons[$index]}"
        expected_count="${expected_counts[$index]}"
        observed_count="$(printf '%s\n' "$metrics" | connected_series_count "$metric")"
        summaries+=("$namespace/$pod_name $metric=$observed_count")

        case "$comparison" in
          exactly)
            [[ "$observed_count" -eq "$expected_count" ]] || all_match=false
            ;;
          "at least")
            [[ "$observed_count" -ge "$expected_count" ]] || all_match=false
            ;;
        esac
      done
    done <<<"$pod_rows"

    last_summary="${summaries[*]}"
    if [[ "$all_match" == true ]]; then
      printf '%s\n' "${summaries[@]}"
      exit 0
    fi
  fi

  now="$(date +%s)"
  if [[ "$now" -ge "$deadline" ]]; then
    echo "timed out after $timeout waiting for Pylon metrics in context $kube_context ($last_summary)" >&2
    exit 1
  fi
  remaining=$(( deadline - now ))
  sleep_seconds=5
  if [[ "$remaining" -lt "$sleep_seconds" ]]; then
    sleep_seconds="$remaining"
  fi
  sleep "$sleep_seconds"
done
