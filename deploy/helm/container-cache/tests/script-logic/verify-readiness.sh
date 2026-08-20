#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Behavioural tests for the DaemonSet readiness helpers.
#
# The helpers live inside the Helm-templated bash script in
# templates/daemonset.yaml, so this test renders the chart, lifts the shipped
# function definitions out of the rendered script, and runs them for real
# against fixture files. Only the two path roots are rewritten (/proc and
# /host point at a temporary directory); the logic under test is the exact
# text that ships in the DaemonSet.
#
# Run from the chart subtree:
#   bash tests/script-logic/verify-readiness.sh
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/deploy"
FAKE="$(mktemp -d)"
trap 'rm -rf "${FAKE}"' EXIT

RENDERED="${FAKE}/rendered.yaml"
HELPERS="${FAKE}/helpers.sh"

helm template container-cache "${CHART_DIR}" > "${RENDERED}"

# Lift the helper block: from the first helper definition through the closing
# brace of reconcile_ready_marker. None of those functions contain a line that
# is a lone closing brace, so the first one after reconcile_ready_marker's
# header is its end.
awk '
  /proc_start_epoch\(\) \{/ { inblk = 1 }
  inblk { print }
  /reconcile_ready_marker\(\) \{/ { inrec = 1 }
  inrec && /^[[:space:]]*\}[[:space:]]*$/ { exit }
' "${RENDERED}" \
  | sed -e "s#/proc#${FAKE}/proc#g" -e "s#/host#${FAKE}/host#g" \
  > "${HELPERS}"

grep -q 'containerd_config_active() {' "${HELPERS}" \
  || { echo "FAILED: could not extract the readiness helpers from the render" >&2; exit 1; }
grep -q 'reconcile_ready_marker() {' "${HELPERS}" \
  || { echo "FAILED: reconcile_ready_marker missing from the extracted block" >&2; exit 1; }

BTIME=1000000
HZ=100

mkdir -p "${FAKE}/proc" "${FAKE}/host/etc/containerd" \
  "${FAKE}/host/etc/containers/registries.conf.d"
printf 'btime %s\n' "${BTIME}" > "${FAKE}/proc/stat"

# Synthesise /proc/<pid>/stat. Field 22 (starttime) is the 20th field after
# pid and comm, so pad with 18 fillers between the state field and it.
write_proc_stat() { # pid comm start_epoch
  local pid="$1" comm="$2" start_epoch="$3" ticks fillers=""
  ticks=$(( (start_epoch - BTIME) * HZ ))
  local i
  for (( i = 0; i < 18; i++ )); do fillers+="0 "; done
  mkdir -p "${FAKE}/proc/${pid}"
  printf '%s (%s) S %s%s 0 0 0\n' "${pid}" "${comm}" "${fillers}" "${ticks}" \
    > "${FAKE}/proc/${pid}/stat"
}

set_mtime() { touch -d "@$2" "$1"; }

# Stubs for the two host lookups the helpers make. Shell functions take
# precedence over PATH, so the shipped `pgrep -x <name>` calls land here.
CONTAINERD_PID=""
CRIO_PID=""
pgrep() {
  case "$2" in
    containerd) [ -n "${CONTAINERD_PID}" ] && echo "${CONTAINERD_PID}" ;;
    crio)       [ -n "${CRIO_PID}" ] && echo "${CRIO_PID}" ;;
    *)          return 1 ;;
  esac
}
getconf() { echo "${HZ}"; }

READY_MARKER="${FAKE}/ready"
CRIO_RELOAD_MARKER="${FAKE}/crio-reloaded"

# shellcheck source=/dev/null
source "${HELPERS}"

fail() { echo "FAILED: $*" >&2; exit 1; }
assert_ready() { [ -f "${READY_MARKER}" ] || fail "$1: expected the ready marker"; }
assert_not_ready() { [ ! -f "${READY_MARKER}" ] || fail "$1: expected no ready marker"; }

echo "0. proc_start_epoch reconstructs an absolute start time"
write_proc_stat 4242 containerd 1000123
got="$(proc_start_epoch 4242)"
[ "${got}" = "1000123" ] || fail "proc_start_epoch returned ${got}, expected 1000123"
proc_start_epoch 999999 >/dev/null 2>&1 && fail "proc_start_epoch must fail on an unknown pid"

CONTAINERD="${FAKE}/host/etc/containerd/config.toml"
CRIO_DROPIN="${FAKE}/host/etc/containers/registries.conf.d/nvcf-container-cache.conf"

echo "1. pod recreated before containerd restarted: not ready"
# update_config.py is idempotent, so a hash diff sees no change on this run and
# would report ready. The running containerd still predates the config, so the
# node is genuinely still bypassing the cache.
: > "${CONTAINERD}"
set_mtime "${CONTAINERD}" 1000200
CONTAINERD_PID=100
write_proc_stat 100 containerd 1000100
touch "${READY_MARKER}"   # stale marker from an earlier state must be cleared
reconcile_ready_marker
assert_not_ready "pod recreated before containerd restart"

echo "2. containerd restarts later: flips to ready with no pod bounce"
# Same running script, same loop: only the runtime state changed.
write_proc_stat 101 containerd 1000300
CONTAINERD_PID=101
reconcile_ready_marker
assert_ready "delayed containerd restart"

echo "3. containerd config rewritten again after that restart: back to not ready"
set_mtime "${CONTAINERD}" 1000400
reconcile_ready_marker
assert_not_ready "config rewritten after the restart"

echo "4. no containerd process: a leftover config.toml does not hold the node"
CONTAINERD_PID=""
reconcile_ready_marker
assert_ready "CRI-O-only node with a leftover config.toml"

echo "5. failed CRI-O reload: not ready"
rm -f "${CONTAINERD}"
CONTAINERD_PID=""
: > "${CRIO_DROPIN}"
set_mtime "${CRIO_DROPIN}" 1000200
CRIO_PID=200
write_proc_stat 200 crio 1000100   # CRI-O predates the drop-in
rm -f "${CRIO_RELOAD_MARKER}"      # SIGHUP failed, so nothing was stamped
reconcile_ready_marker
assert_not_ready "failed CRI-O SIGHUP"

echo "6. successful CRI-O reload: ready without a restart"
touch "${CRIO_RELOAD_MARKER}"
set_mtime "${CRIO_RELOAD_MARKER}" 1000250
reconcile_ready_marker
assert_ready "successful CRI-O SIGHUP"

echo "7. CRI-O restarted after the drop-in was written: ready without a reload"
rm -f "${CRIO_RELOAD_MARKER}"
write_proc_stat 201 crio 1000300
CRIO_PID=201
reconcile_ready_marker
assert_ready "CRI-O restarted after the drop-in"

echo "8. both runtimes present: the inactive one wins"
: > "${CONTAINERD}"
set_mtime "${CONTAINERD}" 1000500
CONTAINERD_PID=100   # started at 1000100, before the config
reconcile_ready_marker
assert_not_ready "containerd inactive while CRI-O is active"

echo "Readiness logic checks passed."
