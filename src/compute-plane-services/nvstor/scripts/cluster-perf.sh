#!/usr/bin/env bash
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

#
# cluster-perf.sh — measure throughput / latency on the cluster.
# Per protocol:
#
#   NFS  — sequential write GB/s, sequential read GB/s, fsync latency,
#          4KB random read p50/p99
#   S3   — single-object PUT GB/s, GET GB/s, DELETE/s
#   NBD  — sequential write GB/s, read GB/s
#
# Runs against the active cluster (sourced from cluster-config.sh).
# Outputs human-readable rows + a markdown table to LOG_DIR/perf.md so
# the numbers persist after the cluster goes away.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=cluster-config.sh
source "$SCRIPT_DIR/cluster-config.sh"

SSH_OPTS="-i $SSH_KEY -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8"
LOG_DIR="${LOG_DIR:-/tmp/nvstor-perf}"
mkdir -p "$LOG_DIR"
REPORT="$LOG_DIR/perf.md"

remote() {
    local node="$1"; shift
    printf '%s\n' "$*" | ssh $SSH_OPTS "$SSH_USER@$node" 'bash -s'
}

# Capacity selection. Default to 4 GB, which catches order-of-magnitude
# regressions without consuming the whole disk every run; raise
# BENCH_SIZE_MB for checkpoint-sized writes.
SIZE_MB="${BENCH_SIZE_MB:-4096}"
SMALL_IO_OPS="${BENCH_SMALL_IO_OPS:-200}"

log()  { echo "[$(date '+%H:%M:%S')] $*"; }

# 3-decimal float math via awk.
fdiv() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.3f", a / b }'; }
fmul() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.3f", a * b }'; }

# Convert bytes/s to GB/s.
gbs_from_bytes_per_sec() {
    awk -v bps="$1" 'BEGIN { printf "%.3f", bps / 1073741824 }'
}

# Convert MB to GiB.
mb_to_gib() {
    awk -v mb="$1" 'BEGIN { printf "%.3f", mb / 1024 }'
}

