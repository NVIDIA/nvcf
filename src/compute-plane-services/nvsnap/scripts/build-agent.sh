#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build NVSNAP agent with layered caching
# - Base image: CRIU, system deps (slow, rarely changes)
# - App image: Go binaries, intercept lib (fast, changes often)
#
# Commands:
#   base             Build base image (CRIU, system deps) - slow, ~5 min
#   app              Build app image (Go, intercept) - fast, ~30 sec
#   all              Build both images
#   push-base        Push base image to registry
#   push-app         Push app image to registry
#   deploy           Deploy agent to k8s cluster
#   bump             Increment app version (v0.9.22 -> v0.9.23)
#   sync-versions    Update K8s manifests + Dockerfile.app from versions.sh
#   build-and-deploy Sync + build app + push + deploy (single command)
#   show-versions    Display current versions

set -euo pipefail

# Ensure Docker talks to local daemon, not remote
unset DOCKER_HOST 2>/dev/null || true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Source centralized versions (supports env var overrides)
source "${SCRIPT_DIR}/versions.sh"

# Backward compat: honor legacy env vars if set
REGISTRY="${REGISTRY:-$NVSNAP_REGISTRY}"
BASE_VERSION="${BASE_VERSION:-$NVSNAP_BASE_VERSION}"
APP_VERSION="${APP_VERSION:-$NVSNAP_APP_VERSION}"
# KUBECONFIG is needed only for subcommands that talk to a cluster
# (deploy, build-and-deploy, full-cycle). Pure image builds + pushes
# don't need it. The deploy() function below re-asserts when relevant.

BASE_IMAGE="${REGISTRY}/nvsnap-agent-base:${BASE_VERSION}"
APP_IMAGE="${REGISTRY}/nvsnap-agent:${APP_VERSION}"

# Auto-detect DOCKER_HOST if not set
if [ -z "${DOCKER_HOST:-}" ]; then
    if [ -S /var/run/docker.sock ]; then
        export DOCKER_HOST="unix:///var/run/docker.sock"
    elif [ -S "$HOME/.docker/run/docker.sock" ]; then
        export DOCKER_HOST="unix://$HOME/.docker/run/docker.sock"
    elif [ -S "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/docker.sock" ]; then
        export DOCKER_HOST="unix://${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/docker.sock"
    fi
fi

# CRIU source directory (from versions.sh, overridable via CRIU_SRC env var)
CRIU_SRC="${CRIU_SRC:-$NVSNAP_CRIU_SRC}"

# Track which criu-src commit the base image was built from
CRIU_BASE_COMMIT_FILE="${PROJECT_ROOT}/.criu-base-commit"

usage() {
    echo "Usage: $0 [base|app|all|push-base|push-app|deploy|bump|sync-versions|build-and-deploy|full-cycle|show-versions]"
    echo ""
    echo "Commands:"
    echo "  base             - Build base image (CRIU, system deps) - slow, ~5 min"
    echo "  app              - Build app image (Go, intercept) - fast, ~30 sec"
    echo "  all              - Build both images"
    echo "  push-base        - Push base image to registry"
    echo "  push-app         - Push app image to registry"
    echo "  deploy           - Deploy agent to k8s cluster"
    echo "  bump             - Increment app version in versions.sh (v0.9.22 -> v0.9.23)"
    echo "  sync-versions    - Update K8s manifests + Dockerfile.app from versions.sh"
    echo "  build-and-deploy - sync-versions + build app + push + deploy"
    echo "  full-cycle       - sync + build base + verify + push + build app + push + deploy"
    echo "  show-versions    - Display current versions from versions.sh"
    echo ""
    echo "Environment (override via env vars or versions.sh):"
    echo "  BASE_VERSION=${BASE_VERSION}"
    echo "  APP_VERSION=${APP_VERSION}"
    echo "  REGISTRY=${REGISTRY}"
    echo "  DOCKER_HOST=${DOCKER_HOST:-<not set>}"
}

