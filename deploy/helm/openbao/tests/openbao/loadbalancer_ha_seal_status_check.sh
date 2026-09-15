#!/bin/bash

TEST_SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${TEST_SCRIPT_DIR}/../../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${TEST_SCRIPT_DIR}/../../utils"
    exit 1
fi
source "${TEST_SCRIPT_DIR}/../../utils/utils.sh"

# Usage
show_usage() {
    echo "Usage: $0 <COLIMA_PROFILE>"
    echo ""
    echo "Arguments:"
    echo "  COLIMA_PROFILE  Colima profile name (required)"
    echo ""
    echo "Examples:"
    echo "  $0 amd64"
    echo "  $0 default"
    exit 1
}

# Get Colima profile from argument (required)
if [[ "${1-}" == "-h" || "${1-}" == "--help" ]]; then
    show_usage
elif [[ -n "${1-}" ]]; then
    COLIMA_PROFILE="$1"
else
    echo "Error: COLIMA_PROFILE argument is required"
    echo ""
    show_usage
fi

# Check Colima is installed
if ! check_colima; then
    echo "Please install Colima before proceeding."
    exit 1
fi

# Verify Colima profile is running
if ! colima -p "$COLIMA_PROFILE" status >/dev/null 2>&1; then
    echo "Error: Colima profile '$COLIMA_PROFILE' is not running"
    echo "Start it with: colima start -p $COLIMA_PROFILE"
    exit 1
fi
echo "Using Colima profile: $COLIMA_PROFILE"

test_seal_ha_status_via_lb() {
    log_section "Starting continuous seal status check via LoadBalancer (Ctrl+C to stop)..."
    while true; do
        colima -p "$COLIMA_PROFILE" ssh -- curl -s http://${curr_ip}:8200/v1/sys/health | jq .sealed
        sleep 1
    done
}

# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! check_kubernetes; then
    exit 1
fi

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

# get loadbalancer IP (after k8s checks)
curr_ip=$(kubectl get svc openbao-server-lb -n vault-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null)
if [[ -z "$curr_ip" ]]; then
    echo "Error: LoadBalancer service 'openbao-server-lb' not found"
    echo ""
    echo "To set up the LoadBalancer, run:"
    echo "  ./utils/lb.sh"
    exit 1
fi
echo "Using LoadBalancer IP: $curr_ip"

# Run infinite loop (Ctrl+C to stop)
test_seal_ha_status_via_lb