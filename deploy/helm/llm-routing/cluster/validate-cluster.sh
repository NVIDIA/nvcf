#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
#
# Validate a k3s GPU cluster: node health, GPU scheduling, and real CUDA
# execution on every node. Creates test pods and deletes them on exit.
#
# Usage:
#   ./validate-cluster.sh --context my-cluster   # from a workstation with a kubeconfig
#   ./validate-cluster.sh --on node1             # copy to a k3s server over ssh and run there
#   ./validate-cluster.sh --on node1 --quick
#
#   --context NAME    kubeconfig context to use (default: the current context)
#   --on HOST         ssh target; the script copies itself there and re-execs
#   --expect-nodes N  fail unless the cluster has exactly N nodes
#   --quick           skip the CUDA compile/run check
#   --namespace NS    run test pods in NS (default: default)
#   --base-image IMG  image for the nvidia-smi checks
#   --devel-image IMG image for the CUDA compile (needs nvcc)
#
# Exit code is 0 only if every check passed.

set -euo pipefail

NAMESPACE="default"
QUICK=0
REMOTE=""
EXPECT_NODES=""
CONTEXT=""
BASE_IMAGE="${VALIDATE_BASE_IMAGE:-nvcr.io/nvidia/cuda:12.6.2-base-ubuntu24.04}"
DEVEL_IMAGE="${VALIDATE_DEVEL_IMAGE:-nvcr.io/nvidia/cuda:12.6.2-devel-ubuntu24.04}"
POD_PREFIX="gpu-validate"
# The CUDA devel image is several GB. A node that has not cached it can spend
# minutes in ContainerCreating, so this has to tolerate a cold pull.
TIMEOUT=900

FORWARD=()
while [ $# -gt 0 ]; do
  case "$1" in
    --on) REMOTE="$2"; shift 2 ;;
    --context) CONTEXT="$2"; shift 2 ;;
    --expect-nodes) EXPECT_NODES="$2"; FORWARD+=("$1" "$2"); shift 2 ;;
    --quick) QUICK=1; FORWARD+=("$1"); shift ;;
    --namespace) NAMESPACE="$2"; FORWARD+=("$1" "$2"); shift 2 ;;
    --base-image) BASE_IMAGE="$2"; FORWARD+=("$1" "$2"); shift 2 ;;
    --devel-image) DEVEL_IMAGE="$2"; FORWARD+=("$1" "$2"); shift 2 ;;
    -h|--help) awk 'NR>1 && /^# SPDX-/ {next} NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# --on: ship this script to the node and run it there. Everything downstream
# then runs locally on a box that already has k3s, so there is no ssh quoting
# or stdin-in-loop hazard in the checks themselves.
if [ -n "$REMOTE" ]; then
  [ -z "$CONTEXT" ] || { echo "--context and --on cannot be combined; --on uses the k3s kubeconfig on HOST" >&2; exit 2; }
  remote_path=$(ssh -n "$REMOTE" 'mktemp /tmp/gpu-validate.XXXXXX' 2>/dev/null || true)
  if ! [[ "$remote_path" =~ ^/tmp/gpu-validate\.[A-Za-z0-9]+$ ]]; then
    echo "could not create a temp file on $REMOTE. Check ssh access." >&2
    exit 2
  fi
  if ! scp -q "$0" "$REMOTE:$remote_path"; then
    ssh -n "$REMOTE" rm -f "$remote_path" || true
    echo "could not scp to $REMOTE. Check ssh access." >&2
    exit 2
  fi
  args=""
  for a in ${FORWARD+"${FORWARD[@]}"}; do args="$args $(printf '%q' "$a")"; done
  # -t only when we have a terminal, otherwise ssh warns and colours are pointless
  SSH_OPTS=()
  [ -t 1 ] && SSH_OPTS+=(-t)
  # shellcheck disable=SC2029  # deliberate local expansion: path and args are built here
  ssh ${SSH_OPTS+"${SSH_OPTS[@]}"} "$REMOTE" "chmod +x $remote_path && $remote_path$args; rc=\$?; rm -f $remote_path; exit \$rc"
  exit $?
