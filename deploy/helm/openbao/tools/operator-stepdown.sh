#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../utils"
    exit 1
fi
source "$SCRIPT_DIR/../utils/utils.sh"

operator_stepdown() {
    local namespace=$1
    local statefulset=$2
    local root_token=$(get_root_token "$namespace" "$statefulset")

    kubectl exec -it $statefulset-0 -c openbao -n $namespace -- \
        env BAO_TOKEN=$root_token \
        bao operator step-down
}

# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

if ! operator_stepdown "$namespace" "$statefulset"; then
    log_error "Failed to issue operator stepdown"
    exit 1
fi
