#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../utils"
    exit 1
fi
source "$SCRIPT_DIR/../utils/utils.sh"

# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

# Build the command to execute inside the pod
COMMAND="env BAO_TOKEN=$(get_root_token "$namespace" $statefulset) bao $@"
ECHO_COMMAND="env BAO_TOKEN=<redacted> bao $@"
# Execute the command inside the OpenBao pod using kubectl
echo "kubectl exec -n $namespace $statefulset-0 -c openbao -- $ECHO_COMMAND"
kubectl exec -n $namespace $statefulset-2 -c openbao -- $COMMAND
