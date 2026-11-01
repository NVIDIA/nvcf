#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Check that every entry point libnvsnap_gpushare.so hands out (its wrappers,
# w_*, and the driver functions it exports, cu*) starts with a no-op, as the
# Makefile's GPUSHARE_ENTRY builds them: a program that hooks those entry
# points by rewriting their first instructions (UCX's memory hooks) must find
# nothing of ours to relocate (docker/agent/gpushare/Makefile).
#
#   scripts/check-gpushare-entry.sh libnvsnap_gpushare.so
set -euo pipefail

lib="$1"
bad="$(objdump -d --no-show-raw-insn "$lib" | awk '
    /^[0-9a-f]+ <(w_|cu)[A-Za-z0-9_]*>:$/ { fn = $2; first = 1; n++; next }
    /^[0-9a-f]+ </ { first = 0; next }
    first && NF >= 2 { first = 0; if ($2 != "nop") print fn }
    END { if (n == 0) print "(no w_* or cu* function found)" }')"
if [ -n "$bad" ]; then
    echo "$(basename "$lib"): entry points that do not start with a no-op: $bad" >&2
    exit 1
fi
echo "$(basename "$lib"): every entry point starts with a no-op"
