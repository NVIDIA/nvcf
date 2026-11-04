#!/bin/bash
# Usage: ./upgrade-test.sh [COLIMA_PROFILE] [OPENBAO_VERSION] [IMAGE_TAG]
# Examples:
#   ./upgrade-test.sh amd64 2.5.1
#   ./upgrade-test.sh amd64 2.5.1 2.5.1-nv-1
#   ./upgrade-test.sh kubernetes 2.5.1
# Defaults: COLIMA_PROFILE=amd64, OPENBAO_VERSION=2.5.1, IMAGE_TAG=<version>-nv-1

set -euo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"
source "$REPO_ROOT/utils/utils.sh"

COLIMA_PROFILE="${1:-amd64}"
UPGRADE_VERSION="${2:-2.5.1}"
LOCAL_TEST_TAG="${3:-${UPGRADE_VERSION}-nv-1}"  # Default to the published production tag
CURRENT_VERSION="2.2.2-nv-1"
IMAGE_NAME="<your-registry>/<your-org>/nvcf-openbao"

log_section "OpenBao Upgrade Test: ${CURRENT_VERSION} -> ${UPGRADE_VERSION} (Colima profile: ${COLIMA_PROFILE})"

# Step 1: Switch Docker context to Colima profile
log_step "Switching Docker context to colima-${COLIMA_PROFILE}..."
ORIGINAL_CONTEXT=$(docker context show)
ORIGINAL_DOCKER_HOST="${DOCKER_HOST:-}"

# Unset DOCKER_HOST so context switch works
unset DOCKER_HOST

# Trap to restore context and DOCKER_HOST on exit
trap "docker context use ${ORIGINAL_CONTEXT} 2>/dev/null || true; export DOCKER_HOST='${ORIGINAL_DOCKER_HOST}'" EXIT

docker context use colima-${COLIMA_PROFILE}

if [ $? -ne 0 ]; then
    log_error "Failed to switch Docker context to colima-${COLIMA_PROFILE}"
    exit 1
fi
log_success "Docker context switched to colima-${COLIMA_PROFILE}"

# Step 2: Pull upgrade image into Colima's containerd
IMAGE_TAG="${IMAGE_NAME}:${LOCAL_TEST_TAG}"

log_info "Pulling upgrade image: ${IMAGE_TAG}"

if docker images --format "{{.Repository}}:{{.Tag}}" | grep -q "^${IMAGE_TAG}$"; then
    log_info "Image ${IMAGE_TAG} already present locally, skipping pull"
    log_info "To force re-pull: docker rmi ${IMAGE_TAG}"
else
    log_step "Pulling OpenBao v${UPGRADE_VERSION} image from registry..."
    docker pull "${IMAGE_TAG}"

    if [ $? -ne 0 ]; then
        log_error "Failed to pull image ${IMAGE_TAG} — ensure the image has been built and pushed by the nvcf-openbao pipeline"
        trap - EXIT
        docker context use ${ORIGINAL_CONTEXT} > /dev/null 2>&1 || true
        exit 1
    fi
    log_success "Image pulled successfully (available in Colima ${COLIMA_PROFILE})"
fi

# Step 3: Verify current deployment
log_step "Verifying current deployment..."
kubectl get pods -n vault-system

