#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../../utils"
    exit 1
fi
source "$SCRIPT_DIR/../../utils/utils.sh"

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

namespace="vault-system"
statefulset="openbao-server"

if ! check_kubernetes; then
    exit 1
fi

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

# Check if MetalLB is deployed
if ! kubectl get pods -n metallb-system >/dev/null 2>&1; then
  log_error "MetalLB is not deployed. Please deploy MetalLB using 'utils/lb.sh'."
  exit 1
fi

# Get loadbalancer IP
curr_ip=$(kubectl get svc openbao-server-lb -n vault-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null)
if [[ -z "$curr_ip" ]]; then
    log_error "LoadBalancer service 'openbao-server-lb' not found."
    echo ""
    echo "To set up the LoadBalancer, run:"
    echo "  ./utils/lb.sh"
    exit 1
fi
log_info "Using LoadBalancer IP: $curr_ip"

OPENBAO_ADDR="http://${curr_ip}:8200"
echo "Using OpenBao at: $OPENBAO_ADDR"

TOKEN=$(get_root_token $namespace $statefulset)

# Verify Colima profile is running
if ! colima -p "$COLIMA_PROFILE" status >/dev/null 2>&1; then
    log_error "Colima profile '$COLIMA_PROFILE' is not running. Start it with: colima start -p $COLIMA_PROFILE"
    exit 1
fi
log_info "Using Colima profile: $COLIMA_PROFILE"

while true; do
  TIMESTAMP=$(date +%s)
  # KVv2 mount path and secret name
  KV_MOUNT="services/all/kv"
  SECRET_NAME="test"
  RANDOM_VALUE="value-$RANDOM-$TIMESTAMP"

  log_section "$(date) - Starting test cycle #$TIMESTAMP"

  # Write with dynamic value (KVv2 API: /v1/{mount}/data/{path})
  log_info "WRITE: Creating secret at $KV_MOUNT/data/$SECRET_NAME with value: $RANDOM_VALUE"
  WRITE_RESULT=$(colima -p "$COLIMA_PROFILE" ssh -- curl -s -w "%{http_code}" \
    -H "X-Vault-Token: $TOKEN" \
    -X POST \
    -d "{\"data\":{\"timestamp\":\"$TIMESTAMP\",\"value\":\"$RANDOM_VALUE\"}}" \
    $OPENBAO_ADDR/v1/$KV_MOUNT/data/$SECRET_NAME)

  # Last 3 chars are the HTTP status code; the rest is body
  if [[ -z "$WRITE_RESULT" || ${#WRITE_RESULT} -lt 3 ]]; then
    log_error "Write failed: empty or invalid response"
    sleep 5
    continue
  fi
  WRITE_CODE="${WRITE_RESULT: -3}"
  WRITE_BODY="${WRITE_RESULT:0:${#WRITE_RESULT}-3}"
  if [[ "$WRITE_CODE" == "200" || "$WRITE_CODE" == "204" ]]; then
    log_success "Write successful"
  else
    log_error "Write failed: HTTP $WRITE_CODE, body: $WRITE_BODY"
    sleep 5
    continue
  fi

  # Read and verify value
  log_info "READ: Fetching secret from $KV_MOUNT/data/$SECRET_NAME"
  READ_RESULT=$(colima -p "$COLIMA_PROFILE" ssh -- curl -s \
    -H "X-Vault-Token: $TOKEN" \
    $OPENBAO_ADDR/v1/$KV_MOUNT/data/$SECRET_NAME)

  # Extract the value from the JSON response
  READ_VALUE=$(echo "$READ_RESULT" | grep -o "\"value\":\"$RANDOM_VALUE\"" || echo "")

  if [[ "$READ_VALUE" == "\"value\":\"$RANDOM_VALUE\"" ]]; then
    log_success "Read successful and value verified: $RANDOM_VALUE"
  else
    log_error "Read failed or value mismatch, Expected:  $RANDOM_VALUE, Response: $READ_RESULT"
  fi

  # Delete step (KVv2 API: DELETE /v1/{mount}/data/{path})
  log_info "DELETE: Removing secret at $KV_MOUNT/data/$SECRET_NAME"
  DELETE_RESULT=$(colima -p "$COLIMA_PROFILE" ssh -- curl -s -w "%{http_code}" \
    -H "X-Vault-Token: $TOKEN" \
    -X DELETE \
    $OPENBAO_ADDR/v1/$KV_MOUNT/data/$SECRET_NAME)

  # Last 3 chars are the HTTP status code; the rest is body
  if [[ -z "$DELETE_RESULT" || ${#DELETE_RESULT} -lt 3 ]]; then
    log_error "Delete failed: empty or invalid response"
    sleep 5
    continue
  fi
  DELETE_CODE="${DELETE_RESULT: -3}"
  DELETE_BODY="${DELETE_RESULT:0:${#DELETE_RESULT}-3}"
  if [[ "$DELETE_CODE" == "204" ]]; then
    log_success "Delete successful"
  else
    log_error "Delete failed: HTTP $DELETE_CODE, body: $DELETE_BODY"
  fi

  # Verify deletion (KV v2 returns 200 with deletion_time, not 404)
  VERIFY_RESULT=$(colima -p "$COLIMA_PROFILE" ssh -- curl -s -w "%{http_code}" \
    -H "X-Vault-Token: $TOKEN" \
    $OPENBAO_ADDR/v1/$KV_MOUNT/data/$SECRET_NAME)

  # Last 3 chars are the HTTP status code; the rest is body
  if [[ -z "$VERIFY_RESULT" || ${#VERIFY_RESULT} -lt 3 ]]; then
    log_error "Verify failed: empty or invalid response"
    sleep 5
    continue
  fi
  VERIFY_CODE="${VERIFY_RESULT: -3}"
  VERIFY_BODY="${VERIFY_RESULT:0:${#VERIFY_RESULT}-3}"
  # Check if either 404 (destroyed) or has deletion_time set (soft-deleted in KV v2)
  if [[ "$VERIFY_CODE" == "404" ]] || echo "$VERIFY_BODY" | grep -q '"deletion_time"'; then
    log_success "Deletion verified"
  else
    log_error "Deletion verification failed: HTTP $VERIFY_CODE, body: $VERIFY_BODY"
  fi

  sleep 5
done