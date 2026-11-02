#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Mount an nvstor-nfs-export over all of the pod's RDMA rails from inside a pod.
#
#   rails-mount.sh sidecar          mount, report ready, wait for SIGTERM, unmount
#   rails-mount.sh check            probe: exit 0 only while the sidecar's mount is up
#   rails-mount.sh exec -- CMD...   mount, then exec CMD in this container (less safe)
#
# sidecar (the default deployment, replica-pod.yaml) runs as a privileged
# native sidecar (an initContainer with restartPolicy: Always). It mounts at
# $NVSTOR_MOUNT_ROOT/$NVSTOR_POD_ID/data on the host through a hostPath with
# mountPropagation: Bidirectional, so the unprivileged workload container sees
# the mount through the same hostPath with HostToContainer. Once all rails
# carry a transport it creates $NVSTOR_READY_FILE (the startup probe runs
# `rails-mount.sh check`), then waits. On SIGTERM or SIGINT, and on any
# failure after the first mount, it unmounts: the mount is on the host, so it
# would otherwise outlive the pod and keep the pod's network namespace (and its
# rail VFs' RDMA resources) referenced by the kernel NFS client.
#
# At startup the sidecar also unmounts what a crashed earlier run left behind:
# its own per-pod directory (a restart of this sidecar in the same pod), and
# the directory of every pod whose kubelet directory
# ($NVSTOR_KUBELET_PODS_DIR/<uid>) no longer exists (a pod that was killed
# without a chance to unmount, then deleted).
#
# exec mounts in this container's own mount namespace (no propagation), then
# replaces itself with CMD. The mount disappears with the container, but CMD
# keeps this container's privileges and the host's / at /host. Use it only for
# a trusted, short-lived loader, never for a network-facing server.
#
# The container holds the rail VFs through its pod (Multus NAD +
# nvidia.com/mlnxnics on any container of the pod; all share one network
# namespace), has CAP_SYS_ADMIN and CAP_NET_ADMIN (privileged), the host's /
# at $NVSTOR_HOST, and $NVSTOR_MOUNT_ROOT at $NVSTOR_HOST$NVSTOR_MOUNT_ROOT
# (writable hostPath). Host tools run through chroot $NVSTOR_HOST: mount.nfs
# and the rpcrdma module come from the node (installed there with nfs-common or
# nfs-utils, and the OFED NFS/RDMA package on OFED hosts). The kernel NFS client connects from the network namespace
# of the process that mounts, so mounting here puts the transports on the
# pod's rail VFs, not the node's front-end NIC.
#
# Environment:
#   NVSTOR_SOURCE_ADDRS       required: the source's rail IPv6 addresses, one per
#                             rail, separated by spaces (the "nvstor-rail" lines
#                             in the source pod's log)
#   NVSTOR_EXPORT_PATH        path inside the export to mount (default /)
#   NVSTOR_POD_ID            per-pod directory name under NVSTOR_MOUNT_ROOT
#                             (default: the hostname; replica-pod.yaml sets the
#                             pod UID)
#   NVSTOR_MOUNT_ROOT         host directory for mount points
#                             (default /var/lib/kubelet/nvstor-nfs-rail)
#   NVSTOR_HOST               where the host's / is mounted (default /host; "/"
#                             runs host tools directly, without chroot)
#   NVSTOR_RDMA_PORT          NFS/RDMA port (default 20049)
#   NVSTOR_MOUNT_OPTS         mount options added after the safe defaults
#                             (ro,soft,softerr,timeo=100,retrans=2), e.g.
#                             timeo=50,retrans=1. It cannot turn off ro or soft.
#   NVSTOR_MOUNT_UNSAFE_OPTS options that may override ro and soft (rw, hard).
#                             A hard mount hangs readers in D state, unkillable,
#                             for as long as the source or a rail is down; rw
#                             lets the workload write to the source's disk if
#                             the export runs with --read-write. Leave unset
#                             unless you accept both.
#   NVSTOR_RAIL_WAIT          seconds to wait for every rail's routable address
#                             (default 60)
#   NVSTOR_XPRT_WAIT          seconds to wait for every trunked transport
#                             (default 15)
#   NVSTOR_TRUNKING           how the other rails join the mount:
#                             discover  one mount with trunkdiscovery; the
#                                       client reads the source's rail
#                                       addresses (fs_locations, set by
#                                       nvstor-nfs-export --trunk-addr) and
#                                       adds one transport per rail
#                             mounts    one extra mount per rail; each adds
#                                       two transports, so N rails make 2N-1
#                             default: discover on kernels >= 5.18 (which
#                             have trunkdiscovery), mounts otherwise
#   NVSTOR_READY_FILE         sidecar ready marker
#                             (default /tmp/nvstor-rails-mount.ready)
#   NVSTOR_KUBELET_PODS_DIR   host directory with one subdirectory per pod UID
#                             (default /var/lib/kubelet/pods)
#   NVSTOR_LINK               exec mode: symlink to the mounted path
#                             (default /models)
set -euo pipefail