# Step 4: Show current versions and check if upgrade is needed
log_info "Current OpenBao versions:"
CURRENT_RUNNING_VERSION=""
for i in 0 1 2; do
    version=$(kubectl exec openbao-server-${i} -n vault-system -- bao version 2>/dev/null | head -1)
    echo "  openbao-server-${i}: ${version}"

    # Extract version number (e.g., "v2.2.2" from "OpenBao v2.2.2 (...)")
    if [ -z "$CURRENT_RUNNING_VERSION" ]; then
        CURRENT_RUNNING_VERSION=$(echo "$version" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+')
    fi
done

# Fail fast if version cannot be determined
if [ -z "$CURRENT_RUNNING_VERSION" ]; then
    log_error "Unable to determine current OpenBao version from server pods; aborting upgrade."
    exit 1
fi

# Check if already running target version
TARGET_VERSION="v${UPGRADE_VERSION}"
if [ "$CURRENT_RUNNING_VERSION" == "$TARGET_VERSION" ]; then
    log_info ""
    log_info "Cluster is already running OpenBao ${TARGET_VERSION}"
    log_info "Nothing to upgrade. Exiting..."
    trap - EXIT  # Clear trap to avoid double restore
    docker context use ${ORIGINAL_CONTEXT} > /dev/null 2>&1 || true
    export DOCKER_HOST="${ORIGINAL_DOCKER_HOST}"
    exit 0
fi

# Check if running unexpected version
EXPECTED_VERSION="v2.2.2"
if [ "$CURRENT_RUNNING_VERSION" != "$EXPECTED_VERSION" ] && [ "$CURRENT_RUNNING_VERSION" != "$TARGET_VERSION" ]; then
    log_warn ""
    log_warn "Warning: Cluster is running ${CURRENT_RUNNING_VERSION}, but expected ${EXPECTED_VERSION}"
    log_warn "This script is designed to upgrade from ${CURRENT_VERSION} -> ${UPGRADE_VERSION}"
    read -p "Do you want to continue anyway? (yes/no): " CONTINUE
    if [ "$CONTINUE" != "yes" ]; then
        log_info "Upgrade cancelled"
        trap - EXIT  # Clear trap to avoid double restore
        docker context use ${ORIGINAL_CONTEXT} > /dev/null 2>&1 || true
        export DOCKER_HOST="${ORIGINAL_DOCKER_HOST}"
        exit 0
    fi
fi

log_info ""
log_info "Proceeding with upgrade: ${CURRENT_RUNNING_VERSION} -> ${TARGET_VERSION}"

# Step 5: Prepare upgrade values
log_step "Creating upgrade values file..."
log_info "Using local test tag for Helm upgrade: ${LOCAL_TEST_TAG}"
cat > /tmp/upgrade-values.yaml <<EOF
openbao:
  server:
    # Enable RollingUpdate for automatic pod restarts
    updateStrategyType: RollingUpdate

    image:
      registry: nvcr.io
      repository: <your-org>/nvcf-openbao
      tag: ${LOCAL_TEST_TAG}
      pullPolicy: IfNotPresent

    # Graceful leader stepdown before deletion (for server pods)
    lifecycle:
      preStop:
        exec:
          command:
            - /bin/sh
            - -c
            - |
              # Get root token from secret
              ROOT_TOKEN=\$(cat /home/openbao/unseal/root_token 2>/dev/null || echo "")
              if [ -z "\$ROOT_TOKEN" ]; then
                echo "Warning: Root token not found, cannot step down"
                exit 0
              fi

              # Check if this pod is the active leader
              export BAO_ADDR="http://127.0.0.1:8200"
              export BAO_TOKEN="\$ROOT_TOKEN"

              STATUS=\$(bao status -format=json 2>/dev/null || echo "{}")
              HA_MODE=\$(echo "\$STATUS" | jq -r '.ha_mode // "unknown"')

              if [ "\$HA_MODE" = "active" ]; then
                echo "Pod is active leader, stepping down gracefully..."
                bao operator step-down || true
                sleep 5
                echo "Leader stepdown complete"
              else
                echo "Pod is not leader (mode: \$HA_MODE), no stepdown needed"
              fi

    # Update auto-unseal sidecar image (for server pods)
    extraContainers:
      - name: auto-unseal-sidecar
        image: ${IMAGE_NAME}:${LOCAL_TEST_TAG}
        resources:
          requests:
            cpu: 50m
            memory: 64Mi
          limits:
            cpu: 250m
            memory: 256Mi
        volumeMounts:
          - name: openbao-server-unseal
            mountPath: /vault/userconfig/unseal
            readOnly: true
        command: ["/bin/sh", "-c"]
        args:
          - |
            echo "Starting auto-unseal monitor..."
            echo http://\$HOSTNAME:8200
            export BAO_ADDR=http://\$HOSTNAME:8200
            while true; do
              if [ -f /vault/userconfig/unseal/unseal_key ]; then
                UNSEAL_KEY=\$(cat /vault/userconfig/unseal/unseal_key)
                if [ ! -z "\$UNSEAL_KEY" ]; then
                    bao operator unseal \$UNSEAL_KEY
                    sleep 60
                    continue
                else
                  echo "Unseal key is empty, waiting..."
                fi
              else
                echo "Unseal key file not found, waiting..."
              fi
              sleep 10
            done

  # Update injector agent image (image used when injecting sidecars into app pods)
  injector:
    # Disable anti-affinity for single-node clusters (allows rolling update to work)
    affinity: ""
    agentImage:
      registry: nvcr.io
      repository: <your-org>/nvcf-openbao
      tag: ${LOCAL_TEST_TAG}
      pullPolicy: IfNotPresent
EOF

log_success "Upgrade values created at /tmp/upgrade-values.yaml"

# Step 6: Instructions before upgrade
log_section "Ready to upgrade!"
echo ""
echo "Before pressing Enter, open separate terminals for monitoring:"
echo ""
echo "  Terminal 1: ./tests/openbao/loadbalancer_ha_kv_rw_check.sh ${COLIMA_PROFILE}"
echo "              (Keep running - shows KV read/write health during upgrade)"
echo ""
echo "  Terminal 2: ./tests/openbao/loadbalancer_ha_seal_status_check.sh ${COLIMA_PROFILE}"
echo "              (Keep running - shows seal status during upgrade)"
echo ""
echo "  Terminal 3: kubectl get pods -n vault-system -w"
echo "              (Optional - watch pods restart in real-time)"
echo ""
echo "Expected behavior during upgrade:"
echo "  - LoadBalancer tests show <1% errors (routes to healthy pods)"
echo "  - Pods restart one at a time (rolling update: 2 → 1 → 0)"
echo "  - Leader may change during upgrade"
echo "  - Seal status stays false throughout"
echo ""

read -p "Press Enter to start the upgrade, or Ctrl+C to cancel..."

# Step 7: Perform upgrade
log_step "Performing Helm upgrade..."
cd "$REPO_ROOT"
helm upgrade openbao-server helm -n vault-system \
    --values helm/values.yaml \
    --values values.local.yaml \
    --values /tmp/upgrade-values.yaml \
    --force-conflicts \
    --wait --wait-for-jobs --timeout 20m

if [ $? -ne 0 ]; then
    log_error "Upgrade failed!"
    exit 1
fi

log_success "Upgrade completed!"

# Step 8: Verify
log_step "Verifying upgraded versions..."
for i in 0 1 2; do
    version=$(kubectl exec openbao-server-${i} -n vault-system -- bao version 2>/dev/null | head -1)
    log_info "openbao-server-${i}: ${version}"
done

# Step 9: Restore original Docker context and DOCKER_HOST
log_step "Restoring Docker context to ${ORIGINAL_CONTEXT}..."
trap - EXIT  # Clear trap to avoid double restore
docker context use ${ORIGINAL_CONTEXT} > /dev/null 2>&1 || true
export DOCKER_HOST="${ORIGINAL_DOCKER_HOST}"

# Step 10: Show files that need manual update
log_step "Files to update for version ${UPGRADE_VERSION}"
echo ""
echo "  The following files contain version references that should be updated:"
echo ""

NEW_VERSION="${UPGRADE_VERSION}"
OLD_VERSION="${CURRENT_VERSION}"

cd "$REPO_ROOT"
FILES_NEEDING_UPDATE=""

# Check each file and show what needs updating
check_file() {
    local file=$1
    local desc=$2
    if [ -f "$file" ]; then
        if grep -q "$OLD_VERSION" "$file"; then
            echo "    - $file"
            echo "      Purpose: $desc"
            echo "      Update: ${OLD_VERSION} → ${NEW_VERSION}"
            echo ""
            FILES_NEEDING_UPDATE="${FILES_NEEDING_UPDATE} ${file}"
        fi
    fi
}

check_file "helm/Chart.yaml" "Helm chart appVersion (shown in 'helm list')"
check_file "helm/values.yaml" "Default image tags for production deployments"
check_file "values.local.yaml" "Local development image tags"
check_file "service-migrations/Dockerfile" "Base image for migration jobs"

if [ -z "$FILES_NEEDING_UPDATE" ]; then
    log_success "All files already at version ${NEW_VERSION}"
else
    echo ""
    log_warn "ACTION REQUIRED: Manually update the files above and create an MR"
fi

log_success "Upgrade test completed successfully!"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  SUMMARY"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  ✅ Local upgrade test passed: ${CURRENT_VERSION} → ${LOCAL_TEST_TAG}"
echo ""
echo "  Next steps to complete the upgrade:"
echo ""
echo "    1. Merge the nvcf-openbao MR to publish the ${UPGRADE_VERSION}-nv-1 image"
echo ""
echo "    2. Update version references in repository files (see above)"
echo ""
echo "    3. Create MR with changes in k8s-openbao:"
echo "       git checkout -b upgrade/openbao-${UPGRADE_VERSION}"
echo "       git add -A"
echo "       git commit -m 'feat: upgrade OpenBao from ${CURRENT_VERSION} to ${UPGRADE_VERSION}-nv-1'"
echo "       git push -u origin HEAD"
echo ""
echo "    4. After MR merge, deploy to environments"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"