fi

# k3s is usually installed with --write-kubeconfig-mode 644, which makes the
# unprivileged path work. It isn't guaranteed, and a non-interactive ssh shell
# doesn't source the profile that might set KUBECONFIG, so try each candidate
# and keep the first that can actually reach the API. sudo is -n so a node
# without passwordless sudo fails fast instead of hanging on a prompt.
if [ -z "${KUBECONFIG:-}" ] && [ -r /etc/rancher/k3s/k3s.yaml ]; then
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
fi

CANDIDATES=()
if [ -n "$CONTEXT" ]; then
  [[ "$CONTEXT" =~ ^[A-Za-z0-9][-A-Za-z0-9_.@:]*$ ]] || { echo "invalid --context: $CONTEXT" >&2; exit 2; }
  command -v kubectl >/dev/null 2>&1 && CANDIDATES+=("kubectl --context $CONTEXT")
else
  command -v kubectl >/dev/null 2>&1 && CANDIDATES+=("kubectl")
  command -v k3s >/dev/null 2>&1 && CANDIDATES+=("k3s kubectl")
  command -v k3s >/dev/null 2>&1 && CANDIDATES+=("sudo -n k3s kubectl")
  command -v kubectl >/dev/null 2>&1 && CANDIDATES+=("sudo -n kubectl")
fi

KUBECTL=""
for c in ${CANDIDATES+"${CANDIDATES[@]}"}; do
  if $c get nodes >/dev/null 2>&1; then
    KUBECTL="$c"
    break
  fi
done

if [ -z "$KUBECTL" ]; then
  echo "cannot reach a cluster with any of: ${CANDIDATES[*]:-<none found>}" >&2
  echo "on a node, check: systemctl is-active k3s; ls -l /etc/rancher/k3s/k3s.yaml" >&2
  exit 2
fi

PASS=0
FAIL=0
SKIP=0

pass() { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL + 1)); }
skip() { printf '  \033[33mSKIP\033[0m  %s\n' "$1"; SKIP=$((SKIP + 1)); }
head2() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  $KUBECTL -n "$NAMESPACE" delete pod -l app="$POD_PREFIX" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Explain why a pod never reached Succeeded. A bare timeout with no logs is
# almost always a slow image pull, and saying so beats making the reader guess.
why_stuck() {
  local pod="$1" phase reason msg node
  phase=$($KUBECTL -n "$NAMESPACE" get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null || echo "?")
  node=$($KUBECTL -n "$NAMESPACE" get pod "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "?")
  reason=$($KUBECTL -n "$NAMESPACE" get pod "$pod" \
    -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || true)
  [ -n "$reason" ] || reason=$($KUBECTL -n "$NAMESPACE" get pod "$pod" \
    -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null || true)
  msg="phase=$phase"
  [ -n "$reason" ] && msg="$msg reason=$reason"
  [ -n "$node" ] && msg="$msg node=$node"
  case "$reason" in
    ContainerCreating|PodInitializing|ImagePullBackOff|ErrImagePull)
      msg="$msg. Likely still pulling the image; re-run, or pre-pull it on that node." ;;
  esac
  echo "$msg"
}

# Wait for a pod to reach Succeeded, or fail out on Failed / timeout.
wait_for_pod() {
  local pod="$1" deadline=$((SECONDS + TIMEOUT)) phase
  while [ $SECONDS -lt $deadline ]; do
    phase=$($KUBECTL -n "$NAMESPACE" get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    case "$phase" in
      Succeeded) return 0 ;;
      Failed) return 1 ;;
    esac
    sleep 3
  done
  return 1
}

head2 "Cluster"

