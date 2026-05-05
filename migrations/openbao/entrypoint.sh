#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

if [ -f "/app/migrations/utils/utils.sh" ]; then
  source /app/migrations/utils/utils.sh
else
  echo "ERROR: Utility file /app/migrations/utils/utils.sh not found!"
  return 1
fi

if [ -f "/app/migrations/utils/issuer_discovery.sh" ]; then
  source /app/migrations/utils/issuer_discovery.sh
else
  log_warn "Issuer discovery script not found, skipping..."
fi

# Unconditionally source functions.sh to ensure initialize_mount_lists is available.
if [ -f "/app/migrations/utils/functions.sh" ]; then
  source /app/migrations/utils/functions.sh
else
  log_error "Utility file /app/migrations/utils/functions.sh not found!"
  exit 1
fi

# Read root token from mounted K8s secret
if [ -f "/secrets/root_token/root_token" ]; then
  export BAO_TOKEN=$(cat /secrets/root_token/root_token)
else
  log_error "The root token not found at /secrets/root_token/root_token"
  exit 1
fi

# Use the service DNS name instead of individual pods
BAO_SERVICE="${BAO_SERVICE:-openbao-server.vault-system.svc.cluster.local}"
log_info "Using OpenBao service: $BAO_SERVICE"
SCHEME=http

# Check if the service is responsive
retry_count=0
MAX_RETRIES=5
RETRY_DELAY=5
while [ $retry_count -lt $MAX_RETRIES ]; do
  log_info "Attempt $(($retry_count + 1))/$MAX_RETRIES: Checking OpenBao health..."

  if curl -s -k -o /dev/null -w "%{http_code}" "$SCHEME://$BAO_SERVICE:8200/v1/sys/health" | grep -q "200\|429"; then
    log_info "OpenBao service is responsive!"
    break
  else
    log_info "OpenBao service not responsive. Retrying in $RETRY_DELAY seconds..."
    retry_count=$((retry_count + 1))
  fi
done

# Use the service to find the leader using raft list-peers
log_info "Querying for leader information..."
export BAO_ADDR="$SCHEME://$BAO_SERVICE:8200"
RAFT_INFO=$(bao operator raft list-peers -format=json)
LEADER_ADDRESS=$(echo "$RAFT_INFO" | jq -r '.data.config.servers[] | select(.leader == true) | .address')

if [ -z "$LEADER_ADDRESS" ] || [ "$LEADER_ADDRESS" == "null" ]; then
  log_error "Could not determine leader address"
  exit 1
fi

# Fix BAO_ADDR if it contains an extra port (like :8201)
if [[ "$LEADER_ADDRESS" == *":8201" ]]; then
  # Replace :8201:8200 with just :8200
  LEADER_ADDRESS=${LEADER_ADDRESS/:8201/:8200}
  log_info "LEADER_ADDRESS is to: $LEADER_ADDRESS"
fi

export BAO_ADDR="$SCHEME://$LEADER_ADDRESS"

# Now that BAO_ADDR is set, initialize the mount lists
initialize_mount_lists

# Execute core migrations (can be disabled for addon-only runs like rotation cronjobs)
if [ "${CORE_MIGRATIONS_ENABLED:-true}" = "true" ]; then
  log_section "Starting core migrations..."
  for migration in $(ls -1 /app/migrations/*.sh | sort); do
    log_section "Running migration: $migration"
    if ! bash -c "source $migration"; then
      log_error "Warning: Migration $migration failed, continuing with next"
    else
      log_success "Migration $migration completed successfully"
    fi
  done
  log_section "Core migrations completed"
else
  log_info "Core migrations disabled (CORE_MIGRATIONS_ENABLED=false), skipping..."
fi

# Execute addons based on environment variables
log_section "Processing addons..."

if [ "${ADDONS_LLS_ENABLED:-false}" = "true" ]; then
  log_section "LLS addon enabled, running setup..."
  if [ -f "/app/addons/lls/setup_lls.sh" ]; then
    if ! bash -c "source /app/addons/lls/setup_lls.sh"; then
      log_error "LLS addon setup failed"
    else
      log_success "LLS addon setup completed successfully"
    fi
  else
    log_warn "LLS addon script not found at /app/addons/lls/setup_lls.sh"
  fi
else
  log_info "LLS addon disabled (ADDONS_LLS_ENABLED != true), skipping..."
fi

log_section "All migrations and addons completed"
