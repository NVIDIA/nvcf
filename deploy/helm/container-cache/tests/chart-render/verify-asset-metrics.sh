#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Per-asset request and byte counters. Two properties matter and neither is
# obvious from reading the rendered config:
#
#   1. Every file and byte range of one model, repo, or bucket collapses to a
#      single label value, so a model pulled as hundreds of 512MiB ranges is one
#      series rather than hundreds.
#   2. Requests the cache never consulted are counted as NONE rather than
#      dropped. On a live sample those were 45.6 percent of requests and 786
#      GiB, and the older metric block skipped all of them.
#
# Run from the chart subtree:
#   bash tests/chart-render/verify-asset-metrics.sh
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/../.." && pwd)/deploy"
OUT="$(mktemp)"; trap 'rm -f "$OUT"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
has() { grep -F -q -- "$1" "$OUT" || fail "missing: $1"; }

helm template t "$CHART_DIR" > "$OUT" 2>/dev/null

echo "1. counters exist and are counters, not gauges"
has 'proxy_cache_asset_requests_total'
has 'proxy_cache_asset_bytes_total'
# The declaration spans two lines: `<var> = prometheus:counter(` then the metric
# name on the next. Match the pair rather than a single line.
counter_type() { grep -B1 -F "\"$1\"" "$OUT" | grep -o 'prometheus:[a-z]*' | head -1; }
[ "$(counter_type proxy_cache_asset_bytes_total)" = "prometheus:counter" ] \
  || fail "asset bytes must be a counter; a gauge cannot be rate()'d for GB/s"
[ "$(counter_type proxy_cache_asset_requests_total)" = "prometheus:counter" ] \
  || fail "asset requests must be a counter"

echo "2. labelled by source, asset and cache_status"
has '{"source", "asset", "cache_status"}'

echo "3. all three upstream families are classified"
has 'source = "ngc"'
has 'source = "hf"'
has 'source = "s3"'
has 'source, asset = "other", "other"'

echo "4. the asset key drops the query string, so ranges and presigned params collapse"
# NGC range URLs carry versionId, Signature and ssec-key. Keying on anything
# past '?' would make every byte range its own series.
has 'request_uri:match("^([^?]*)")'

echo "5. uncounted traffic is named, not dropped"
has 'if status == nil or status == "" then status = "NONE" end'
# The counters must sit outside the older guard, which requires a non-nil
# cache_status and therefore discards exactly the traffic we are trying to see.
# The asset block must have its own guard that does NOT require cache_status;
# the older guard does, and therefore discards the traffic we want to see.
# Note: no `... | grep -q` here. grep -q exits on first match, awk takes SIGPIPE,
# and `set -o pipefail` reports the pipeline as failed even when it matched.
asset_guard='if request_uri ~= nil and not string.find(request_uri, "manifest") then'
grep -F -q -- "$asset_guard" "$OUT" \
  || fail "asset counters must use a guard free of cache_status, or they inherit the drop"

echo "6. label values are bounded"
has 'if #asset > 120 then asset = asset:sub(1, 120) end'

echo "7. manifest traffic stays excluded, as it is for the other metrics"
n_manifest="$(grep -c 'string.find(request_uri, "manifest")' "$OUT" || true)"
[ "${n_manifest}" -ge 2 ] \
  || fail "asset counters must keep the manifest exclusion the other metrics use"

echo "PASS: per-asset metric render assertions hold"
