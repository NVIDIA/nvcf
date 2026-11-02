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

# ok and bad always return 0, so "check && ok || bad" is a safe if-then-else.
# shellcheck disable=SC2015
# Tests for rails-mount.sh that need no root, no RDMA and no cluster.
#
#   deploy/nfs-rdma/test-rails-mount.sh
#
# Runs rails-mount.sh with NVSTOR_HOST=/ (no chroot), fake ip, mount and
# umount commands first on PATH, and fake /proc/self/mounts and mountstats
# files that the fake mount and umount keep up to date. Covers argument
# validation, the rail and route failure paths, the mount options, the
# sidecar lifecycle (ready file, SIGTERM unmount, stale-mount cleanup) and
# exec mode. What it cannot cover (the kernel NFS client, real routes,
# mount propagation) is the hardware test in README.md.
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$HERE/rails-mount.sh
T=$(mktemp -d "${TMPDIR:-/tmp}/rails-mount-test.XXXXXX")
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"

cat >"$T/bin/ip" <<'EOF'
#!/bin/bash
# $FAKE_IP_ADDRS: "dev addr" lines. FAKE_ROUTE_FAIL=addr: route get fails.
args="$*"
case "$args" in
  "-6 -o addr show scope global -tentative")
    [ -z "${FAKE_ADDR_FAIL:-}" ] || { echo "Cannot open netlink socket" >&2; exit 1; }
    while read -r d a; do
      echo "5: $d    inet6 $a/64 scope global dynamic mngtmpaddr \\       valid_lft 2591998sec"
    done <"$FAKE_IP_ADDRS" ;;
  "-6 route get "*" oif "*)
    set -- $args
    [ "$4" != "${FAKE_ROUTE_FAIL:-}" ] || { echo "RTNETLINK answers: Network is unreachable" >&2; exit 2; }
    src=$(awk -v d="$6" '$1 == d { print $2 }' "$FAKE_IP_ADDRS")
    echo "$4 from :: via fe80::1 dev $6 proto ra src $src metric 1024 pref medium" ;;
  "-6 route replace "*) echo "ip $args" >>"$FAKE_LOG" ;;
  "-6 route get "*)
    set -- $args
    dev=$(grep -F "replace $4/128 " "$FAKE_LOG" | tail -1 | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") print $(i + 1) }')
    [ -z "${FAKE_ROUTE_WRONG:-}" ] || dev=net9
    echo "$4 from :: via fe80::1 dev ${dev:-eth0} proto ra src x metric 1024 pref medium" ;;
  *) echo "fake ip: unexpected: $args" >&2; exit 1 ;;
esac
EOF

cat >"$T/bin/mount" <<'EOF'
#!/bin/bash
# mount -t nfs -o OPTS SOURCE TARGET
echo "mount $*" >>"$FAKE_LOG"
[ -z "${FAKE_MOUNT_FAIL:-}" ] || { echo "mount.nfs: Connection refused" >&2; exit 32; }
opts=$4 src=$5 dst=$6
type=nfs4
[ -z "${FAKE_MOUNT_TCP:-}" ] || opts=${opts/proto=rdma6/proto=tcp6}
echo "$src $dst $type $opts 0 0" >>"$FAKE_MOUNTS"
case "$dst" in
  */data)
    echo "device $src mounted on $dst with fstype nfs4 statvers=1.1" >>"$FAKE_MOUNTSTATS"
    for ((i = 0; i < ${FAKE_XPRTS:-4}; i++)); do printf '\txprt:\trdma 0 0 1 0 0\n' >>"$FAKE_MOUNTSTATS"; done ;;
esac
EOF

cat >"$T/bin/uname" <<'EOF'
#!/bin/bash
# uname -r: FAKE_UNAME (default a kernel with trunkdiscovery).
echo "${FAKE_UNAME:-6.8.0-1015-nvidia-64k}"
EOF

cat >"$T/bin/umount" <<'EOF'
#!/bin/bash
echo "umount $*" >>"$FAKE_LOG"
dst=${*: -1}
grep -v -F " $dst " "$FAKE_MOUNTS" >"$FAKE_MOUNTS.new"; mv "$FAKE_MOUNTS.new" "$FAKE_MOUNTS"
EOF
chmod +x "$T/bin/"*

