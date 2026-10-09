#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
#
# Copy a k3s cluster's kubeconfig to this workstation, so kubectl, helm and llm.py
# work without ssh. Reads /etc/rancher/k3s/k3s.yaml from the k3s server, points
# it at a name in the server certificate, and renames the k3s "default"
# cluster, user and context to CONTEXT so several clusters can share one
# KUBECONFIG.
#
# Usage:
#   ./fetch-kubeconfig.sh node1 k3s.example.com my-cluster
#   ./fetch-kubeconfig.sh [--output PATH] [--force] [--no-check] SSH_TARGET API_HOST CONTEXT
#
#   SSH_TARGET        ssh target for the k3s server (~/.ssh/config alias or user@host)
#   API_HOST          API server name in the k3s certificate (a --tls-san value)
#   CONTEXT           name for the new context, cluster and user
#   --output PATH     write the kubeconfig here (default: ~/.kube/CONTEXT.yaml)
#   --force           replace an existing file at PATH
#   --no-check        skip the final kubectl get nodes check
#
# Reads the file as the ssh user, or with passwordless sudo when k3s left it
# mode 600. The written file grants cluster-admin and is mode 600.

set -euo pipefail

OUTPUT=""
FORCE=0
CHECK=1
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --output)
      [ $# -ge 2 ] || { echo "--output needs a path" >&2; exit 2; }
      OUTPUT="$2"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --no-check) CHECK=0; shift ;;
    -h|--help) awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    -*) echo "unknown argument: $1" >&2; exit 2 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done

if [ "${#ARGS[@]}" -ne 3 ]; then
  echo "usage: $0 [--output PATH] [--force] [--no-check] SSH_TARGET API_HOST CONTEXT" >&2
  exit 2
fi
SSH_TARGET="${ARGS[0]}"
API_HOST="${ARGS[1]}"
CONTEXT="${ARGS[2]}"

name_re='^[a-z0-9][-a-z0-9]*$'
host_re='^[A-Za-z0-9][-A-Za-z0-9.]*$'
if ! [[ "$CONTEXT" =~ $name_re ]]; then
  echo "CONTEXT must be lowercase letters, digits and dashes: $CONTEXT" >&2
  exit 2
fi
if ! [[ "$API_HOST" =~ $host_re ]]; then
  echo "API_HOST must be a host name or IPv4 address: $API_HOST" >&2
  exit 2
fi
case "$SSH_TARGET" in
  -*|"") echo "SSH_TARGET must be an ssh host: $SSH_TARGET" >&2; exit 2 ;;
esac
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is not installed" >&2; exit 2; }

if [ -z "$OUTPUT" ]; then
  [ -d "$HOME/.kube" ] || mkdir -m 700 "$HOME/.kube"
  OUTPUT="$HOME/.kube/$CONTEXT.yaml"