# Check if CRIU source has changed since last base build
criu_src_changed() {
    local current_commit
    current_commit=$(git -C "${CRIU_SRC}" rev-parse HEAD 2>/dev/null || echo "unknown")

    if [ ! -f "$CRIU_BASE_COMMIT_FILE" ]; then
        echo "  No previous base build recorded — will use --no-cache"
        return 0  # changed (no record)
    fi

    local last_commit
    last_commit=$(cat "$CRIU_BASE_COMMIT_FILE")

    if [ "$current_commit" != "$last_commit" ]; then
        echo "  CRIU src changed: ${last_commit:0:12} -> ${current_commit:0:12} — will use --no-cache"
        return 0  # changed
    fi

    echo "  CRIU src unchanged (${current_commit:0:12})"
    return 1  # not changed
}

# Record current CRIU commit after successful base build
record_criu_commit() {
    git -C "${CRIU_SRC}" rev-parse HEAD > "$CRIU_BASE_COMMIT_FILE" 2>/dev/null || true
}

build_base() {
    echo "=== Building BASE image (CRIU, system deps) ==="
    echo "This takes ~5 minutes but caches well"
    echo "Image: ${BASE_IMAGE}"
    echo "CRIU source: ${CRIU_SRC}"
    echo ""

    if [ ! -d "${CRIU_SRC}" ]; then
        # OSS clone-and-build path: no local ../criu checkout, so fetch the
        # public fork at the pinned ref into a repo-local cache.
        CRIU_SRC="${PROJECT_ROOT}/.criu-src-cache"
        echo "No local CRIU checkout; using public fork ${NVSNAP_CRIU_REPO} (${NVSNAP_CRIU_REF})"
        # Shallow-fetch the pinned ref (works for a SHA or a branch/tag name;
        # a plain `clone -b` would reject a SHA). Init a bare-ish cache repo on
        # first run, then fetch + detach onto the ref.
        if [ ! -d "${CRIU_SRC}/.git" ]; then
            rm -rf "${CRIU_SRC}"; mkdir -p "${CRIU_SRC}"
            git -C "${CRIU_SRC}" init -q
            git -C "${CRIU_SRC}" remote add origin "${NVSNAP_CRIU_REPO}"
        fi
        git -C "${CRIU_SRC}" fetch --depth 1 origin "${NVSNAP_CRIU_REF}" && \
            git -C "${CRIU_SRC}" checkout -q FETCH_HEAD || {
                echo "ERROR: fetch ${NVSNAP_CRIU_REF} from ${NVSNAP_CRIU_REPO} failed"; exit 1; }
    fi

    # Smart --no-cache: auto-detect if CRIU source changed
    local cache_flag=""
    if [ "${NO_CACHE:-0}" = "1" ]; then
        cache_flag="--no-cache"
        echo "Building with --no-cache (forced via NO_CACHE=1)"
    elif criu_src_changed; then
        cache_flag="--no-cache"
    fi

    # Create build context for base
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    cp "${PROJECT_ROOT}/docker/agent/Dockerfile.base" "${BUILD_CTX}/Dockerfile"
    # Use git archive to get clean source without build artifacts
    mkdir -p "${BUILD_CTX}/criu-src"
    git -C "${CRIU_SRC}" archive HEAD | tar -x -C "${BUILD_CTX}/criu-src"

    # Copy cuda-checkpoint (required by Dockerfile.base)
    if [ -f "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" ]; then
        cp "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" "${BUILD_CTX}/cuda-checkpoint"
    else
        echo "ERROR: cuda-checkpoint not found at ${PROJECT_ROOT}/docker/agent/cuda-checkpoint"
        exit 1
    fi

    # Wrapper script — Dockerfile.base COPYs it into /criu-bundle/cuda-checkpoint
    # (the wrapper that the CRIU plugin actually invokes via PATH).
    cp "${PROJECT_ROOT}/docker/agent/cuda-checkpoint-wrapper.sh" "${BUILD_CTX}/cuda-checkpoint-wrapper.sh"

    docker build \
        $cache_flag \
        --platform linux/amd64 \
        -t "${BASE_IMAGE}" \
        "${BUILD_CTX}"

    # Verify the CRIU binary has expected PIE restorer strings
    echo ""
    echo "Verifying CRIU binary..."
    local verify_dir
    verify_dir=$(mktemp -d /tmp/criu-verify-XXXXXX)
    local verify_id
    verify_id=$(docker create "${BASE_IMAGE}" 2>&1)
    docker cp "$verify_id:/criu-bundle/criu" "$verify_dir/criu" 2>&1
    docker rm "$verify_id" > /dev/null 2>&1

    if [ ! -s "$verify_dir/criu" ]; then
        echo "  ERROR: Failed to extract CRIU binary from ${BASE_IMAGE}"
        rm -rf "$verify_dir"
        exit 1
    fi

    local verify_fail=0
    for pattern in "io_uring"; do  # io_uring support marker, present in both criu forks
        if ! grep -qa "$pattern" "$verify_dir/criu"; then
            echo "  FAIL: missing '$pattern' in CRIU binary"
            verify_fail=1
        fi
    done
    rm -rf "$verify_dir"

    if [ "$verify_fail" = "1" ]; then
        echo "ERROR: CRIU binary verification failed — PIE blob may be stale"
        exit 1
    fi
    echo "  OK: CRIU binary verified"

    # Record commit for future change detection
    record_criu_commit

    echo ""
    echo "Base image built: ${BASE_IMAGE}"
}

