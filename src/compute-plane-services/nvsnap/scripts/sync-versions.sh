#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Sync all nvsnap image refs (registry + version) across K8s manifests.
#
# Single source of truth for server-bundled manifests:
#   deploy/k8s/workloads/ — copied into the nvsnap-server image at build
#                           time (see internal/server/manifests.go).
# Operational manifests:
#   deploy/k8s/           — applied directly (e2e tests, ops)
#   deploy/               — legacy standalone manifests (kept in sync
#                           defensively in case anyone still applies them)
#
# Handles 8 deployable image names: 4 main components (agent, server,
# init, blobstore) and 4 dependency builders (uvloop, libuv, libzmq,
# pyzmq) consumed by init container manifests.
#
# Rewrites the REGISTRY PREFIX (everything before the image name) too —
# not just the tag — so the 2026-05-21 migration to
# nvcr.io/0651155215864979/ncp-dev gets applied to legacy manifests that
# still reference stg.nvcr.io/zq9tgrjzrfpo.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/versions.sh"

DIRS=(deploy/k8s deploy)

# image-name → version-var
declare -A IMAGES=(
    [nvsnap-agent]="$NVSNAP_APP_VERSION"
    [nvsnap-server]="$NVSNAP_SERVER_VERSION"
    [nvsnap-init]="$NVSNAP_INIT_VERSION"
    [nvsnap-blobstore]="$NVSNAP_BLOBSTORE_VERSION"
    [uvloop-builder]="$NVSNAP_UVLOOP_VERSION"
    [libuv-builder]="$NVSNAP_LIBUV_VERSION"
    [libzmq-builder]="$NVSNAP_LIBZMQ_VERSION"
    [pyzmq-builder]="$NVSNAP_PYZMQ_VERSION"
)

for name in "${!IMAGES[@]}"; do
    ver="${IMAGES[$name]}"
    new="${NVSNAP_REGISTRY}/${name}:${ver}"
    # Match any "<non-space-chars>/<image-name>:<non-space-chars>" segment.
    # The /<image-name>: anchor distinguishes e.g. nvsnap-agent from
    # nvsnap-agent-base (different prefix) and nvsnap-init from nvsnap-init-config.
    sed_re="s|[^[:space:]\"']*/${name}:[^[:space:]\"']*|${new}|g"
    for dir in "${DIRS[@]}"; do
        find "$dir" -name "*.yaml" -exec sed -i -E "${sed_re}" {} \;
    done
    echo "Synced ${name} -> ${new}"
done

# Verify nothing stale remains. For each image, every <name>: reference must
# match the expected full path.
fail=0
for name in "${!IMAGES[@]}"; do
    ver="${IMAGES[$name]}"
    expected="${NVSNAP_REGISTRY}/${name}:${ver}"
    if remaining=$(grep -rn "/${name}:" "${DIRS[@]}" 2>/dev/null | grep -v "${expected}" || true); then
        if [ -n "$remaining" ]; then
            echo "WARNING: stale ${name} references found:" >&2
            echo "$remaining" >&2
            fail=1
        fi
    fi
done

exit $fail
