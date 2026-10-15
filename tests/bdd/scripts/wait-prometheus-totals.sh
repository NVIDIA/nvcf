#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Sums Prometheus samples across every running pod selected by a label
# selector and polls until each expected total holds. Each expectation is a
# metric name, a comma-separated name=value label filter (empty matches every
# sample), a comparison (exactly or at least), and a non-negative total.

set -euo pipefail

if [[ $# -lt 9 || $(( ( $# - 5 ) % 4 )) -ne 0 ]]; then
  echo "usage: $0 <port> <pod-selector> <namespace> <kube-context> <timeout> <metric> <labels> <comparison> <total> [...]" >&2
  exit 64
fi

port="$1"
selector="$2"
namespace="$3"
kube_context="$4"
timeout="$5"
shift 5

if [[ -z "$selector" || -z "$namespace" || -z "$kube_context" ]]; then
  echo "pod-selector, namespace, and kube-context must be non-empty" >&2
  exit 64
fi
if ! [[ "$port" =~ ^[1-9][0-9]{0,4}$ ]] || [[ "$port" -gt 65535 ]]; then
  echo "port must be an integer from 1 to 65535, got: $port" >&2
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

label_pair='[a-zA-Z_][a-zA-Z0-9_]*=[^",\\[:space:]]+'
metric_names=()
label_filters=()
comparisons=()
expected_totals=()
while [[ $# -gt 0 ]]; do
  metric="$1"
  labels="$2"
  comparison="$3"
  expected_total="$4"
  shift 4

  if ! [[ "$metric" =~ ^[a-zA-Z_:][a-zA-Z0-9_:]*$ ]]; then
    echo "invalid Prometheus metric name: $metric" >&2
    exit 64
  fi
  if [[ -n "$labels" ]] && ! [[ "$labels" =~ ^${label_pair}(,${label_pair})*$ ]]; then
    echo "labels must be comma-separated name=value pairs, got: $labels" >&2
    exit 64
  fi
  if [[ "$comparison" != "exactly" && "$comparison" != "at least" ]]; then
    echo "comparison must be exactly or at least, got: $comparison" >&2
    exit 64
  fi
  if ! [[ "$expected_total" =~ ^[0-9]+$ ]]; then
    echo "total must be a non-negative integer, got: $expected_total" >&2
    exit 64
  fi

  metric_names+=("$metric")
  label_filters+=("$labels")
  comparisons+=("$comparison")
  expected_totals+=("$expected_total")
done

# matching_total sums the value of every sample of one metric whose label set
# contains each wanted name="value" pair. Comment lines, other metrics, and
# metrics that only share a name prefix are ignored. An optional sample
# timestamp after the value is ignored.
matching_total() {
  awk -v metric="$1" -v labels="$2" '
    BEGIN {
      wanted_count = (labels == "") ? 0 : split(labels, wanted, ",")
    }
    /^#/ || NF == 0 { next }
    {
      line = $0
      if (index(line, metric "{") == 1) {
        if (!match(line, /[}][^}]*$/)) { next }
        block = substr(line, length(metric) + 2, RSTART - length(metric) - 2)
        rest = substr(line, RSTART + 1)
      } else if (index(line, metric " ") == 1) {
        block = ""
        rest = substr(line, length(metric) + 1)
      } else {
        next
      }
      padded = "," block ","
      for (i = 1; i <= wanted_count; i++) {
        separator = index(wanted[i], "=")
        pair = substr(wanted[i], 1, separator - 1) "=\"" substr(wanted[i], separator + 1) "\""
        if (index(padded, "," pair ",") == 0) { next }
      }
      split(rest, fields, " ")
      total += fields[1]
    }
    END { printf "%.15g\n", total + 0 }
  '
}

total_matches() {
  awk -v observed="$1" -v comparison="$2" -v expected="$3" '
    BEGIN {
      if (comparison == "exactly") { exit !(observed + 0 == expected + 0) }
      exit !(observed + 0 >= expected + 0)
    }
  '
}

deadline=$(( $(date +%s) + timeout_seconds ))
last_summary="no running pod matches $selector in namespace $namespace"

while true; do
  if ! pods_json="$(kubectl --context "$kube_context" --namespace "$namespace" get pods --selector "$selector" --output json 2>&1)"; then
    last_summary="pod discovery failed: $pods_json"
  elif ! pod_names="$(printf '%s\n' "$pods_json" | jq -r '
    .items[]?
      | select(.metadata.deletionTimestamp == null)
      | select(.status.phase == "Running")
      | .metadata.name
  ' 2>&1)"; then
    last_summary="pod discovery response could not be parsed: $pod_names"
  elif [[ -z "$pod_names" ]]; then
    last_summary="no running pod matches $selector in namespace $namespace"
  else
    all_match=true
    summaries=()
    scraped=""
    pod_count=0

    while IFS= read -r pod_name; do
      pod_count=$(( pod_count + 1 ))
      if ! metrics="$(kubectl --context "$kube_context" get --raw "/api/v1/namespaces/$namespace/pods/$pod_name:$port/proxy/metrics" 2>&1)"; then
        summaries+=("$namespace/$pod_name metrics scrape failed: $metrics")
        all_match=false
        continue
      fi
      if [[ -z "$metrics" ]]; then
        summaries+=("$namespace/$pod_name metrics scrape returned an empty response")
        all_match=false
        continue
      fi
      scraped+="$metrics"$'\n'
    done <<<"$pod_names"

    if [[ "$all_match" == true ]]; then
      for index in "${!metric_names[@]}"; do
        metric="${metric_names[$index]}"
        labels="${label_filters[$index]}"
        comparison="${comparisons[$index]}"
        expected_total="${expected_totals[$index]}"
        observed_total="$(printf '%s' "$scraped" | matching_total "$metric" "$labels")"
        summaries+=("$metric{$labels} total=$observed_total across $pod_count pods, want $comparison $expected_total")
        if ! total_matches "$observed_total" "$comparison" "$expected_total"; then
          all_match=false
        fi
      done
    fi

    last_summary="${summaries[*]}"
    if [[ "$all_match" == true ]]; then
      printf '%s\n' "${summaries[@]}"
      exit 0
    fi
  fi

  now="$(date +%s)"
  if [[ "$now" -ge "$deadline" ]]; then
    echo "timed out after $timeout waiting for Prometheus totals in context $kube_context ($last_summary)" >&2
    exit 1
  fi
  remaining=$(( deadline - now ))
  sleep_seconds=5
  if [[ "$remaining" -lt "$sleep_seconds" ]]; then
    sleep_seconds="$remaining"
  fi
  sleep "$sleep_seconds"
done