NODES=$($KUBECTL get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
NODE_COUNT=$(echo "$NODES" | grep -c . || true)
pass "reachable via '$KUBECTL', $NODE_COUNT node(s)"

if [ -n "$EXPECT_NODES" ]; then
  if [ "$NODE_COUNT" -eq "$EXPECT_NODES" ]; then
    pass "node count is $EXPECT_NODES as expected"
  else
    fail "expected $EXPECT_NODES node(s), found $NODE_COUNT. A node may have dropped out of the cluster."
  fi
fi

NOT_READY=$($KUBECTL get nodes \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' \
  | awk '$2 != "True" {print $1}')
if [ -z "$NOT_READY" ]; then
  pass "all nodes Ready"
else
  fail "nodes not Ready: $(echo "$NOT_READY" | tr '\n' ' ')"
fi

head2 "GPU plumbing"

if $KUBECTL get runtimeclass nvidia >/dev/null 2>&1; then
  pass "RuntimeClass 'nvidia' exists"
else
  fail "RuntimeClass 'nvidia' missing. k3s did not detect nvidia-container-runtime at startup."
fi

PLUGIN_NODES=$($KUBECTL -n kube-system get pods -l name=nvidia-device-plugin-ds \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{" "}{.status.phase}{"\n"}{end}' 2>/dev/null || true)
PLUGIN_RUNNING=$(echo "$PLUGIN_NODES" | awk '$2 == "Running" {print $1}' | grep -c . || true)
if [ "$PLUGIN_RUNNING" -eq "$NODE_COUNT" ] && [ "$NODE_COUNT" -gt 0 ]; then
  pass "device plugin Running on all $NODE_COUNT node(s)"
else
  fail "device plugin Running on $PLUGIN_RUNNING of $NODE_COUNT node(s)"
fi

MISSING_CAPACITY=""
for n in $NODES; do
  cap=$($KUBECTL get node "$n" -o jsonpath='{.status.capacity.nvidia\.com/gpu}' 2>/dev/null || echo "")
  [ -n "$cap" ] && [ "$cap" != "0" ] || MISSING_CAPACITY="$MISSING_CAPACITY $n"
done
if [ -z "$MISSING_CAPACITY" ]; then
  pass "every node advertises nvidia.com/gpu"
else
  fail "no GPU capacity on:$MISSING_CAPACITY"
fi

head2 "GPU visible in a scheduled pod"

UUIDS=""
for n in $NODES; do
  pod="${POD_PREFIX}-smi-$(echo "$n" | tr -cd 'a-z0-9-')"
  $KUBECTL -n "$NAMESPACE" delete pod "$pod" --ignore-not-found >/dev/null 2>&1 || true
  cat <<EOF | $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  labels:
    app: $POD_PREFIX
spec:
  restartPolicy: Never
  nodeName: $n
  runtimeClassName: nvidia
  containers:
    - name: smi
      image: $BASE_IMAGE
      command: ["nvidia-smi", "-L"]
      resources:
        limits:
          nvidia.com/gpu: 1
EOF
  if wait_for_pod "$pod"; then
    out=$($KUBECTL -n "$NAMESPACE" logs "$pod" 2>/dev/null | head -1)
    uuid=$(echo "$out" | sed -n 's/.*UUID: \([^)]*\)).*/\1/p')
    if [ -n "$uuid" ]; then
      pass "$n: ${out}"
      UUIDS="$UUIDS$uuid\n"
    else
      fail "$n: no GPU UUID in output: ${out:-<empty>}"
    fi
  else
    fail "$n: pod did not complete: $(why_stuck "$pod")"
  fi
done

UNIQ=$(printf "%b" "$UUIDS" | grep -c . || true)
DISTINCT=$(printf "%b" "$UUIDS" | sort -u | grep -c . || true)
if [ "$UNIQ" -gt 0 ] && [ "$UNIQ" -eq "$DISTINCT" ]; then
  pass "all $DISTINCT GPU UUID(s) distinct; each pod saw its own node's GPU"
elif [ "$UNIQ" -gt 0 ]; then
  fail "$UNIQ pod(s) reported only $DISTINCT distinct UUID(s)"
fi

head2 "Scheduler accounting"

pod="${POD_PREFIX}-overcommit"
$KUBECTL -n "$NAMESPACE" delete pod "$pod" --ignore-not-found >/dev/null 2>&1 || true
cat <<EOF | $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  labels:
    app: $POD_PREFIX
spec:
  restartPolicy: Never
  runtimeClassName: nvidia
  containers:
    - name: nope
      image: $BASE_IMAGE
      command: ["true"]
      resources:
        limits:
          nvidia.com/gpu: 99
EOF
sleep 10
# Pending alone also covers a scheduled pod still pulling its image, so ask
# the scheduler why the pod is not placed.
reason=$($KUBECTL -n "$NAMESPACE" get pod "$pod" \
  -o jsonpath='{.status.conditions[?(@.type=="PodScheduled")].reason}' 2>/dev/null || echo "")
if [ "$reason" = "Unschedulable" ]; then
  pass "request for 99 GPUs is Unschedulable; capacity is enforced, not advisory"
else
  fail "request for 99 GPUs was not rejected by the scheduler (PodScheduled reason '${reason:-none}'). GPU limits are not being enforced."
fi

head2 "CUDA execution"

if [ "$QUICK" -eq 1 ]; then
  skip "--quick given; GPU compute not exercised"
else
  pod="${POD_PREFIX}-compute"
  $KUBECTL -n "$NAMESPACE" delete pod "$pod" --ignore-not-found >/dev/null 2>&1 || true
  cat <<EOF | $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  labels:
    app: $POD_PREFIX
spec:
  restartPolicy: Never
  runtimeClassName: nvidia
  containers:
    - name: compute
      image: $DEVEL_IMAGE
      command: ["/bin/bash", "-c"]
      args:
        - |
          set -e
          cat > /tmp/t.cu <<'CUDA'
          #include <cstdio>
          __global__ void saxpy(int n, float a, float *x, float *y) {
            int i = blockIdx.x * blockDim.x + threadIdx.x;
            if (i < n) y[i] = a * x[i] + y[i];
          }
          int main() {
            const int N = 1 << 20;
            float *x, *y;
            if (cudaMallocManaged(&x, N * sizeof(float)) != cudaSuccess) { printf("ALLOC FAILED\n"); return 1; }
            if (cudaMallocManaged(&y, N * sizeof(float)) != cudaSuccess) { printf("ALLOC FAILED\n"); return 1; }
            for (int i = 0; i < N; i++) { x[i] = 1.0f; y[i] = 2.0f; }
            saxpy<<<(N + 255) / 256, 256>>>(N, 3.0f, x, y);
            if (cudaDeviceSynchronize() != cudaSuccess) { printf("LAUNCH FAILED\n"); return 1; }
            double err = 0.0;
            for (int i = 0; i < N; i++) err += (y[i] - 5.0f) * (y[i] - 5.0f);
            cudaFree(x); cudaFree(y);
            if (err != 0.0) { printf("WRONG RESULT err=%f\n", err); return 1; }
            printf("CUDA OK: saxpy over %d elements, exact\n", N);
            return 0;
          }
          CUDA
          nvcc -o /tmp/t /tmp/t.cu
          /tmp/t
      resources:
        limits:
          nvidia.com/gpu: 1
EOF
  if wait_for_pod "$pod"; then
    out=$($KUBECTL -n "$NAMESPACE" logs "$pod" 2>/dev/null | tail -1)
    case "$out" in
      "CUDA OK"*) pass "$out" ;;
      *) fail "compute pod finished but output unexpected: ${out:-<empty>}" ;;
    esac
  else
    out=$($KUBECTL -n "$NAMESPACE" logs "$pod" 2>/dev/null | tail -3 || true)
    if [ -n "$out" ]; then
      fail "CUDA compute failed: $out"
    else
      fail "CUDA compute did not run: $(why_stuck "$pod")"
    fi
  fi
fi

printf '\n\033[1mSummary\033[0m  %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