log() { echo "rails-mount: $*" >&2; }
die() { log "error: $*"; exit 1; }
usage() {
  log "usage: rails-mount.sh sidecar | check | exec -- COMMAND [ARGS...]"
  exit 2
}
trap 'log "error: line $LINENO: \"$BASH_COMMAND\" failed (exit $?)"' ERR

MODE=${1:-}
[ $# -gt 0 ] && shift
case "$MODE" in
  sidecar|check) [ $# -eq 0 ] || usage ;;
  exec) [ "${1:-}" = "--" ] && shift; [ $# -gt 0 ] || usage ;;
  *) usage ;;
esac

HOST=${NVSTOR_HOST:-/host}
ROOT=${NVSTOR_MOUNT_ROOT:-/var/lib/kubelet/nvstor-nfs-rail}
POD_ID=${NVSTOR_POD_ID:-$(cat /proc/sys/kernel/hostname)}
READY=${NVSTOR_READY_FILE:-/tmp/nvstor-rails-mount.ready}
PODS_DIR=${NVSTOR_KUBELET_PODS_DIR:-/var/lib/kubelet/pods}
# Overridable only so the script can be tested without root (test-rails-mount.sh).
MOUNTS=${NVSTOR_PROC_MOUNTS:-/proc/self/mounts}
MOUNTSTATS=${NVSTOR_PROC_MOUNTSTATS:-/proc/self/mountstats}

# Host paths are used two ways: as seen by the host tools (chroot), and as
# seen in this container ($HP prefix).
if [ "$HOST" = / ]; then HP="" H=(); else HP=$HOST H=(chroot "$HOST"); fi

