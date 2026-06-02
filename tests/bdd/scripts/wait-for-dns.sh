#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Polls until a hostname resolves to at least one A record via the
# system resolver. Used after AWS ELB provisioning to bridge the lag
# between gateway Programmed and the hostname being globally resolvable
# from inside a pod's DNS resolver.
#
# The BDD runner uses shlex tokenization, not a shell, so an inline
# `until host ...; do sleep ...; done` cannot be expressed in `When I
# run command`. This script wraps the loop with a hard timeout and
# clean exit codes so the feature can call it via one DSL step.
#
# Usage:
#   wait-for-dns.sh <hostname> <timeout-seconds>
#
# Exits 0 on first successful resolution, 2 on timeout, 64 on usage
# error.

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

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
attempt=0
until host "$HOSTNAME" >/dev/null 2>&1; do
  attempt=$(( attempt + 1 ))
  if [[ $(date +%s) -ge "$deadline" ]]; then
    echo "timed out after ${TIMEOUT_SECONDS}s waiting for DNS for $HOSTNAME (attempts=$attempt)" >&2
    exit 2
  fi
  sleep 5
done
echo "resolved $HOSTNAME after ${attempt} retries"