A1=fd12:3456:1a01:40e7::1 A2=fd12:3456:1a02:40e7::1 A3=fd12:3456:1a03:40e7::1 A4=fd12:3456:1a04:40e7::1
UID1=0b5e0b6c-1d2f-4a3b-9c8d-111111111111
pass=0 fail=0

# Fresh fake host for one case.
setup() {
  rm -rf "$T/case"; mkdir -p "$T/case/root" "$T/case/pods/$UID1"
  : >"$T/case/log"; : >"$T/case/mountstats"
  echo "tmpfs $T/case/root tmpfs rw 0 0" >"$T/case/mounts"
  cat >"$T/case/addrs" <<EOF
net1 fd12:3456:1a01:40e8::21
net2 fd12:3456:1a02:40e8::21
net3 fd12:3456:1a03:40e8::21
net4 fd12:3456:1a04:40e8::21
EOF
}
# run MODE... with the fake environment; extra VAR=value settings come first.
run() {
  local -a vars=()
  while [[ ${1:-} == *=* ]]; do vars+=("$1"); shift; done
  env PATH="$T/bin:$PATH" NVSTOR_HOST=/ NVSTOR_MOUNT_ROOT="$T/case/root" \
    NVSTOR_PROC_MOUNTS="$T/case/mounts" NVSTOR_PROC_MOUNTSTATS="$T/case/mountstats" \
    NVSTOR_KUBELET_PODS_DIR="$T/case/pods" NVSTOR_READY_FILE="$T/case/ready" \
    NVSTOR_POD_ID="$UID1" NVSTOR_RAIL_WAIT=0 NVSTOR_XPRT_WAIT=0 NVSTOR_LINK="$T/case/models" \
    NVSTOR_SOURCE_ADDRS="$A1 $A2 $A3 $A4" \
    FAKE_IP_ADDRS="$T/case/addrs" FAKE_LOG="$T/case/log" FAKE_MOUNTS="$T/case/mounts" \
    FAKE_MOUNTSTATS="$T/case/mountstats" \
    "${vars[@]}" bash "$SCRIPT" "$@" >"$T/case/out" 2>&1
}
ok() { echo "ok   $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1"; sed 's/^/     | /' "$T/case/out"; fail=$((fail + 1)); }
# expect NAME RC PATTERN [VAR=value...] MODE...: exit code and a rails-mount: line.
expect() {
  local name=$1 rc=$2 pat=$3; shift 3
  setup
  run "$@"; local got=$?
  if [ "$got" -eq "$rc" ] && grep -q -- "rails-mount: .*$pat" "$T/case/out"; then ok "$name"
  else echo "     (exit $got, want $rc, pattern '$pat')"; bad "$name"; fi
}
nomounts() { ! grep -q -F "$T/case/root/" "$T/case/mounts"; }