case "$HOST" in
  /*) ;;
  *) die "NVSTOR_HOST must be an absolute path, got '$HOST'" ;;
esac
case "$ROOT" in
  /*) ;;
  *) die "NVSTOR_MOUNT_ROOT must be an absolute path, got '$ROOT'" ;;
esac
[ "$ROOT" != / ] || die "NVSTOR_MOUNT_ROOT must not be /"
# POD_ID names a directory that gets unmounted recursively and removed.
[[ $POD_ID =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] && [ "${#POD_ID}" -le 253 ] \
  || die "NVSTOR_POD_ID '$POD_ID' is not a valid directory name ([A-Za-z0-9._-], at most 253)"
BASE=$ROOT/$POD_ID  # host-relative

# --- check: the sidecar's startup and readiness probe -----------------------
if [ "$MODE" = check ]; then
  trap - ERR
  [ -f "$READY" ] || { log "not ready: no $READY"; exit 1; }
  awk -v m="$HP$BASE/data" '$2 == m && $3 == "nfs4" { f = 1 } END { exit !f }' "$MOUNTS" \
    || { log "not ready: $HP$BASE/data is not mounted"; exit 1; }
  exit 0
fi

# --- validate the environment before touching anything ---------------------
RAW_ADDRS=${NVSTOR_SOURCE_ADDRS:-}
[ -n "${RAW_ADDRS//[[:space:]]/}" ] \
  || die "NVSTOR_SOURCE_ADDRS is empty: set it to the source's rail addresses, space separated (kubectl logs nvstor-source | awk '/^nvstor-rail/ {print \$3}')"
case "$RAW_ADDRS" in
  *,*) die "NVSTOR_SOURCE_ADDRS must be separated by spaces, not commas: '$RAW_ADDRS'" ;;
esac
read -r -d '' -a ADDRS < <(printf '%s' "$RAW_ADDRS") || true
is_ipv6() { # address: hex groups and colons, at most one ::, 8 groups if no ::
  local a=$1 groups
  [[ $a =~ ^[0-9A-Fa-f:]+$ ]] || return 1
  [[ $a == *:* ]] || return 1
  [[ $a != *:::* ]] || return 1
  local rest=${a//::/}
  [ $(( (${#a} - ${#rest}) / 2 )) -le 1 ] || return 1
  [[ $a =~ (^|:)[0-9A-Fa-f]{5,} ]] && return 1
  [[ $a =~ ^:[^:] || $a =~ [^:]:$ ]] && return 1
  IFS=: read -r -a groups <<<"${a//::/:x:}"
  if [[ $a == *::* ]]; then [ "${#groups[@]}" -le 8 ]; else [ "${#groups[@]}" -eq 8 ]; fi
}
declare -A SEEN_ADDR
for a in "${ADDRS[@]}"; do
  is_ipv6 "$a" || die "NVSTOR_SOURCE_ADDRS: '$a' is not an IPv6 address (the rails' routable addresses are IPv6; no brackets, prefix length or zone)"
  [ -z "${SEEN_ADDR[$a]:-}" ] || die "NVSTOR_SOURCE_ADDRS lists $a twice"
  SEEN_ADDR[$a]=1
done
[ "${#ADDRS[@]}" -le 16 ] || die "NVSTOR_SOURCE_ADDRS has ${#ADDRS[@]} addresses; the NFS client trunks at most 16 transports"

is_uint() { [[ $1 =~ ^[0-9]+$ ]] && [ "${#1}" -le 6 ]; }
PORT=${NVSTOR_RDMA_PORT:-20049}
is_uint "$PORT" && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "NVSTOR_RDMA_PORT '$PORT' is not a port in 1..65535"
RAIL_WAIT=${NVSTOR_RAIL_WAIT:-60}
is_uint "$RAIL_WAIT" || die "NVSTOR_RAIL_WAIT '$RAIL_WAIT' is not a number of seconds"
XPRT_WAIT=${NVSTOR_XPRT_WAIT:-15}
is_uint "$XPRT_WAIT" || die "NVSTOR_XPRT_WAIT '$XPRT_WAIT' is not a number of seconds"

EXPORT_PATH=${NVSTOR_EXPORT_PATH:-/}
case "$EXPORT_PATH" in
  /*) ;;
  *) die "NVSTOR_EXPORT_PATH must start with /, got '$EXPORT_PATH'" ;;
esac
[[ $EXPORT_PATH =~ [[:space:],] ]] && die "NVSTOR_EXPORT_PATH must not contain spaces or commas: '$EXPORT_PATH'"

# User options come after the safe defaults, so the kernel's last-wins parsing
# lets them tune timeo/retrans; options that would undo ro or soft are refused
# unless they are in NVSTOR_MOUNT_UNSAFE_OPTS.
SAFE=ro,soft,softerr,timeo=100,retrans=2
EXTRA=${NVSTOR_MOUNT_OPTS:-}
UNSAFE=${NVSTOR_MOUNT_UNSAFE_OPTS:-}
[[ $EXTRA =~ [[:space:]] || $UNSAFE =~ [[:space:]] ]] && die "mount options must be comma separated, without spaces"
IFS=, read -r -a extra_opts <<<"$EXTRA"
for o in "${extra_opts[@]}"; do
  case "$o" in
    rw|hard)
      die "NVSTOR_MOUNT_OPTS may not contain '$o' (it undoes ro/soft); put it in NVSTOR_MOUNT_UNSAFE_OPTS if you accept the risk (header of this script)" ;;
    vers=*|proto=*|port=*|max_connect=*|trunkdiscovery|notrunkdiscovery)
      die "NVSTOR_MOUNT_OPTS may not set '$o'; rails-mount.sh sets it" ;;
  esac
done
# Trunking: see NVSTOR_TRUNKING above. trunkdiscovery arrived in Linux 5.18.
kernel_has_trunkdiscovery() {
  local rel major minor
  rel=$("${H[@]}" uname -r) || return 1
  IFS=.- read -r major minor _ <<<"$rel"
  is_uint "$major" && is_uint "$minor" || return 1
  [ "$major" -gt 5 ] || { [ "$major" -eq 5 ] && [ "$minor" -ge 18 ]; }
}
TRUNKING=${NVSTOR_TRUNKING:-}
if [ -z "$TRUNKING" ]; then
  if kernel_has_trunkdiscovery; then TRUNKING=discover
  else
    TRUNKING=mounts
    log "kernel $("${H[@]}" uname -r 2>/dev/null || echo '?') has no trunkdiscovery (5.18+): one mount per rail, $((2 * ${#ADDRS[@]} - 1)) transports for ${#ADDRS[@]} rails"
  fi
fi
case "$TRUNKING" in
  discover) DISCOVER=,trunkdiscovery ;;
  mounts) DISCOVER= ;;
  *) die "NVSTOR_TRUNKING must be discover or mounts, got '$TRUNKING'" ;;
esac
OPTS="vers=4.1,proto=rdma6,port=$PORT,max_connect=${#ADDRS[@]}$DISCOVER,$SAFE${EXTRA:+,$EXTRA}${UNSAFE:+,$UNSAFE}"
[ -z "$UNSAFE" ] || log "WARNING: NVSTOR_MOUNT_UNSAFE_OPTS=$UNSAFE overrides the safe defaults ($SAFE)"

[ -d "$HP$ROOT" ] || die "$HP$ROOT does not exist (mount the hostPath $ROOT there)"
[ -z "$HP" ] || [ -x "$HP/bin/sh" ] || die "$HP does not look like the host's / (mount hostPath / there)"
if [ "$MODE" = sidecar ]; then
  awk -v m="$HP$ROOT" '$2 == m { f = 1 } END { exit !f }' "$MOUNTS" \
    || die "$HP$ROOT is not a mount point: the hostPath $ROOT must be mounted there with mountPropagation: Bidirectional"
fi

# --- unmounting --------------------------------------------------------------
# Mount points under a directory, deepest first, as seen in this container.
mounts_under() {
  awk -v d="$1/" 'index($2, d) == 1 { print length($2), $2 }' "$MOUNTS" | sort -rn | cut -d' ' -f2-
}
# Unmount everything under a per-pod directory (host-relative) and remove it.
# umount -f first: it aborts outstanding RPCs, so a dead source cannot hold up
# the pod's shutdown for retrans x timeo; then a lazy unmount if still busy.
unmount_dir() {
  local dir=$1 m rc=0
  case "$dir" in
    "$ROOT"/?*) ;;
    *) log "error: refusing to clean up $dir (not under $ROOT)"; return 1 ;;
  esac
  while read -r m; do
    [ -n "$m" ] || continue
    if "${H[@]}" umount -f "${m#"$HP"}" 2>/dev/null || "${H[@]}" umount -l "${m#"$HP"}"; then
      log "unmounted ${m#"$HP"}"
    else
      log "error: could not unmount ${m#"$HP"}"; rc=1
    fi
  done < <(mounts_under "$HP$dir")
  rm -rf --one-file-system "$HP$dir" 2>/dev/null || { log "error: could not remove $dir"; rc=1; }
  return $rc
}

MOUNTED="" WAITER=""
cleanup() {
  local rc=$?
  trap - ERR EXIT INT TERM
  [ -z "$WAITER" ] || kill "$WAITER" 2>/dev/null || true
  rm -f "$READY"
  if [ -n "$MOUNTED" ]; then
    unmount_dir "$BASE" || rc=1
  fi
  exit "$rc"
}

if [ "$MODE" = sidecar ]; then
  rm -f "$READY"
  if [ -n "$(mounts_under "$HP$BASE")" ]; then
    log "unmounting what a previous run of this pod left in $BASE"
  fi
  unmount_dir "$BASE" || die "could not clean up $BASE"
  if [ -d "$HP$PODS_DIR" ]; then
    for d in "$HP$ROOT"/*/; do
      [ -d "$d" ] || continue
      id=$(basename "$d")
      [[ $id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || continue
      [ "$id" != "$POD_ID" ] && [ ! -e "$HP$PODS_DIR/$id" ] || continue
      log "pod $id is gone from this node; unmounting what it left in $ROOT/$id"
      unmount_dir "$ROOT/$id" || log "WARNING: could not clean up $ROOT/$id"
    done
  else
    log "WARNING: $PODS_DIR not found on the host; not cleaning up after deleted pods"
  fi
  trap cleanup EXIT
  trap 'log "signal received, unmounting"; exit 0' INT TERM
elif [ -n "$(mounts_under "$HP$BASE")" ]; then
  die "$BASE already has mounts; exec mode does not clean up after other runs"
else
  trap cleanup EXIT
fi

# --- rails -------------------------------------------------------------------
plane() { cut -d: -f1-3 <<<"$1"; }  # fd12:3456:1a01 = one rail plane
expand_plane() { # normalize the first three groups so "::" or case cannot hide a match
  local a=${1,,} head
  head=${a%%::*}
  if [[ $a == *::* ]]; then
    IFS=: read -r -a g <<<"$head"
    while [ "${#g[@]}" -lt 3 ]; do g+=(0); done
  else
    IFS=: read -r -a g <<<"$a"
  fi
  printf '%x:%x:%x\n' "0x${g[0]:-0}" "0x${g[1]:-0}" "0x${g[2]:-0}"
}

# Local rail interfaces and their routable (SLAAC) addresses. SLAAC can take
# a few seconds after the pod starts.
declare -A DEV_OF_PLANE SRC_OF_DEV
want_planes() {
  local a
  for a in "${ADDRS[@]}"; do
    [ -n "${DEV_OF_PLANE[$(expand_plane "$a")]:-}" ] || return 1
  done
}
for ((t = 0; ; t++)); do
  DEV_OF_PLANE=() SRC_OF_DEV=()
  addrs_out=$("${H[@]}" ip -6 -o addr show scope global -tentative 2>&1) \
    || die "ip -6 addr show failed: $addrs_out"
  while read -r dev addr; do
    [ -n "$dev" ] || continue
    DEV_OF_PLANE[$(expand_plane "$addr")]=$dev
    SRC_OF_DEV[$dev]=$addr
  done < <(awk '$2 ~ /^net[0-9]+$/ { split($4, a, "/"); print $2, a[1] }' <<<"$addrs_out")
  want_planes && break
  [ "$t" -lt "$RAIL_WAIT" ] || break
  sleep 1
done
have=""
for p in "${!DEV_OF_PLANE[@]}"; do have+=" ${DEV_OF_PLANE[$p]}=$p"; done
for a in "${ADDRS[@]}"; do
  [ -n "${DEV_OF_PLANE[$(expand_plane "$a")]:-}" ] \
    || die "after ${RAIL_WAIT}s, no local rail interface (netN with a global, non-tentative IPv6 address) is on the plane of $a ($(plane "$a")); have:${have:- none}. Check that the pod holds the rail VFs (Multus NAD + nvidia.com/mlnxnics) and that the rail is up: ip -6 addr show; ip link"
done

# One client VF per rail. The NFS client cannot pick a source interface per
# transport and the RA default routes all point at one VF, so pin a /128 to
# each source address through the VF on the same plane. Pod netns only.
field() { awk -v k="$1" '{ for (i = 1; i < NF; i++) if ($i == k) { print $(i + 1); exit } }' <<<"$2"; }
for a in "${ADDRS[@]}"; do
  dev=${DEV_OF_PLANE[$(expand_plane "$a")]}
  r=$("${H[@]}" ip -6 route get "$a" oif "$dev" 2>&1) \
    || die "no route to $a through $dev (${SRC_OF_DEV[$dev]}): ${r:-ip route get failed}. Is the rail up, and has $dev received a router advertisement? ip -6 route show dev $dev"
  r=${r%%$'\n'*}
  via=$(field via "$r")
  src=$(field src "$r")
  [ -n "$src" ] || die "no source address on the route to $a through $dev: $r"
  out=$("${H[@]}" ip -6 route replace "$a/128" ${via:+via "$via"} dev "$dev" src "$src" 2>&1) \
    || die "could not pin $a/128 to $dev: $out"
  r=$("${H[@]}" ip -6 route get "$a" 2>&1) || die "route to $a vanished after pinning it to $dev: $r"
  r=${r%%$'\n'*}
  case "$r " in
    *" dev $dev "*) log "rail $dev ($src) -> $a" ;;
    *) die "pinned $a/128 to $dev, but the kernel routes it as: $r" ;;
  esac