# ---------------------------------------------------------------------------
# Bench: NFS sequential write, read, fsync, small-IO latency
# ---------------------------------------------------------------------------
bench_nfs() {
    log "[bench:nfs] mounting on ${NODE1}"
    local script
    script=$(cat <<NFSSH
set -uo pipefail
MNT=\$(mktemp -d -t nvstor-perf-mnt.XXXXXX)
sudo pkill -9 -f 'mount\.nfs' 2>/dev/null || true
sudo timeout 12 mount -t nfs4 \\
    -o port=${NVSTOR_NFS_PORT},proto=tcp,rw,soft,timeo=50,nfsvers=4.1 \\
    127.0.0.1:/ "\$MNT" || { echo "MOUNT_FAILED"; exit 2; }

# 1. Sequential write throughput. dd to a large file, time the wall clock.
SIZE_MB=${SIZE_MB}
START=\$(date +%s.%N)
sudo dd if=/dev/zero of="\$MNT/bench-write.bin" bs=1M count=\$SIZE_MB conv=fdatasync status=none
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$SIZE_MB -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "NFS_SEQ_WRITE bytes_per_sec=\$BPS sec=\$SEC size_mb=\$SIZE_MB"

# 2. Sequential read throughput. drop_caches first so we read from server.
sudo bash -c 'echo 3 > /proc/sys/vm/drop_caches' || true
START=\$(date +%s.%N)
sudo dd if="\$MNT/bench-write.bin" of=/dev/null bs=1M count=\$SIZE_MB status=none
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$SIZE_MB -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "NFS_SEQ_READ bytes_per_sec=\$BPS sec=\$SEC size_mb=\$SIZE_MB"

# 3. Small-I/O random read latency. ${SMALL_IO_OPS} 4KB reads at random
# offsets; report p50/p99 in microseconds.
sudo bash -c 'echo 3 > /proc/sys/vm/drop_caches' || true
python3 - "\$MNT/bench-write.bin" ${SMALL_IO_OPS} <<'PYEOF'
import os, random, sys, time
path, n = sys.argv[1], int(sys.argv[2])
sz = os.path.getsize(path)
fd = os.open(path, os.O_RDONLY)
try:
    samples = []
    for _ in range(n):
        off = random.randrange(0, max(1, sz - 4096)) & ~4095
        t0 = time.perf_counter_ns()
        os.pread(fd, 4096, off)
        samples.append(time.perf_counter_ns() - t0)
finally:
    os.close(fd)
samples.sort()
p50 = samples[len(samples)//2] / 1000.0
p99 = samples[int(len(samples)*0.99)] / 1000.0
print(f"NFS_4K_READ p50_us={p50:.1f} p99_us={p99:.1f} samples={len(samples)}")
PYEOF

# 4. fsync latency on a small write.
python3 - "\$MNT/bench-fsync.bin" 50 <<'PYEOF'
import os, sys, time
path, n = sys.argv[1], int(sys.argv[2])
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
try:
    samples = []
    for i in range(n):
        os.write(fd, b"x" * 64)
        t0 = time.perf_counter_ns()
        os.fsync(fd)
        samples.append(time.perf_counter_ns() - t0)
finally:
    os.close(fd)
samples.sort()
p50 = samples[len(samples)//2] / 1000.0
p99 = samples[int(len(samples)*0.99)] / 1000.0
print(f"NFS_FSYNC p50_us={p50:.1f} p99_us={p99:.1f} samples={len(samples)}")
PYEOF

sudo rm -f "\$MNT/bench-write.bin" "\$MNT/bench-fsync.bin"
sudo umount -lf "\$MNT"
rmdir "\$MNT" 2>/dev/null
NFSSH
)
    remote "$NODE1" "$script"
}

# ---------------------------------------------------------------------------
# Bench: S3 PUT/GET throughput
# ---------------------------------------------------------------------------
bench_s3() {
    log "[bench:s3] PUT/GET on ${NODE1}:${NVSTOR_S3_PORT}"
    local bucket="bench-s3-bucket"
    local key="bench-object.bin"
    local size_mb=${SIZE_MB}

    curl -sf -m 5 -X PUT -o /dev/null \
        "http://${NODE1}:${NVSTOR_S3_PORT}/${bucket}" || { echo "S3 BUCKET PUT FAILED"; return; }

    # Use a remote tmp file so the network roundtrip is what we measure,
    # not local disk → server. We do a server-local PUT via curl with a
    # /dev/zero-fed body that the cluster receives.
    local script
    script=$(cat <<S3SH
set -uo pipefail
size_mb=$size_mb
TMP=\$(mktemp -t nvstor-bench-s3.XXXXXX)
dd if=/dev/zero of="\$TMP" bs=1M count=\$size_mb status=none
START=\$(date +%s.%N)
curl -sf -X PUT --data-binary "@\$TMP" \\
    "http://127.0.0.1:${NVSTOR_S3_PORT}/${bucket}/${key}" >/dev/null
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$size_mb -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "S3_PUT bytes_per_sec=\$BPS sec=\$SEC size_mb=\$size_mb"

START=\$(date +%s.%N)
curl -sf "http://127.0.0.1:${NVSTOR_S3_PORT}/${bucket}/${key}" -o /dev/null
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$size_mb -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "S3_GET bytes_per_sec=\$BPS sec=\$SEC size_mb=\$size_mb"

curl -sf -X DELETE "http://127.0.0.1:${NVSTOR_S3_PORT}/${bucket}/${key}" >/dev/null
curl -sf -X DELETE "http://127.0.0.1:${NVSTOR_S3_PORT}/${bucket}" >/dev/null
rm -f "\$TMP"
S3SH
)
    remote "$NODE1" "$script"
}

# ---------------------------------------------------------------------------
# Bench: NBD sequential write/read
# ---------------------------------------------------------------------------
bench_nbd() {
    log "[bench:nbd] dd through /dev/nbd0 on ${NODE1}"
    local size_mb=$((SIZE_MB / 4))
    local script
    script=$(cat <<NBDSH
set -uo pipefail
DEV=/dev/nbd0
size_mb=$size_mb
sudo nbd-client -d \$DEV >/dev/null 2>&1 || true
VOL=\$(python3 /tmp/nvstor-volume.py create --server 127.0.0.1:9002 --size \$((size_mb * 1024 * 1024))) || exit 2
sudo nbd-client -N "\$VOL" 127.0.0.1 ${NVSTOR_NBD_PORT} \$DEV -timeout 30 \\
    >/dev/null 2>&1 || { echo "NBD CONNECT FAILED"; exit 2; }
SIZE=\$(sudo blockdev --getsize64 \$DEV)
echo "NBD_VOLUME bytes=\$SIZE"

# Sequential write — direct I/O so we measure server, not page cache.
START=\$(date +%s.%N)
sudo dd if=/dev/zero of=\$DEV bs=1M count=\$size_mb oflag=direct status=none
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$size_mb -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "NBD_SEQ_WRITE bytes_per_sec=\$BPS sec=\$SEC size_mb=\$size_mb"

# Sequential read.
START=\$(date +%s.%N)
sudo dd if=\$DEV of=/dev/null bs=1M count=\$size_mb iflag=direct status=none
END=\$(date +%s.%N)
SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
BPS=\$(awk -v sm=\$size_mb -v s=\$SEC 'BEGIN { print sm * 1048576 / s }')
echo "NBD_SEQ_READ bytes_per_sec=\$BPS sec=\$SEC size_mb=\$size_mb"

sudo nbd-client -d \$DEV >/dev/null 2>&1
NBDSH
)
    remote "$NODE1" "$script"
}

# ---------------------------------------------------------------------------
# Bench: S3-over-RDMA — runs examples/s3_rdma_bench cross-node
# (NODE2 client → NODE1 server). Skips silently if the bench binary
# isn't deployed or RDMA hardware isn't present so this script keeps
# working on non-RDMA boxes.
# ---------------------------------------------------------------------------
bench_s3_rdma() {
    log "[bench:s3-rdma] PUT 16 MiB qd=8 on ${NODE1}:${NVSTOR_RDMA_PORT:-9877}"
    local rdma_port="${NVSTOR_RDMA_PORT:-9877}"
    local size_mb=16
    local ops=64
    local conc=8
    local script
    script=$(cat <<S3RDMASH
set -uo pipefail
# The server authenticates every s3-rdma request; the bench reads its
# credentials from the environment. Without these it gets STATUS_UNAUTHORIZED.
export NVSTOR_ACCESS_KEY='${NVSTOR_ACCESS_KEY}'
export NVSTOR_SECRET_KEY='${NVSTOR_SECRET_KEY}'
BENCH=/tmp/s3_rdma_bench
if [[ ! -x "\$BENCH" ]]; then
    echo "S3_RDMA_SKIP reason=no_bench_binary"
    exit 0
fi
if ! command -v ibv_devices >/dev/null; then
    echo "S3_RDMA_SKIP reason=no_ibverbs"
    exit 0
fi
OUT=\$(timeout 120 "\$BENCH" --addr=${NODE1}:${rdma_port} \\
    --mode=put --ops=${ops} --size-mb=${size_mb} --concurrency=${conc} 2>&1) || {
    echo "S3_RDMA_PUT skip=true reason=bench_failed"
    echo "\$OUT" | tail -5
    exit 0
}
echo "\$OUT" | tail -10
MIBS=\$(echo "\$OUT" | awk '/throughput:/ { for (i=1;i<=NF;i++) if (\$i ~ /^[0-9.]+\$/ && \$(i+1) == "MiB/s") { print \$i; exit } }')
if [[ -n "\$MIBS" ]]; then
    BPS=\$(awk -v m="\$MIBS" 'BEGIN { print m * 1048576 }')
    echo "S3_RDMA_PUT bytes_per_sec=\$BPS size_mb=${size_mb} ops=${ops} conc=${conc}"
fi
P50=\$(echo "\$OUT" | awk '/latency/ { for (i=1;i<=NF;i++) if (\$i ~ /^p50=/) { split(\$i, p, "="); print p[2]; exit } }')
P99=\$(echo "\$OUT" | awk '/latency/ { for (i=1;i<=NF;i++) if (\$i ~ /^p99=/) { split(\$i, p, "="); print p[2]; exit } }')
if [[ -n "\$P50" && -n "\$P99" ]]; then
    echo "S3_RDMA_LAT p50_us=\$P50 p99_us=\$P99"
fi
S3RDMASH
)
    remote "$NODE2" "$script"
}

# ---------------------------------------------------------------------------
# Run + format
# ---------------------------------------------------------------------------
{
    echo "# nvstor perf baseline"
    echo
    echo "_Generated $(date -u +%Y-%m-%dT%H:%M:%SZ)_"
    echo "_(${NODE1} + ${NODE2}; bench size = ${SIZE_MB} MiB; small-I/O ops = ${SMALL_IO_OPS})_"
    echo
    echo "## Measured"
    echo
    echo "| Protocol | Op | Result |"
    echo "|---|---|---|"
} > "$REPORT"

CAPTURE="$LOG_DIR/raw.log"
: > "$CAPTURE"

bench_nfs 2>&1 | tee -a "$CAPTURE"
bench_s3  2>&1 | tee -a "$CAPTURE"
bench_nbd 2>&1 | tee -a "$CAPTURE"
bench_s3_rdma 2>&1 | tee -a "$CAPTURE"

# Synthesize the markdown table from the raw log.
awk '
function gib(b) { return sprintf("%.3f", b / 1073741824) }
function mibs(b) { return sprintf("%.0f", b / 1048576) }

/^NFS_SEQ_WRITE/ { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| NFS  | sequential write | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^NFS_SEQ_READ/  { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| NFS  | sequential read  | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^NFS_4K_READ/   { for (i=1;i<=NF;i++) {
                      if ($i ~ /^p50_us=/) { split($i, p, "="); p50=p[2] }
                      if ($i ~ /^p99_us=/) { split($i, p, "="); p99=p[2] } }
                  print "| NFS  | 4 KiB random read | p50 " p50 " µs, p99 " p99 " µs |" }
/^NFS_FSYNC/     { for (i=1;i<=NF;i++) {
                      if ($i ~ /^p50_us=/) { split($i, p, "="); p50=p[2] }
                      if ($i ~ /^p99_us=/) { split($i, p, "="); p99=p[2] } }
                  print "| NFS  | fsync (small write) | p50 " p50 " µs, p99 " p99 " µs |" }
/^S3_PUT/        { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| S3   | single-object PUT | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^S3_GET/        { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| S3   | single-object GET | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^NBD_SEQ_WRITE/ { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| NBD  | sequential write | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^NBD_SEQ_READ/  { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  print "| NBD  | sequential read  | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^S3_RDMA_PUT/   { for (i=1;i<=NF;i++) if ($i ~ /^bytes_per_sec=/) { split($i, p, "="); bps=p[2] }
                  if (bps != "")
                    print "| S3-RDMA | 16 MiB PUT qd=8 | " gib(bps) " GiB/s (" mibs(bps) " MiB/s) |" }
/^S3_RDMA_LAT/   { for (i=1;i<=NF;i++) {
                      if ($i ~ /^p50_us=/) { split($i, p, "="); p50=p[2] }
                      if ($i ~ /^p99_us=/) { split($i, p, "="); p99=p[2] } }
                  print "| S3-RDMA | PUT latency | p50 " p50 " µs, p99 " p99 " µs |" }
/^S3_RDMA_SKIP/  { for (i=1;i<=NF;i++) if ($i ~ /^reason=/) { split($i, p, "="); r=p[2] }
                  print "| S3-RDMA | (skipped: " r ") | — |" }
' "$CAPTURE" >> "$REPORT"

echo >> "$REPORT"
echo "## Raw" >> "$REPORT"
echo >> "$REPORT"
echo '```' >> "$REPORT"
cat "$CAPTURE" >> "$REPORT"
echo '```' >> "$REPORT"

echo
echo "==============================================="
echo "Report: $REPORT"
cat "$REPORT"