# --- usage and validation -----------------------------------------------------
expect "no mode"                 2 ""
grep -q "usage: rails-mount.sh" "$T/case/out" || bad "usage text"
expect "unknown mode"            2 ""  nope
expect "exec without command"    2 ""  exec --
expect "empty addresses"         1 "NVSTOR_SOURCE_ADDRS is empty"           NVSTOR_SOURCE_ADDRS=" " exec -- true
expect "comma separated"         1 "separated by spaces, not commas"        "NVSTOR_SOURCE_ADDRS=$A1,$A2" exec -- true
expect "IPv4 address"            1 "'10.0.0.1' is not an IPv6 address"      NVSTOR_SOURCE_ADDRS=10.0.0.1 exec -- true
expect "bracketed address"       1 "is not an IPv6 address"                 "NVSTOR_SOURCE_ADDRS=[$A1]" exec -- true
expect "address with prefix"     1 "is not an IPv6 address"                 "NVSTOR_SOURCE_ADDRS=$A1/64" exec -- true
expect "two ::"                  1 "is not an IPv6 address"                 NVSTOR_SOURCE_ADDRS=fd12::1::2 exec -- true
expect "five hex digits"         1 "is not an IPv6 address"                 NVSTOR_SOURCE_ADDRS=fd12:34560::1 exec -- true
expect "too few groups"          1 "is not an IPv6 address"                 NVSTOR_SOURCE_ADDRS=fd12:3456:1 exec -- true
expect "hostname"                1 "is not an IPv6 address"                 NVSTOR_SOURCE_ADDRS=source.local exec -- true
expect "duplicate address"       1 "lists $A1 twice"                        "NVSTOR_SOURCE_ADDRS=$A1 $A1" exec -- true
expect "bad port"                1 "NVSTOR_RDMA_PORT 'x'"                  NVSTOR_RDMA_PORT=x exec -- true
expect "port 0"                  1 "NVSTOR_RDMA_PORT '0'"                  NVSTOR_RDMA_PORT=0 exec -- true
expect "relative export path"    1 "NVSTOR_EXPORT_PATH must start with /"  NVSTOR_EXPORT_PATH=models exec -- true
expect "pod id with slash"       1 "NVSTOR_POD_ID '../x'"                  NVSTOR_POD_ID=../x exec -- true
expect "relative mount root"     1 "NVSTOR_MOUNT_ROOT must be an absolute" NVSTOR_MOUNT_ROOT=rel exec -- true
expect "missing mount root"      1 "does not exist"                          NVSTOR_MOUNT_ROOT=/nonexistent/x exec -- true
expect "opts: hard refused"      1 "may not contain 'hard'"                 NVSTOR_MOUNT_OPTS=timeo=50,hard exec -- true
expect "opts: rw refused"        1 "may not contain 'rw'"                   NVSTOR_MOUNT_OPTS=rw exec -- true
expect "opts: vers refused"      1 "may not set 'vers=4.2'"                 NVSTOR_MOUNT_OPTS=vers=4.2 exec -- true
expect "opts: spaces refused"    1 "comma separated"                        "NVSTOR_MOUNT_OPTS=timeo=50, retrans=1" exec -- true
expect "sidecar: root not a mount" 1 "is not a mount point"                 NVSTOR_PROC_MOUNTS=/dev/null sidecar

# --- rails and routes -----------------------------------------------------------
setup; sed -i '/^net3/d' "$T/case/addrs"; run exec -- true
if [ $? -eq 1 ] && grep -q "rails-mount: error: after 0s, no local rail interface .* plane of $A3 (fd12:3456:1a03)" "$T/case/out"
then ok "rail down / no address"; else bad "rail down / no address"; fi
expect "ip addr fails"           1 "ip -6 addr show failed"                 FAKE_ADDR_FAIL=1 exec -- true
expect "route get fails"         1 "no route to $A2 through net2"           "FAKE_ROUTE_FAIL=$A2" exec -- true
expect "pin does not take"       1 "pinned $A1/128 to net1, but"            FAKE_ROUTE_WRONG=1 exec -- true
setup; run exec -- true; if grep -q "replace $A3/128 via fe80::1 dev net3 src fd12:3456:1a03:40e8::21" "$T/case/log"
then ok "route pinned per plane"; else bad "route pinned per plane"; fi
setup; printf 'net1 FD12:3456:1A01:40e8::21\nnet2 fd12:3456:1a02::21\n' >"$T/case/addrs"
run "NVSTOR_SOURCE_ADDRS=$A1 fd12:3456:1a02:1::9" exec -- true
if grep -q "rails-mount: rail net2 " "$T/case/out"; then ok "plane match ignores case and ::"; else bad "plane match ignores case and ::"; fi