build_app() {
    echo "=== Building APP image (Go binaries, intercept lib) ==="
    echo "Base: ${BASE_IMAGE}"
    echo "Image: ${APP_IMAGE}"
    echo ""

    # Warn about untracked .c files in intercept library
    local untracked
    untracked=$(cd "$PROJECT_ROOT" && git ls-files --others --exclude-standard lib/nvsnap_intercept/src/*.c 2>/dev/null)
    if [ -n "$untracked" ]; then
        echo "WARNING: Untracked .c files in intercept library: $untracked"
        echo "  These will be missing from the Docker build context!"
        echo "  Run: git add $untracked"
    fi

    # Verify base image CRIU binary
    echo "Verifying base image CRIU binary..."
    local verify_id
    verify_id=$(docker create "${BASE_IMAGE}" 2>/dev/null)
    if [ -z "$verify_id" ]; then
        echo "ERROR: Base image ${BASE_IMAGE} not found locally or in registry"
        echo "Run './scripts/build-agent.sh base' first, or './scripts/build-agent.sh full-cycle'"
        exit 1
    fi
    local verify_dir
    verify_dir=$(mktemp -d /tmp/criu-verify-XXXXXX)
    docker cp "$verify_id:/criu-bundle/criu" "$verify_dir/criu" 2>&1
    docker rm "$verify_id" > /dev/null 2>&1
    if [ ! -s "$verify_dir/criu" ] || ! grep -qaE "sq_entries|Restoring id" "$verify_dir/criu"; then
        rm -rf "$verify_dir"
        echo "ERROR: Base image CRIU binary missing or missing expected strings"
        echo "Run './scripts/build-agent.sh base' to rebuild, or './scripts/build-agent.sh full-cycle'"
        exit 1
    fi
    rm -rf "$verify_dir"
    echo "  OK: base CRIU verified"
    echo ""

    # Create build context for app
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    cp "${PROJECT_ROOT}/docker/agent/Dockerfile.app" "${BUILD_CTX}/Dockerfile"

    # Copy Go project
    rsync -a \
        --exclude='.git' \
        --exclude='vendor' \
        --exclude='bin' \
        --exclude='docker' \
        --exclude='placeholder' \
        --exclude='*.tar.gz' \
        "${PROJECT_ROOT}/" "${BUILD_CTX}/"

    # Copy intercept library source
    cp -r "${PROJECT_ROOT}/lib/nvsnap_intercept" "${BUILD_CTX}/lib/"

    # Copy cuda-checkpoint binary and wrapper
    if [ -f "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" ]; then
        cp "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" "${BUILD_CTX}/"
    elif [ -f "${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint" ]; then
        cp "${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint" "${BUILD_CTX}/"
    else
        echo "ERROR: cuda-checkpoint not found!"
        exit 1
    fi
    cp "${PROJECT_ROOT}/docker/agent/cuda-checkpoint-wrapper.sh" "${BUILD_CTX}/"

    # Support NO_CACHE=1 environment variable to force rebuild
    local cache_flag=""
    if [ "${NO_CACHE:-0}" = "1" ]; then
        cache_flag="--no-cache"
        echo "Building with --no-cache (forced rebuild)"
    fi

    docker build \
        $cache_flag \
        --platform linux/amd64 \
        --build-arg BASE_IMAGE="${BASE_IMAGE}" \
        --build-arg UVLOOP_IMAGE="${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}" \
        --build-arg LIBUV_IMAGE="${REGISTRY}/libuv-builder:${NVSNAP_LIBUV_VERSION}" \
        --build-arg LIBZMQ_IMAGE="${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}" \
        -t "${APP_IMAGE}" \
        "${BUILD_CTX}"

    # Verify final app image CRIU binary
    echo ""
    echo "Verifying app image CRIU binary..."
    local app_verify_dir
    app_verify_dir=$(mktemp -d /tmp/criu-verify-XXXXXX)
    local app_verify_id
    app_verify_id=$(docker create "${APP_IMAGE}" 2>&1)
    docker cp "$app_verify_id:/criu-bundle/criu" "$app_verify_dir/criu" 2>&1
    docker rm "$app_verify_id" > /dev/null 2>&1
    if [ ! -s "$app_verify_dir/criu" ] || ! grep -qaE "sq_entries|Restoring id" "$app_verify_dir/criu"; then
        rm -rf "$app_verify_dir"
        echo "ERROR: App image CRIU binary verification failed"
        exit 1
    fi
    rm -rf "$app_verify_dir"
    echo "  OK: app CRIU verified"

    echo ""
    echo "App image built: ${APP_IMAGE}"
}

push_base() {
    echo "Pushing base image: ${BASE_IMAGE}"
    docker push "${BASE_IMAGE}"
    echo "Done."
}

push_app() {
    echo "Pushing app image: ${APP_IMAGE}"
    docker push "${APP_IMAGE}"

    # Auto-build and push nvsnap-init with matching agent version.
    # nvsnap-init bundles the agent's intercept lib + patched dependencies.
    # Using the same tag as the agent prevents build-ID mismatch on restore.
    local INIT_IMAGE="${REGISTRY}/nvsnap-init:${APP_VERSION}"
    echo ""
    echo "Building nvsnap-init: ${INIT_IMAGE}"
    # --network=host so the build container's apt-get can reach
    # archive.ubuntu.com via the host's DNS + proxy config. Without
    # this, on a corp VPN the build container ends up in an isolated
    # bridge network with no working DNS for ubuntu.com mirrors.
    docker build --no-cache --network=host \
        -t "${INIT_IMAGE}" \
        --build-arg UVLOOP_IMAGE="${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}" \
        --build-arg LIBUV_IMAGE="${REGISTRY}/libuv-builder:${NVSNAP_LIBUV_VERSION}" \
        --build-arg LIBZMQ_IMAGE="${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}" \
        --build-arg PYZMQ_IMAGE="${REGISTRY}/pyzmq-builder:${NVSNAP_PYZMQ_VERSION}" \
        --build-arg AGENT_IMAGE="${APP_IMAGE}" \
        -f "${PROJECT_ROOT}/docker/init/Dockerfile" \
        "${PROJECT_ROOT}"

    echo "Pushing nvsnap-init: ${INIT_IMAGE}"
    docker push "${INIT_IMAGE}"
    echo "Done."
}

deploy() {
    : "${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"
    echo "=== Deploying agent to k8s ==="
    echo "Image: ${APP_IMAGE}"

    # Update daemonset image. Set BOTH the main agent container AND the
    # nvsnap-bundle-stage init container: the init stages the /nvsnap tool
    # bundle (restore-entrypoint, nvsnap-rootfs-restore, ...) from its own
    # image's /criu-bundle. If left at an older tag it stages a stale
    # bundle and newly-added tools never reach the nodes (silently broke
    # nvsnap-rootfs-restore staging on v0.0.69 until caught). The Helm
    # template already pins both to nvsnap.agent.image; mirror that here.
    kubectl set image daemonset/nvsnap-agent -n nvsnap-system \
        agent="${APP_IMAGE}" nvsnap-bundle-stage="${APP_IMAGE}"

    # Restart pods
    kubectl rollout restart daemonset/nvsnap-agent -n nvsnap-system

    echo "Waiting for rollout..."
    kubectl rollout status daemonset/nvsnap-agent -n nvsnap-system --timeout=120s

    echo ""
    echo "=== Agent pods ==="
    kubectl get pods -n nvsnap-system -l app=nvsnap-agent
}

bump_version() {
    local current="$NVSNAP_APP_VERSION"
    local suffix=""

    # Handle optional suffix: v0.9.26-ghost-cleanup -> base=v0.9.26, suffix=ghost-cleanup
    # Also handle plain: v0.9.26 -> base=v0.9.26, suffix=""
    local base="$current"
    if [[ "$current" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)-(.+)$ ]]; then
        base="${BASH_REMATCH[1]}"
        suffix="${BASH_REMATCH[2]}"
    fi

    local prefix patch new_patch new_version
    prefix="${base%.*}"
    patch="${base##*.}"
    new_patch=$((patch + 1))
    new_version="${prefix}.${new_patch}"

    # Prompt for suffix (default: keep current)
    if [ -n "$suffix" ]; then
        echo "Current: ${current} (base=${base}, suffix=${suffix})"
    else
        echo "Current: ${current}"
    fi

    # Accept suffix as $2 or via BUMP_SUFFIX env var
    local new_suffix="${2:-${BUMP_SUFFIX:-$suffix}}"
    if [ -n "$new_suffix" ]; then
        new_version="${new_version}-${new_suffix}"
    fi

    echo "Bumping app version: ${current} -> ${new_version}"

    # Update versions.sh in place — escape special chars in current version for sed
    local escaped_current
    escaped_current=$(printf '%s\n' "$current" | sed 's/[[\.*^$()+?{|]/\\&/g')
    sed -i "s|NVSNAP_APP_VERSION:-${escaped_current}|NVSNAP_APP_VERSION:-${new_version}|" "${SCRIPT_DIR}/versions.sh"

    echo "Updated scripts/versions.sh"
    echo ""
    echo "New version: ${new_version}"
    echo "Run './scripts/build-agent.sh sync-versions' to update manifests, or"
    echo "Run './scripts/build-agent.sh build-and-deploy' to build + push + deploy."
}

sync_versions() {
    echo "=== Syncing versions from versions.sh to manifests ==="
    echo "  APP_VERSION:   ${APP_VERSION}"
    echo "  BASE_VERSION:  ${BASE_VERSION}"
    echo "  UVLOOP:        ${NVSNAP_UVLOOP_VERSION}"
    echo "  LIBZMQ:        ${NVSNAP_LIBZMQ_VERSION}"
    echo "  PYZMQ:         ${NVSNAP_PYZMQ_VERSION}"
    echo "  VLLM:          ${NVSNAP_VLLM_VERSION}"
    echo ""

    local files_updated=0

    # --- Dockerfile.app: BASE_IMAGE default ---
    local dockerfile="${PROJECT_ROOT}/docker/agent/Dockerfile.app"
    if [ -f "$dockerfile" ]; then
        sed -i "s|ARG BASE_IMAGE=nvcr.io/0651155215864979/ncp-dev/nvsnap-agent-base:[^ ]*|ARG BASE_IMAGE=${REGISTRY}/nvsnap-agent-base:${BASE_VERSION}|" "$dockerfile"
        echo "  Updated: docker/agent/Dockerfile.app"
        files_updated=$((files_updated + 1))
    fi

    # --- agent-daemonset*.yaml: agent image (main + CRI-O variant) ---
    for daemonset in "${PROJECT_ROOT}"/deploy/k8s/agent-daemonset*.yaml; do
        if [ -f "$daemonset" ]; then
            sed -i "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:.*|image: ${REGISTRY}/nvsnap-agent:${APP_VERSION}|" "$daemonset"
            echo "  Updated: deploy/k8s/$(basename "$daemonset")"
            files_updated=$((files_updated + 1))
        fi
    done

    # --- All workload manifests under deploy/k8s/workloads/ ---
    #
    # Substitute every versioned image (nvsnap-*, vllm-openai, builder
    # images). Workloads now live in the workloads/ subdirectory; the
    # previous glob deploy/k8s/vllm-*.yaml silently missed nim/sglang/
    # trtllm and everything under workloads/.
    sync_workload_manifest() {
        local manifest="$1"
        sed -i \
            -e "s|image: nvcr.io/0651155215864979/ncp-dev/uvloop-builder:[^ ]*|image: ${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}|" \
            -e "s|image: nvcr.io/0651155215864979/ncp-dev/libzmq-builder:[^ ]*|image: ${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}|" \
            -e "s|image: nvcr.io/0651155215864979/ncp-dev/pyzmq-builder:[^ ]*|image: ${REGISTRY}/pyzmq-builder:${NVSNAP_PYZMQ_VERSION}|" \
            -e "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:[^ ]*|image: ${REGISTRY}/nvsnap-agent:${APP_VERSION}|" \
            -e "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-init:[^ ]*|image: ${REGISTRY}/nvsnap-init:${NVSNAP_INIT_VERSION}|" \
            -e "s|image: vllm/vllm-openai:[^ ]*|image: vllm/vllm-openai:${NVSNAP_VLLM_VERSION}|" \
            "$manifest"
        # Restore manifests reference the agent's checkpoint hostPath.
        case "$(basename "$manifest")" in
            *restore*)
                sed -i "s|path: /var/lib/.*/checkpoints|path: ${NVSNAP_CHECKPOINT_HOST_PATH}|" "$manifest"
                ;;
        esac
    }
    for manifest in "${PROJECT_ROOT}"/deploy/k8s/workloads/*.yaml; do
        [ -f "$manifest" ] || continue
        sync_workload_manifest "$manifest"
        echo "  Updated: deploy/k8s/workloads/$(basename "$manifest")"
        files_updated=$((files_updated + 1))
    done
    # Server-side restore templates (used by the REST API + UI).
    for manifest in "${PROJECT_ROOT}"/internal/server/manifests/*restore*.yaml; do
        [ -f "$manifest" ] || continue
        sed -i "s|path: /var/lib/.*/checkpoints|path: ${NVSNAP_CHECKPOINT_HOST_PATH}|" "$manifest"
    done

    # --- nvsnap-server.yaml: server image ---
    local server_manifest="${PROJECT_ROOT}/deploy/k8s/nvsnap-server.yaml"
    if [ -f "$server_manifest" ] && [ -n "${NVSNAP_SERVER_VERSION:-}" ]; then
        sed -i "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-server:[^ ]*|image: ${REGISTRY}/nvsnap-server:${NVSNAP_SERVER_VERSION}|" "$server_manifest"
        echo "  Updated: deploy/k8s/nvsnap-server.yaml"
        files_updated=$((files_updated + 1))
    fi

    # --- nvsnap-blobstore.yaml: blobstore image ---
    local blobstore_manifest="${PROJECT_ROOT}/deploy/k8s/nvsnap-blobstore.yaml"
    if [ -f "$blobstore_manifest" ] && [ -n "${NVSNAP_BLOBSTORE_VERSION:-}" ]; then
        sed -i "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-blobstore:[^ ]*|image: ${REGISTRY}/nvsnap-blobstore:${NVSNAP_BLOBSTORE_VERSION}|" "$blobstore_manifest"
        echo "  Updated: deploy/k8s/nvsnap-blobstore.yaml"
        files_updated=$((files_updated + 1))
    fi

    echo ""
    echo "Synced ${files_updated} files. Run 'git diff' to review changes."
}