fi
case "$OUTPUT" in
  /*) ;;
  *) OUTPUT="$PWD/$OUTPUT" ;;
esac
OUT_DIR=$(dirname "$OUTPUT")
[ -d "$OUT_DIR" ] || { echo "directory does not exist: $OUT_DIR" >&2; exit 2; }
if [ -e "$OUTPUT" ] && [ "$FORCE" -eq 0 ]; then
  echo "$OUTPUT already exists; pass --force to replace it" >&2
  exit 2
fi

# Temp files sit next to the output so the final mv is an atomic rename.
umask 077
FETCHED=$(mktemp "$OUT_DIR/.$CONTEXT.yaml.fetched.XXXXXX")
RENAMED=$(mktemp "$OUT_DIR/.$CONTEXT.yaml.XXXXXX")
# shellcheck disable=SC2329  # invoked by the EXIT trap
cleanup() { rm -f "$FETCHED" "$RENAMED" "$RENAMED.lock"; }
trap cleanup EXIT

if ! ssh "$SSH_TARGET" 'cat /etc/rancher/k3s/k3s.yaml 2>/dev/null || sudo -n cat /etc/rancher/k3s/k3s.yaml' > "$FETCHED"; then
  echo "could not read /etc/rancher/k3s/k3s.yaml on $SSH_TARGET" >&2
  echo "check ssh access, that $SSH_TARGET is a k3s server, not an agent, and that" >&2
  echo "your ssh user can read the file or run sudo without a password" >&2
  exit 2
fi

# A k3s server writes one cluster, user and context, all named "default",
# with the API at 127.0.0.1. Anything else is not the file this script expects.
if [ "$(grep -cE '^[[:space:]]+server: ' "$FETCHED" || true)" != 1 ] \
    || ! grep -qE '^[[:space:]]+server: https://127\.0\.0\.1:6443$' "$FETCHED" \
    || ! grep -qx 'current-context: default' "$FETCHED"; then
  echo "$SSH_TARGET:/etc/rancher/k3s/k3s.yaml does not look like a k3s server kubeconfig" >&2
  exit 2
fi

# kubectl cannot rename clusters or users, so rename them on the k3s layout,
# then let kubectl rename the context and set the server.
sed -e "/^clusters:/,/^contexts:/s/^  name: default\$/  name: $CONTEXT/" \
    -e "/^contexts:/,/^current-context:/s/^    cluster: default\$/    cluster: $CONTEXT/" \
    -e "/^contexts:/,/^current-context:/s/^    user: default\$/    user: $CONTEXT/" \
    -e "/^users:/,\$s/^- name: default\$/- name: $CONTEXT/" \
    "$FETCHED" > "$RENAMED"
kubectl --kubeconfig "$RENAMED" config rename-context default "$CONTEXT" >/dev/null
kubectl --kubeconfig "$RENAMED" config set-cluster "$CONTEXT" --server="https://$API_HOST:6443" >/dev/null

view() { kubectl --kubeconfig "$RENAMED" config view -o jsonpath="$1"; }
if [ "$(view '{.contexts[*].name}')" != "$CONTEXT" ] \
    || [ "$(view '{.current-context}')" != "$CONTEXT" ] \
    || [ "$(view "{.contexts[?(@.name==\"$CONTEXT\")].context.cluster}")" != "$CONTEXT" ] \
    || [ "$(view "{.contexts[?(@.name==\"$CONTEXT\")].context.user}")" != "$CONTEXT" ] \
    || [ "$(view '{.clusters[*].name}')" != "$CONTEXT" ] \
    || [ "$(view '{.users[*].name}')" != "$CONTEXT" ] \
    || [ "$(view "{.clusters[?(@.name==\"$CONTEXT\")].cluster.server}")" != "https://$API_HOST:6443" ]; then
  echo "could not rename the k3s kubeconfig to $CONTEXT; the file layout was not as expected" >&2
  exit 2
fi

chmod 600 "$RENAMED"
mv -f "$RENAMED" "$OUTPUT"
echo "Wrote $OUTPUT (context $CONTEXT, server https://$API_HOST:6443)."

status=0
if [ "$CHECK" -eq 1 ]; then
  if nodes=$(kubectl --kubeconfig "$OUTPUT" --context "$CONTEXT" --request-timeout=15s get nodes 2>&1); then
    echo "$nodes"
  else
    echo "kubectl get nodes failed:" >&2
    echo "$nodes" | tail -n 3 >&2
    echo "check that this workstation reaches $API_HOST on 6443/tcp. A certificate error means" >&2
    echo "$API_HOST is not a --tls-san name or address of the server." >&2
    status=1
  fi
fi

echo
echo "To use it:"
# shellcheck disable=SC2016  # print $HOME literally for the reader's shell
printf '  export KUBECONFIG="$HOME/.kube/config:%s"\n' "$OUTPUT"
echo "  kubectl config use-context $CONTEXT"
echo "The file grants cluster-admin. Keep it mode 600 and do not share it."
exit "$status"
