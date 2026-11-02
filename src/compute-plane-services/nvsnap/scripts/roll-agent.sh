#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# roll-agent.sh: build an agent image and roll it onto a cluster in one step.
#
#   scripts/roll-agent.sh --tag v0.2.76-mv149 --values my-values.yaml [options]
#
# 1. builds and pushes the agent image (scripts/build-agent.sh app), unless
#    --no-build, and checks it has every platform asked for;
# 2. refuses to roll while an instance capture is running (a rollout
#    restarts the agents one by one, and a capture loses the member whose
#    agent restarts), unless --force;
# 3. helm upgrade with the values file and the new agent image tag;
# 4. waits until every agent runs the new image and is available, then
#    reports any agent pod that is not Running.
#
# Options:
#   --tag TAG            agent image tag to build and roll (required)
#   --values FILE        chart values (required); the tag overrides its
#                        agent.image.tag
#   --kubeconfig FILE    default: $KUBECONFIG
#   --release NAME       default: nvsnap
#   --namespace NS       default: nvsnap-system
#   --registry REG       default: the registry the chart renders for the agent
#   --base-version V     agent base image; default: $NVSNAP_BASE_VERSION
#   --platforms LIST     default: linux/amd64,linux/arm64
#   --builder NAME       docker buildx builder; default: multi
#   --no-build           roll an image that is already pushed
#   --force              roll even while a capture runs
#   --timeout DURATION   helm and rollout wait (s, m or h); default: 40m
#   --dry-run            run the checks and print the upgrade, change nothing
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=/dev/null
source "$SCRIPT_DIR/versions.sh"

tag="" values="" release="nvsnap" ns="nvsnap-system" registry=""
base="${NVSNAP_BASE_VERSION}" platforms="linux/amd64,linux/arm64" builder="multi"
build=1 force=0 timeout="40m" dry=0
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) tag="$2"; shift 2 ;;
    --values) values="$2"; shift 2 ;;
    --kubeconfig) export KUBECONFIG="$2"; shift 2 ;;
    --release) release="$2"; shift 2 ;;
    --namespace) ns="$2"; shift 2 ;;
    --registry) registry="$2"; shift 2 ;;
    --base-version) base="$2"; shift 2 ;;
    --platforms) platforms="$2"; shift 2 ;;
    --builder) builder="$2"; shift 2 ;;
    --no-build) build=0; shift ;;
    --force) force=1; shift ;;
    --timeout) timeout="$2"; shift 2 ;;
    --dry-run) dry=1; build=0; shift ;;
    -h|--help) sed -n 4,33p "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
[ -n "$tag" ] && [ -n "$values" ] || { echo "--tag and --values are required" >&2; exit 2; }
[ -f "$values" ] || { echo "values file not found: $values" >&2; exit 2; }
chart="$ROOT/deploy/helm/nvsnap"
if [ ! -d "$chart/charts" ] || [ -z "$(ls -A "$chart/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart" >/dev/null
fi
if [ -z "$registry" ]; then
  # The registry the release pulls the agent from, as the chart renders it.
  registry="$(helm template "$release" "$chart" -n "$ns" -f "$values" 2>/dev/null |
    sed -n 's#^ *image: *"\{0,1\}\([^"]*\)/nvsnap-agent:.*#\1#p' | head -1)"
  [ -n "$registry" ] || { echo "cannot tell the agent's registry from the chart; pass --registry" >&2; exit 2; }
fi
image="$registry/nvsnap-agent:$tag"
case "$timeout" in
  *h) secs=$(( ${timeout%h} * 3600 )) ;; *m) secs=$(( ${timeout%m} * 60 )) ;; *s) secs=${timeout%s} ;;
  *) echo "--timeout takes s, m or h: $timeout" >&2; exit 2 ;;
esac
step() { echo; echo "== $(date -u +%T) $*"; }