show_versions() {
    echo "=== NVSNAP Versions (from scripts/versions.sh) ==="
    echo ""
    echo "  Registry:      ${REGISTRY}"
    echo "  Base image:    ${REGISTRY}/nvsnap-agent-base:${BASE_VERSION}"
    echo "  App image:     ${REGISTRY}/nvsnap-agent:${APP_VERSION}"
    echo ""
    echo "  Dependencies:"
    echo "    uvloop:      ${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}"
    echo "    libzmq:      ${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}"
    echo "    pyzmq:       ${REGISTRY}/pyzmq-builder:${NVSNAP_PYZMQ_VERSION}"
    echo "    vllm:        vllm/vllm-openai:${NVSNAP_VLLM_VERSION}"
    echo ""
    echo "  DOCKER_HOST:   ${DOCKER_HOST:-<not set>}"
    echo "  KUBECONFIG:    ${KUBECONFIG}"

    # Show CRIU source tracking
    echo ""
    echo "  CRIU source:   ${CRIU_SRC}"
    if [ -f "$CRIU_BASE_COMMIT_FILE" ]; then
        local recorded current
        recorded=$(cat "$CRIU_BASE_COMMIT_FILE")
        current=$(git -C "${CRIU_SRC}" rev-parse HEAD 2>/dev/null || echo "unknown")
        echo "    Last base build: ${recorded:0:12}"
        echo "    Current HEAD:    ${current:0:12}"
        if [ "$recorded" != "$current" ]; then
            echo "    Status:          CHANGED (base rebuild needed)"
        else
            echo "    Status:          up to date"
        fi
    else
        echo "    Status:          no base build recorded yet"
    fi
}

