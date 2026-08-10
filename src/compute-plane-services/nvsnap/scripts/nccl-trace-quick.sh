#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# nccl-trace-quick.sh — Minimal nvidia_ioctl tracer
#
# Run directly on a GPU node (SSH or kubectl debug).
# No orchestration — just starts tracing and you trigger checkpoint separately.
#
# Usage (on GPU node):
#   ./nccl-trace-quick.sh                    # trace all nvidia ioctls
#   ./nccl-trace-quick.sh <pid>              # trace only one process
#   ./nccl-trace-quick.sh cuda-checkpoint    # trace only cuda-checkpoint
#
# In another terminal:
#   # Option A: via nvsnap API
#   curl -X POST http://localhost:8080/api/v1/checkpoint/pod \
#     -H 'Content-Type: application/json' \
#     -d '{"podName":"vllm-70b","namespace":"default"}'
#
#   # Option B: via agent directly
#   curl -X POST http://localhost:8081/v1/checkpoint \
#     -d '{"namespace":"default","podName":"vllm-70b"}'
#
# When it hangs, Ctrl+C to see summary. The last ENTER line is the blocker.

set -euo pipefail

FILTER="${1:-}"

if ! command -v bpftrace &>/dev/null; then
    echo "ERROR: bpftrace not found. Install: apt install bpftrace"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: must run as root (need CAP_BPF)"
    exit 1
fi

echo "=== NVIDIA ioctl tracer ==="
echo "Filter: ${FILTER:-all processes}"
echo "Ctrl+C to stop and show summary"
echo ""

if [ -n "$FILTER" ] && [[ "$FILTER" =~ ^[0-9]+$ ]]; then
    # Filter by PID
    exec bpftrace -e "
kprobe:nvidia_ioctl /pid == $FILTER/ {
    @start[tid] = nsecs;
    printf(\"%-8d %-20s fd=%-4d cmd=0x%-8x ENTER\\n\", pid, comm, arg0, arg1);
}
kretprobe:nvidia_ioctl /@start[tid]/ {
    \$us = (nsecs - @start[tid]) / 1000;
    if (\$us > 1000) {
        printf(\"%-8d %-20s              cmd=0x%-8x DONE %dus ret=%d\\n\", pid, comm, @cmd[tid], \$us, retval);
    }
    @hist = hist(\$us);
    delete(@start[tid]);
}
"
elif [ -n "$FILTER" ]; then
    # Filter by comm name
    exec bpftrace -e "
kprobe:nvidia_ioctl /comm == \"$FILTER\"/ {
    @start[tid] = nsecs;
    @cmd[tid] = arg1;
    printf(\"%-8d %-20s fd=%-4d cmd=0x%-8x ENTER\\n\", pid, comm, arg0, arg1);
}
kretprobe:nvidia_ioctl /@start[tid]/ {
    \$us = (nsecs - @start[tid]) / 1000;
    if (\$us > 1000) {
        printf(\"%-8d %-20s              cmd=0x%-8x DONE %dus ret=%d\\n\", pid, comm, @cmd[tid], \$us, retval);
    }
    @hist = hist(\$us);
    @by_cmd[@cmd[tid]] = count();
    delete(@start[tid]);
    delete(@cmd[tid]);
}
"
else
    # Trace everything but only print slow calls and cuda-checkpoint
    exec bpftrace -e '
kprobe:nvidia_ioctl {
    @start[tid] = nsecs;
    @cmd[tid] = arg1;
    @fd[tid] = arg0;
    if (comm == "cuda-checkpoin" || comm == "nsenter") {
        printf("%-8d %-20s fd=%-4d cmd=0x%-8x ENTER\n", pid, comm, arg0, arg1);
    }
}
kretprobe:nvidia_ioctl /@start[tid]/ {
    $us = (nsecs - @start[tid]) / 1000;
    if ($us > 10000) {
        printf("%-8d %-20s fd=%-4d cmd=0x%-8x SLOW %dus ret=%d\n",
               pid, comm, @fd[tid], @cmd[tid], $us, retval);
    }
    @hist = hist($us);
    @by_cmd[@cmd[tid]] = count();
    if ($us > 1000000) {
        @blocked[comm, @cmd[tid]] = count();
    }
    delete(@start[tid]);
    delete(@cmd[tid]);
    delete(@fd[tid]);
}
'
fi
