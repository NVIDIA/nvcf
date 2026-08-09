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

# Helm values files spell an image as split `repository:` / `tag:` fields
# rather than one registry/name:tag token, so the substitution below can
# never see them and the grep-based check below can never flag them. They
# are handled separately by chart_tag()/set_chart_tag().
CHART_VALUES=(deploy/helm/nvsnap/values.yaml)

# Print the tag a chart values file pins for <image-name>, or nothing when
# that repository isn't present. \042 and \047 are " and ' — spelled in
# octal so this awk program survives shell quoting intact.
chart_tag() {
    awk -v name="$2" '
        $1 == "repository:" { pending = ($2 == name) }
        pending && $1 == "tag:" { gsub(/[\042\047]/, "", $2); print $2; exit }
    ' "$1"
}

# Rewrite the tag a chart values file pins for <image-name>, preserving the
# original indentation.
set_chart_tag() {
    local file="$1"
    awk -v name="$2" -v ver="$3" '
        $1 == "repository:" { pending = ($2 == name) }
        pending && $1 == "tag:" {
            match($0, /^[ \t]*/)
            print substr($0, 1, RLENGTH) "tag: \"" ver "\""
            pending = 0
            next
        }
        { print }
    ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

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
    for f in "${CHART_VALUES[@]}"; do
        [ -f "$f" ] && [ -n "$(chart_tag "$f" "$name")" ] || continue
        set_chart_tag "$f" "$name" "$ver"
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

# Same check for chart values. The grep above matches a combined
# registry/name:tag token, which split repository:/tag: fields never form —
# so without this loop a drifting chart tag passes verification silently.
# That is exactly how the chart shipped nvsnap-agent v0.1.3 against an
# NVSNAP_APP_VERSION of v0.2.32 (nvsnap#731).
for f in "${CHART_VALUES[@]}"; do
    [ -f "$f" ] || continue
    for name in "${!IMAGES[@]}"; do
        actual=$(chart_tag "$f" "$name")
        [ -n "$actual" ] || continue
        if [ "$actual" != "${IMAGES[$name]}" ]; then
            echo "WARNING: ${f} pins ${name} tag ${actual}, expected ${IMAGES[$name]}" >&2
            fail=1
        fi
    done
    # The chart builds refs as <imageRegistry>/<repository>:<tag>, so a
    # drifting registry breaks every image at once.
    registry=$(awk '$1 == "imageRegistry:" { print $2; exit }' "$f")
    if [ -n "$registry" ] && [ "$registry" != "$NVSNAP_REGISTRY" ]; then
        echo "WARNING: ${f} imageRegistry is ${registry}, expected ${NVSNAP_REGISTRY}" >&2
        fail=1
    fi
done

exit $fail
