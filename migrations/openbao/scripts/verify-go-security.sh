#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

binary=${1:?usage: verify-go-security.sh BINARY}
go_bin=${GO:-go}
metadata=$(mktemp)
trap 'rm -f "$metadata"' EXIT INT TERM
"$go_bin" version -m "$binary" > "$metadata"

# GO-2026-6611 has independent standard-library and x/net fixes. Check actual
# binary metadata, since a builder pin alone does not identify embedded code.
awk '
  NR == 1 {
    version = $2
    if (version !~ /^go[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
    sub(/^go/, "", version)
    split(version, go, ".")
    if (go[1] < 1 || (go[1] == 1 && go[2] < 26)) exit 1
    if (go[1] == 1 && go[2] == 26 && go[3] < 9) exit 1
    if (go[1] == 1 && go[2] == 27 && go[3] < 2) exit 1
    toolchain_ok = 1
  }
  $1 == "dep" && $2 == "golang.org/x/net" {
    version = $3
    if (version !~ /^v[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
    sub(/^v/, "", version)
    split(version, net, ".")
    if (net[1] == 0 && net[2] < 60) exit 1
    xnet = 1
    next
  }
  xnet && $1 == "=>" { exit 1 }
  { xnet = 0 }
  END { if (!toolchain_ok) exit 1 }
' "$metadata" || {
  echo "$binary does not meet the Go HTTP/2 security baseline" >&2
  exit 1
}
echo "verified Go security metadata: $binary"
