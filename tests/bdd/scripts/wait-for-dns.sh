#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Polls until a hostname resolves to at least one A record via the
# system resolver. Used after AWS ELB provisioning to bridge the lag
# between gateway Programmed and the hostname being globally resolvable
# from the BDD runner's system resolver.
#
# The BDD runner uses shlex tokenization, not a shell, so an inline
# resolver loop cannot be expressed in `When I run command`. This script
# wraps the loop with a hard timeout and clean exit codes so the feature
# can call it via one DSL step.
#
# Usage:
#   wait-for-dns.sh <hostname> <timeout-seconds>
#
# Exits 0 after three consecutive successful resolutions, 2 on timeout,
# 64 on usage error, and 69 when python3 is unavailable.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <hostname> <timeout-seconds>" >&2
  exit 64
fi

HOSTNAME="$1"
TIMEOUT_SECONDS="$2"

if [[ -z "$HOSTNAME" ]]; then
  echo "hostname must be non-empty" >&2
  exit 64
fi
if ! [[ "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]]; then
  echo "timeout-seconds must be a non-negative integer, got: $TIMEOUT_SECONDS" >&2
  exit 64
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to query the system resolver" >&2
  exit 69
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
attempt=0
consecutive_successes=0
while [[ "$consecutive_successes" -lt 3 ]]; do
  attempt=$(( attempt + 1 ))
  if python3 - "$HOSTNAME" >/dev/null 2>&1 <<'PY'
import socket
import sys

socket.getaddrinfo(sys.argv[1], None, socket.AF_INET)
PY
  then
    consecutive_successes=$(( consecutive_successes + 1 ))
  else
    consecutive_successes=0
  fi
  if [[ "$consecutive_successes" -ge 3 ]]; then
    break
  fi
  if [[ $(date +%s) -ge "$deadline" ]]; then
    echo "timed out after ${TIMEOUT_SECONDS}s waiting for DNS for $HOSTNAME (attempts=$attempt)" >&2
    exit 2
  fi
  sleep 5
done
echo "resolved $HOSTNAME in 3 consecutive system-resolver checks after ${attempt} attempts"