done

# --- mount -------------------------------------------------------------------
# The first address carries the mount. With trunkdiscovery the client asks
# the source for its other addresses (fs_locations) during the mount and adds
# one transport per address, skipping the one it already has. Without it,
# mounting the export through each other address adds that address to the
# first mount's client (session trunking), at the cost of a second transport
# kept by the extra mount. Reads through the first mount use every rail.
nfs_mount() { # address target
  local out
  mkdir -p "$HP$2" || die "could not create mount point $2"
  MOUNTED=1
  out=$("${H[@]}" mount -t nfs -o "$OPTS" "[$1]:$EXPORT_PATH" "$2" 2>&1) \
    || die "mount -t nfs -o $OPTS [$1]:$EXPORT_PATH $2 failed: $out"
}
nfs_mount "${ADDRS[0]}" "$BASE/data"
if [ "$TRUNKING" = mounts ]; then
  for ((k = 1; k < ${#ADDRS[@]}; k++)); do
    nfs_mount "${ADDRS[$k]}" "$BASE/trunk$k"
  done
fi
awk -v m="$HP$BASE/data" '$2 == m && $3 == "nfs4" && $4 ~ /(^|,)proto=rdma6(,|$)/ { f = 1 } END { exit !f }' "$MOUNTS" \
  || die "$BASE/data is not an NFS/RDMA mount after mount returned: $(grep -F " $HP$BASE/data " "$MOUNTS" || echo 'not in /proc/self/mounts')"

# The client tests each trunked address (an NFSv4.1 SEQUENCE) and attaches
# its transport asynchronously, after mount returns: wait for all of them.
xprts() {
  awk -v m="$HP$BASE/data" '$1 == "device" { on = ($5 == m) } on && $1 == "xprt:" && $2 == "rdma" { n++ } END { print n + 0 }' "$MOUNTSTATS"
}
want=${#ADDRS[@]}
for ((t = 0; ; t++)); do
  n=$(xprts)
  [ "$n" -ge "$want" ] && break
  if [ "$t" -ge "$((XPRT_WAIT * 2))" ]; then
    hint="Check the source's rails and the server log"
    [ "$TRUNKING" = discover ] && hint="Does the source advertise its rail addresses (nvstor-nfs-export --trunk-addr, one per rail)? $hint; NVSTOR_TRUNKING=mounts works without them"
    die "only $n of $want RDMA transports attached after ${XPRT_WAIT}s; the missing rails would leave reads at a fraction of the bandwidth. $hint"
  fi
  sleep 0.5
done
# mountstats can list a transport the client discovered more than once (with
# trunkdiscovery, each rail can show up twice while the client holds one
# transport per rail and the server sees one connection each), so the count is a lower bound
# for the wait above, not the number of connections.
log "mounted [${ADDRS[0]}]:$EXPORT_PATH at $BASE/data over ${#ADDRS[@]} rails ($TRUNKING; mountstats lists $n RDMA transports)"

if [ "$MODE" = exec ]; then
  LINK=${NVSTOR_LINK:-/models}
  ln -sfn "$HP$BASE/data" "$LINK" || die "could not link $LINK to $HP$BASE/data"
  trap - EXIT ERR
  log "exec $*"
  exec "$@"
fi

touch "$READY" || die "could not create the ready file $READY"
log "ready; waiting for SIGTERM to unmount"
# A builtin wait returns as soon as a trapped signal arrives.
sleep infinity &
WAITER=$!
wait "$WAITER" || true