# 1. Build and push.
if [ "$build" = 1 ]; then
  step "building $image ($platforms)"
  log="$(mktemp -t roll-agent-build.XXXXXX)"
  if ! PLATFORMS="$platforms" BUILDX_BUILDER="$builder" NVSNAP_REGISTRY="$registry" \
       NVSNAP_BASE_VERSION="$base" NVSNAP_APP_VERSION="$tag" "$SCRIPT_DIR/build-agent.sh" app >"$log" 2>&1; then
    tail -20 "$log"; echo "build failed; full log: $log" >&2; exit 1
  fi
  echo "built; log: $log"
fi
step "checking $image"
have="$(docker buildx imagetools inspect "$image" 2>/dev/null | awk '/Platform:/{print $2}' | sort -u | tr '\n' ' ')" || true
[ -n "$have" ] || { echo "image $image is not in the registry (build it, or drop --no-build)" >&2; exit 1; }
for p in ${platforms//,/ }; do
  case " $have " in *" $p "*) ;; *) echo "image $image lacks $p (has: $have)" >&2; exit 1 ;; esac
done
echo "platforms: $have"

# 2. No rollout during a capture.
step "checking for running captures"
capturing="$(kubectl -n "$ns" get configmaps -o jsonpath='{range .items[*]}{.metadata.name}={.data.state}{"\n"}{end}' |
  awk -F= '$1 ~ /^nvsnap-criu-group-/ && $2 == "capturing" {print $1}')"
if [ -n "$capturing" ]; then
  echo "captures running:"; echo "$capturing" | sed 's/^/  /'
  if [ "$force" = 0 ]; then
    echo "a rollout now would fail them and restart their instances cold; wait, or pass --force" >&2; exit 1
  fi
fi

# A release left pending (an upgrade whose helm lost the cluster midway, e.g.
# an expired login) blocks every later upgrade with "another operation is in
# progress". Say which revision and how to recover rather than fail on it.
status="$(helm -n "$ns" status "$release" 2>/dev/null | sed -n 's/^STATUS: *//p')"
case "$status" in
  pending-*)
    rev="$(helm -n "$ns" history "$release" --max 20 2>/dev/null | awk '$3 == "deployed" {r=$1} END {print r}')"
    echo "release $release is $status (an earlier helm run did not finish)." >&2
    echo "if no other helm run is going on, roll it back to its last deployed revision, then rerun this:" >&2
    echo "  helm -n $ns rollback $release ${rev:-<revision>} --wait --timeout $timeout" >&2
    exit 1 ;;
esac

# 3. Upgrade.
upgrade=(helm upgrade "$release" "$chart" -n "$ns" -f "$values" --set "agent.image.tag=$tag"
  --disable-openapi-validation --timeout "$timeout")
if [ "$dry" = 1 ]; then
  step "dry run: would run"; echo "  ${upgrade[*]}"
  helm template "$release" "$chart" -n "$ns" -f "$values" --set "agent.image.tag=$tag" 2>/dev/null |
    grep -q "image: $image" && echo "  the chart renders the agent as $image" || { echo "the chart does not render $image" >&2; exit 1; }
  exit 0
fi
step "helm upgrade $release to $tag"
"${upgrade[@]}"

# 4. Wait for every agent on the new image.
step "waiting for the agents"
deadline=$(( $(date +%s) + secs ))
last=""
while :; do
  st="$(kubectl -n "$ns" get ds nvsnap-agent -o jsonpath='{.spec.template.spec.containers[0].image} {.status.updatedNumberScheduled}/{.status.numberAvailable}/{.status.desiredNumberScheduled}' 2>/dev/null || true)"
  [ "$st" != "$last" ] && { echo "  $st"; last="$st"; }
  read -r img counts <<<"$st"
  IFS=/ read -r up av want <<<"${counts:-0/0/1}"
  if [ "$img" = "$image" ] && [ "$up" = "$want" ] && [ "$av" = "$want" ]; then break; fi
  [ "$(date +%s)" -gt "$deadline" ] && { echo "agents not all on $tag after $timeout" >&2; exit 1; }
  sleep 10
done
bad="$(kubectl -n "$ns" get pods -l app=nvsnap-agent --no-headers | awk '$3 != "Running"')"
[ -n "$bad" ] && { echo "agent pods not Running:"; echo "$bad"; exit 1; }
step "done: every agent runs $tag"
