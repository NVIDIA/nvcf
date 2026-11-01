#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

# source utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/utils.sh"

# test configuration
TEST_NAME="ess-agent-sigterm-test"
GRACE_PERIOD=60
MAX_EXPECTED_TIME=5

echo -e "${BLUE}=== ess agent sigterm handling test ===${NC}"

# cleanup function
cleanup() {
    log "cleaning up..."
    kubectl delete pod $TEST_NAME --ignore-not-found=true --grace-period=0 --force 2>/dev/null || true
    kubectl delete configmap ${TEST_NAME}-config ${TEST_NAME}-templates --ignore-not-found=true 2>/dev/null || true
}
trap cleanup EXIT

# check prerequisites
if ! command -v kubectl &> /dev/null || ! kubectl cluster-info &> /dev/null; then
    log_error "kubectl not available or not connected to cluster"
    exit 1
fi

# create test manifest
log "creating test resources..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: $TEST_NAME
spec:
  terminationGracePeriodSeconds: $GRACE_PERIOD
  containers:
  - name: ess-agent
    image: ess-agent/distroless-go-multi-arch:latest
    imagePullPolicy: Never
    command: ["/bin/ess-agent"]
    args: ["-config=/config/test-config", "-log-level=TRACE"]
    volumeMounts:
    - name: config-volume
      mountPath: /config
    - name: templates-volume
      mountPath: /ess-agent/file/templates
    - name: secrets-volume
      mountPath: /ess-agent/file/secrets
    env:
    - name: SECRET_PATH
      value: "functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets"
  volumes:
  - name: config-volume
    configMap:
      name: ${TEST_NAME}-config
  - name: templates-volume
    configMap:
      name: ${TEST_NAME}-templates
  - name: secrets-volume
    emptyDir: {}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${TEST_NAME}-config
data:
  test-config: |
    ess {
      address = "http://host.docker.internal:3002"
      namespace = "nvcf"
      ess_agent_token_file = "./jwt.token"
      default_lease_duration = "30s"
      lease_renewal_threshold = 0.80
    }
    template {
      source = "/ess-agent/file/templates/mockoon-example.tmpl"
      destination = "/ess-agent/file/secrets/mockoon-example.json"
    }
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${TEST_NAME}-templates
data:
  mockoon-example.tmpl: |
    {{- \$secretPath := env "SECRET_PATH" }}
    {{- with secret \$secretPath -}}
    {{ .Data.data | toJSON }}
    {{- end }}
EOF

# wait for pod to be ready
log "waiting for pod to be ready..."
if ! kubectl wait --for=condition=Ready pod/$TEST_NAME --timeout=120s; then
    log_error "pod failed to become ready"
    kubectl get pod $TEST_NAME -o wide
    kubectl logs $TEST_NAME --tail=10
    exit 1
fi

# let pod run briefly
sleep 10

# test sigterm handling
log "testing sigterm handling..."
START_TIME=$(date +%s)

if timeout $((GRACE_PERIOD + 10)) kubectl delete pod $TEST_NAME --grace-period=$GRACE_PERIOD; then
    END_TIME=$(date +%s)
    TOTAL_TIME=$((END_TIME - START_TIME))

    if [ $TOTAL_TIME -le $MAX_EXPECTED_TIME ]; then
        log_success "sigterm handling works! terminated in ${TOTAL_TIME}s"
        echo -e "${GREEN}=== PASS ===${NC}"
        exit 0
    elif [ $TOTAL_TIME -ge $((GRACE_PERIOD - 5)) ]; then
        log_error "sigterm handling broken! took ${TOTAL_TIME}s"
        echo -e "${RED}=== FAIL ===${NC}"
        exit 1
    else
        log_warning "sigterm handling slow but working: ${TOTAL_TIME}s"
        echo -e "${YELLOW}=== SLOW ===${NC}"
        exit 2
    fi
else
    log_error "pod deletion failed or timed out"
    echo -e "${RED}=== ERROR ===${NC}"
    exit 1
fi