# --- mount ------------------------------------------------------------------------
expect "mount fails"             1 "mount -t nfs .* failed: mount.nfs: Connection refused" FAKE_MOUNT_FAIL=1 exec -- true
expect "not an RDMA mount"       1 "is not an NFS/RDMA mount"               FAKE_MOUNT_TCP=1 exec -- true
nomounts && ok "failed mount cleaned up" || bad "failed mount cleaned up"
expect "missing transports"      1 "only 2 of 4 RDMA transports"            FAKE_XPRTS=2 exec -- true
nomounts && ok "partial trunk cleaned up" || bad "partial trunk cleaned up"
expect "discover: hint on missing" 1 "nvstor-nfs-export --trunk-addr"        FAKE_XPRTS=2 exec -- true
expect "mounts: no discover hint"  1 "only 2 of 4 RDMA transports.*server log$" FAKE_XPRTS=2 NVSTOR_TRUNKING=mounts exec -- true
nomounts && ok "partial trunk cleaned up (mounts)" || bad "partial trunk cleaned up (mounts)"
expect "trunking: bad value"     1 "NVSTOR_TRUNKING must be discover or mounts" NVSTOR_TRUNKING=both exec -- true
expect "opts: trunkdiscovery refused" 1 "may not set 'notrunkdiscovery'"     NVSTOR_MOUNT_OPTS=notrunkdiscovery exec -- true

# --- trunking ---------------------------------------------------------------------
# Kernel >= 5.18: one mount with trunkdiscovery, no per-rail mounts.
setup; run exec -- true
if [ "$(grep -c '^mount ' "$T/case/log")" -eq 1 ] && grep -q "max_connect=4,trunkdiscovery," "$T/case/log" \
   && grep -q "over 4 rails (discover; mountstats lists 4 RDMA transports)" "$T/case/out"
then ok "discover: one mount"; else bad "discover: one mount"; fi
# Older kernel: one mount per rail, and a log line saying why.
setup; run FAKE_UNAME=5.15.0-122-generic exec -- true
if [ "$(grep -c '^mount ' "$T/case/log")" -eq 4 ] && ! grep -q trunkdiscovery "$T/case/log" \
   && grep -q "trunk3$" "$T/case/log" && grep -q "has no trunkdiscovery (5.18+): one mount per rail, 7 transports" "$T/case/out"
then ok "old kernel: one mount per rail"; else bad "old kernel: one mount per rail"; fi
setup; run FAKE_UNAME=5.18.0-1 exec -- true
grep -q "trunkdiscovery" "$T/case/log" && ok "5.18 counts as new" || bad "5.18 counts as new"
setup; run FAKE_UNAME=4.19.0 NVSTOR_TRUNKING=discover exec -- true
grep -q "trunkdiscovery" "$T/case/log" && ok "NVSTOR_TRUNKING=discover overrides the kernel check" || bad "NVSTOR_TRUNKING=discover overrides the kernel check"
setup; run NVSTOR_TRUNKING=mounts exec -- true
[ "$(grep -c '^mount ' "$T/case/log")" -eq 4 ] && ok "NVSTOR_TRUNKING=mounts on a new kernel" || bad "NVSTOR_TRUNKING=mounts on a new kernel"
setup; run FAKE_UNAME=garbage exec -- true
[ "$(grep -c '^mount ' "$T/case/log")" -eq 4 ] && ok "unparseable kernel release: mounts" || bad "unparseable kernel release: mounts"

setup; run NVSTOR_MOUNT_OPTS=timeo=50,retrans=1 exec -- true
if grep -q "mount -t nfs -o vers=4.1,proto=rdma6,port=20049,max_connect=4,trunkdiscovery,ro,soft,softerr,timeo=100,retrans=2,timeo=50,retrans=1 \[$A1\]:/ $T/case/root/$UID1/data" "$T/case/log"
then ok "extra opts appended after safe defaults"; else bad "extra opts appended after safe defaults"; fi
setup; run NVSTOR_MOUNT_UNSAFE_OPTS=hard exec -- true
if grep -q "WARNING: NVSTOR_MOUNT_UNSAFE_OPTS=hard" "$T/case/out" && grep -q "retrans=2,hard " "$T/case/log"
then ok "unsafe opts allowed with warning"; else bad "unsafe opts allowed with warning"; fi
setup; run "NVSTOR_SOURCE_ADDRS=$A1
$A2" exec -- true
if grep -q "max_connect=2," "$T/case/log"; then ok "newline separated addresses"; else bad "newline separated addresses"; fi

