#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Create the CRIU matrix as NVCF Helm functions: for each case, the chart
# with the case's values as its defaults is pushed as its own chart version,
# and a function is created from it, so a deployment needs no configuration.
#
#   ORG=<org> NGC_CLI_API_KEY=... ./create-functions.sh            # every case for ARCH
#   CASES="sts-tp2 vllm-llama-8b" ORG=<org> ./create-functions.sh  # some cases
#   DRY_RUN=1 ORG=<org> ./create-functions.sh                      # print the commands
#
# Environment:
#   ORG, TEAM          NGC org (required) and team (optional) for chart and functions
#   NGC_CLI_API_KEY    NGC key that can push charts and create functions in ORG
#   NGC_ENV            prod (default) or stg
#   PREFIX             reg (default, the regression matrix) or tst (ad hoc tests);
#                      functions are named nvsnap-<PREFIX>-<case>
#   ARCH               node architecture to select cases for (default arm64)
#   HAVE               cluster features cases may require (default:
#                      "DynamoGraphDeployment"; add LeaderWorkerSet where installed)
#   CASES              case names (files under cases/) instead of the selection
#   HF_TOKEN, NGC_API_KEY  function secrets, for the cases that need them
#   CHART_BASE_VERSION base chart version (default 0.2.0)
set -euo pipefail

D="$(cd "$(dirname "$0")" && pwd)"
: "${ORG:?set ORG to the NGC org}"
: "${NGC_CLI_API_KEY:?set NGC_CLI_API_KEY}"
export NGC_CLI_API_KEY NGC_CLI_ORG="$ORG"
TEAM="${TEAM:-}"
PREFIX="${PREFIX:-reg}"
ARCH="${ARCH:-arm64}"
HAVE="${HAVE:-DynamoGraphDeployment}"
DRY_RUN="${DRY_RUN:-0}"
BASE="${CHART_BASE_VERSION:-0.2.0}"
CHART=nvsnap-criu-matrix
case "${NGC_ENV:-prod}" in
  prod) export NGC_CLI_API_URL=https://api.ngc.nvidia.com ;;
  stg | staging) export NGC_CLI_API_URL=https://api.stg.ngc.nvidia.com ;;
  *) echo "NGC_ENV must be prod or stg" >&2; exit 2 ;;
esac
REPO="$ORG${TEAM:+/$TEAM}/$CHART"
if [ -n "$TEAM" ]; then export NGC_CLI_TEAM="$TEAM"; fi

# run prints the command with secret values masked, then runs it unless DRY_RUN.
run() {
  local shown=() a
  for a in "$@"; do
    case "$a" in
      HF_TOKEN:* | NGC_API_KEY:*) shown+=("${a%%:*}:***") ;;
      *) shown+=("$a") ;;
    esac
  done
  echo "+ ${shown[*]}"
  [ "$DRY_RUN" = 1 ] || "$@"
}

# field <case file> <key>: a value under the case's `function:` block.
field() {
  python3 - "$1" "$2" <<'PY'
import sys, yaml
f = (yaml.safe_load(open(sys.argv[1])) or {}).get("function") or {}
v = f.get(sys.argv[2])
print(" ".join(v) if isinstance(v, list) else (v or ""))
PY
}

selected() {
  if [ -n "${CASES:-}" ]; then echo "$CASES"; return; fi
  local f c arch req ok r
  for f in "$D"/cases/*.yaml; do
    c="$(basename "$f" .yaml)"
    arch="$(field "$f" arch)"
    req="$(field "$f" requires)"
    if [ -n "$arch" ] && [[ " $arch " != *" $ARCH "* ]]; then continue; fi
    ok=1
    for r in $req; do [[ " $HAVE " == *" $r "* ]] || ok=0; done
    [ "$ok" = 1 ] && echo "$c"
  done
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
[ "$DRY_RUN" = 1 ] || ngc registry chart info "$REPO" >/dev/null 2>&1 ||
  run ngc registry chart create "$REPO" --short-desc "nvsnap CRIU test matrix: one inference workload in a chosen shape"

for c in $(selected); do
  f="$D/cases/$c.yaml"
  [ -f "$f" ] || { echo "no case $c" >&2; exit 2; }
  ver="$BASE-$c"
  dir="$WORK/$c/$CHART"
  mkdir -p "$WORK/$c"
  cp -r "$D/chart" "$dir"
  # The case's values become the chart's defaults; the function block is
  # this script's, not the chart's.
  python3 - "$dir/values.yaml" "$f" <<'PY'
import sys, yaml
base = yaml.safe_load(open(sys.argv[1]))
case = yaml.safe_load(open(sys.argv[2])) or {}
case.pop("function", None)
def merge(a, b):
    for k, v in b.items():
        a[k] = merge(a[k], v) if isinstance(v, dict) and isinstance(a.get(k), dict) else v
    return a
yaml.safe_dump(merge(base, case), open(sys.argv[1], "w"), sort_keys=False)
PY
  sed -i "s/^version: .*/version: $ver/" "$dir/Chart.yaml"
  helm lint "$dir" >/dev/null
  (cd "$WORK/$c" && helm package "$CHART" >/dev/null)
  (cd "$WORK/$c" && run ngc registry chart push "$REPO:$ver")

  url="$(field "$f" inferenceUrl)"
  health="$(field "$f" healthUri)"
  args=(cloud-function function create --name "nvsnap-$PREFIX-$c"
    --helm-chart "$REPO:$ver" --helm-chart-service "$CHART"
    --inference-url "${url:-/v1/chat/completions}" --inference-port 8000
    --health-protocol HTTP --health-port 8000 --health-uri "${health:-/health}"
    --health-expected-status-code 200 --health-timeout PT60S
    --tag "nvsnap-$PREFIX")
  for s in $(field "$f" secrets); do
    v="${!s:-}"
    [ -n "$v" ] || { echo "case $c needs $s in the environment" >&2; exit 2; }
    args+=(--secret "$s:$v")
  done
  run ngc "${args[@]}"
done
