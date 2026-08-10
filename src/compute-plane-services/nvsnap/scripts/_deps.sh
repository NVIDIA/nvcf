#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Resolve sibling-repo dependencies under the conventional CONTRIBUTING.md
# layout. Sourced by build/test scripts; sets globals like LIBZMQ_SRC etc.
#
# Lookup order for each component <name>:
#   1. $<UPPER_NAME>_SRC if set by the caller — used verbatim
#   2. ${REPO_ROOT}/../<name>/.git exists — use that sibling
#   3. error with a clear "export <UPPER_NAME>_SRC" message
#
# Where REPO_ROOT is the gpucr repo's working directory.

_nvsnap_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

nvsnap_resolve_sibling() {
    # nvsnap_resolve_sibling <env-var-name> <sibling-dir-name>
    # Example: nvsnap_resolve_sibling LIBZMQ_SRC libzmq
    local var_name="$1"
    local sib_name="$2"
    local current
    eval "current=\${$var_name:-}"
    if [ -n "$current" ]; then
        return 0
    fi
    local candidate="$_nvsnap_repo_root/../$sib_name"
    if [ -d "$candidate/.git" ]; then
        eval "$var_name=\"$(cd \"$candidate\" && pwd)\""
        eval "export $var_name"
        return 0
    fi
    echo "ERROR: $var_name not set and sibling not found at $candidate" >&2
    echo "       Set it explicitly: export $var_name=/path/to/$sib_name" >&2
    echo "       See CONTRIBUTING.md for the recommended layout." >&2
    return 1
}