# --- exec mode ----------------------------------------------------------------------
setup
# shellcheck disable=SC2016 # expanded by the workload's shell
run exec -- sh -c 'echo workload-ran; readlink "$NVSTOR_LINK"'
if grep -q "^workload-ran$" "$T/case/out" && grep -q "^$T/case/root/$UID1/data$" "$T/case/out"
then ok "exec runs the workload with the link"; else bad "exec runs the workload with the link"; fi

# --- sidecar lifecycle ---------------------------------------------------------------
setup
STALE=0b5e0b6c-1d2f-4a3b-9c8d-222222222222 LIVE=0b5e0b6c-1d2f-4a3b-9c8d-333333333333
mkdir -p "$T/case/root/$STALE/data" "$T/case/root/$LIVE/data" "$T/case/root/$UID1/data" "$T/case/pods/$LIVE"
for id in $STALE $LIVE $UID1; do echo "x $T/case/root/$id/data nfs4 ro 0 0" >>"$T/case/mounts"; done
touch "$T/case/ready"
run NVSTOR_TRUNKING=mounts sidecar &
pid=$!
for _ in $(seq 50); do [ -f "$T/case/ready" ] && grep -q "ready; waiting" "$T/case/out" && break; sleep 0.1; done
if [ -f "$T/case/ready" ] && grep -q "ready; waiting" "$T/case/out"; then ok "sidecar becomes ready"; else bad "sidecar becomes ready"; fi
grep -q "previous run of this pod" "$T/case/out" && ok "own stale mount cleaned" || bad "own stale mount cleaned"
if ! grep -q " $T/case/root/$STALE/data " "$T/case/mounts" && [ ! -e "$T/case/root/$STALE" ]
then ok "deleted pod's mount cleaned"; else bad "deleted pod's mount cleaned"; fi
grep -q " $T/case/root/$LIVE/data " "$T/case/mounts" && ok "live pod's mount kept" || bad "live pod's mount kept"
env NVSTOR_HOST=/ NVSTOR_MOUNT_ROOT="$T/case/root" NVSTOR_PROC_MOUNTS="$T/case/mounts" \
  NVSTOR_READY_FILE="$T/case/ready" NVSTOR_POD_ID="$UID1" bash "$SCRIPT" check \
  && ok "check passes while mounted" || bad "check passes while mounted"
# The probe must fail if the mount disappears under a running sidecar.
cp "$T/case/mounts" "$T/case/mounts.saved"; grep -v " $T/case/root/$UID1/data " "$T/case/mounts.saved" >"$T/case/mounts"
env NVSTOR_HOST=/ NVSTOR_MOUNT_ROOT="$T/case/root" NVSTOR_PROC_MOUNTS="$T/case/mounts" \
  NVSTOR_READY_FILE="$T/case/ready" NVSTOR_POD_ID="$UID1" bash "$SCRIPT" check 2>/dev/null \
  && bad "check fails without the mount" || ok "check fails without the mount"
cp "$T/case/mounts.saved" "$T/case/mounts"
pkill -TERM -P "$pid"; wait "$pid"; rc=$?  # $pid is the run subshell; its child is the script
if [ "$rc" -eq 0 ] && grep -q "signal received, unmounting" "$T/case/out" && [ ! -e "$T/case/ready" ] \
   && ! grep -q -F "$T/case/root/$UID1/" "$T/case/mounts" && [ ! -e "$T/case/root/$UID1" ]
then ok "SIGTERM unmounts every trunk and removes the ready file"; else bad "SIGTERM unmounts every trunk and removes the ready file"; fi
grep -q "^umount -f $T/case/root/$UID1/trunk3$" "$T/case/log" && ok "trunks unmounted with -f" || bad "trunks unmounted with -f"
env NVSTOR_HOST=/ NVSTOR_MOUNT_ROOT="$T/case/root" NVSTOR_PROC_MOUNTS="$T/case/mounts" \
  NVSTOR_READY_FILE="$T/case/ready" NVSTOR_POD_ID="$UID1" bash "$SCRIPT" check 2>/dev/null \
  && bad "check fails after shutdown" || ok "check fails after shutdown"

echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