build_and_deploy() {
    echo "=== Build and Deploy Pipeline ==="
    echo ""
    sync_versions
    echo ""
    build_app
    echo ""
    push_app
    echo ""
    deploy
    echo ""
    echo "=== Pipeline complete ==="
    echo "Run './scripts/test-vllm-zmq.sh' to test."
}

full_cycle() {
    echo "=== Full Cycle: Base + App + Deploy ==="
    echo ""
    sync_versions
    echo ""
    build_base
    echo ""
    push_base
    echo ""
    build_app
    echo ""
    push_app
    echo ""
    deploy
    echo ""
    echo "=== Full cycle complete ==="
    echo "Run './scripts/test-e2e.sh sglang-small' to test."
}

case "${1:-help}" in
    base)
        build_base
        ;;
    app)
        build_app
        ;;
    all)
        build_base
        build_app
        ;;
    push-base)
        push_base
        ;;
    push-app)
        push_app
        ;;
    deploy)
        deploy
        ;;
    bump)
        bump_version
        ;;
    sync-versions)
        sync_versions
        ;;
    build-and-deploy)
        build_and_deploy
        ;;
    full-cycle)
        full_cycle
        ;;
    show-versions)
        show_versions
        ;;
    help|--help|-h)
        usage
        ;;
    *)
        echo "Unknown command: $1"
        usage
        exit 1
        ;;
esac
