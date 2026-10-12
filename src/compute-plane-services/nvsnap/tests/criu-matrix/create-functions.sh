#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Create the CRIU matrix as NVCF functions, Helm and container: for each
# case, the chart with the case's values as its defaults is pushed as its
# own chart version and a Helm function is created from it, so a deployment
# needs no configuration. A case that is one pod of a Deployment also
# becomes a container function running the same image, model and flags.
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
#                      Helm functions are named nvsnap-<PREFIX>-<case>, container
#                      functions nvsnap-<PREFIX>-ctr-<case>
#   MODE               both (default), helm or container
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
MODE="${MODE:-both}"
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

# container <values.yaml> prints, one per line, the container function's
# image, container args and environment (NAME:VALUE), or nothing when the
# case has no container form: a container function is one pod NVCF builds
# itself, so StatefulSets, several pods, a set user and Grove do not apply.
container() {
  python3 - "$1" <<'PY'
import sys, yaml
v = yaml.safe_load(open(sys.argv[1]))
fn = v.get("function") or {}
if v.get("kind") != "Deployment" or int(v.get("podsPerInstance", 1)) != 1 or v.get("runAsUser") is not None or fn.get("container") is False:
    sys.exit(0)
tp = int(v.get("podsPerInstance", 1)) * int(v.get("gpusPerPod", 1))
served = " --served-model-name %s" % v["servedModelName"] if v.get("servedModelName") else ""
extra = " " + v["extraArgs"] if v.get("extraArgs") else ""
eng = v.get("engine", "vllm")
if "containerArgs" in fn:
    args = fn["containerArgs"] or ""
elif eng == "vllm":  # the image's entrypoint is `vllm serve`
    args = "%s%s --host 0.0.0.0 --port %s --max-model-len %s --gpu-memory-utilization %s --tensor-parallel-size %d%s" % (
        v["model"], served, v["port"], v["maxModelLen"], v["gpuMemoryUtilization"], tp, extra)
elif eng == "sglang":
    args = "python3 -m sglang.launch_server --model-path %s%s --host 0.0.0.0 --port %s --context-length %s --mem-fraction-static %s --tp %d%s" % (
        v["model"], served, v["port"], v["maxModelLen"], v["gpuMemoryUtilization"], tp, extra)
else:  # command: the image starts its own server
    args = ""
env = ["HF_HUB_DISABLE_XET:1"] + (["HF_HOME:" + v["hfHome"]] if v.get("hfHome") else [])
env += ["%s:%s" % (e["name"], e["value"]) for e in v.get("extraEnv") or [] if "value" in e]
print(v["image"]); print(args)
for e in env: print(e)
PY
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
  merged="$WORK/$c/values.yaml"
  python3 - "$dir/values.yaml" "$f" "$merged" <<'PY'
import sys, yaml
v = yaml.safe_load(open(sys.argv[1])); v["function"] = (yaml.safe_load(open(sys.argv[2])) or {}).get("function") or {}
yaml.safe_dump(v, open(sys.argv[3], "w"))
PY
  secrets=()
  for s in $(field "$f" secrets); do
    sv="${!s:-}"
    [ -n "$sv" ] || { echo "case $c needs $s in the environment" >&2; exit 2; }
    secrets+=(--secret "$s:$sv")
  done
  url="$(field "$f" inferenceUrl)"
  health="$(field "$f" healthUri)"
  common=(--inference-url "${url:-/v1/chat/completions}" --inference-port 8000
    --health-protocol HTTP --health-port 8000 --health-uri "${health:-/health}"
    --health-expected-status-code 200 --health-timeout PT60S --tag "nvsnap-$PREFIX")

  if [ "$MODE" != helm ]; then
    mapfile -t ctr < <(container "$merged")
    if [ "${#ctr[@]}" -gt 0 ]; then
      cargs=(cloud-function function create --name "nvsnap-$PREFIX-ctr-$c" --container-image "${ctr[0]}" "${common[@]}")
      [ -z "${ctr[1]}" ] || cargs+=("--container-args=${ctr[1]}")
      for e in "${ctr[@]:2}"; do cargs+=(--container-environment-variable "$e"); done
      run ngc "${cargs[@]}" "${secrets[@]}"
    fi
  fi
  [ "$MODE" != container ] || continue
  helm lint "$dir" >/dev/null
  (cd "$WORK/$c" && helm package "$CHART" >/dev/null)
  (cd "$WORK/$c" && run ngc registry chart push "$REPO:$ver")

  run ngc cloud-function function create --name "nvsnap-$PREFIX-$c" \
    --helm-chart "$REPO:$ver" --helm-chart-service "$CHART" "${common[@]}" "${secrets[@]}"
done
