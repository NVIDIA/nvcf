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


set -euo pipefail

curr_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${curr_dir}/utils/utils.sh" ]; then
    echo "Error: log.sh not found in ${curr_dir}/utils/utils.sh"
    return 1
else
  source "${curr_dir}/utils/utils.sh"
  source "${curr_dir}/utils/functions.sh"
  source "${curr_dir}/utils/encryption_setup.sh"
fi

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/all"

#-------------------------------------------
# Create encryption keys
#-------------------------------------------

HMAC_KEY_KID=$(generate_kid)
HMAC_KEY=$(generate_hmac_json $HMAC_KEY_KID 32)

write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "encryption/keys/audit" "keys=$HMAC_KEY current_kid=$HMAC_KEY_KID"

DATA_KEY_KID=$(generate_kid)
DATA_KEY=$(generate_base64_encoded_json $(generate_jwk $DATA_KEY_KID))

JWE_MAPPING=$(echo -n "{\"payload_jwe_kid\":\"$DATA_KEY_KID\"}")
PAYLOAD_JWK=$(generate_jwk "$DATA_KEY_KID")
PRIVATE_JWKS=$(generate_jwks_escaped_json "$PAYLOAD_JWK")
PRIVATE_JWKS_BASE64=$(base64_encode "$PRIVATE_JWKS")

# Must never overwrite!
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "encryption/keys/stored_data" "keys=$DATA_KEY current_kid=$DATA_KEY_KID jwe_mapping=$JWE_MAPPING private_jwks=$PRIVATE_JWKS_BASE64" false

#-------------------------------------------------------------
# Fetch nats key from nats-system namespace and write to vault
#-------------------------------------------------------------

NATS_NS="${NATS_NAMESPACE:-nats-system}"
log_info "Fetching nats keys from $NATS_NS namespace"

NATS_DATA=$(kubectl get -n $NATS_NS secret nats-nkeys -o jsonpath='{.data}')
if [ -z "${NATS_DATA}" ]; then
  log_error "nats-system secret 'nats-nkeys' is empty"
  exit 1
fi

NATS_PUB_KEY=$(echo $NATS_DATA | jq '."user.pub"' -r | base64 -d)
NATS_KEY=$(echo $NATS_DATA | jq '."user.key"' -r | base64 -d)

# nats key to be shared with all services
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "nats/creds" "key=$NATS_KEY public_key=$NATS_PUB_KEY" "true"

#-------------------------------------------------------------
# Mirror auth-callout NKey material from nats-system K8s Secrets to KV.
#
# `nats-auth-callout-nkeys` (rendered by nvcf-nats-k8s) holds the AUTH
# account user + signing NKeys the auth-callout pod's vault-agent sidecar
# loads as `secrets.json`. `shared_worker_nkey_pub` is the APP-account
# shared-worker public key (the `nats-nkeys` user.pub fetched above) and
# `shared_worker_account` is hardcoded to "APP" — mirrors the chart's
# nkey_mappings entry for the shared-worker NKey.
#-------------------------------------------------------------
log_info "Fetching auth-callout NKey material from $NATS_NS namespace"

CALLOUT_DATA=$(kubectl get -n "$NATS_NS" secret nats-auth-callout-nkeys -o jsonpath='{.data}')
if [ -z "${CALLOUT_DATA}" ]; then
  log_error "$NATS_NS secret 'nats-auth-callout-nkeys' is empty"
  exit 1
fi

NKEY_SEED=$(echo "$CALLOUT_DATA" | jq -r '.nkey_seed' | base64 -d)
NKEY_SIGNATURE=$(echo "$CALLOUT_DATA" | jq -r '.nkey_signature' | base64 -d)

if [ -z "$NKEY_SEED" ] || [ -z "$NKEY_SIGNATURE" ]; then
  log_error "auth-callout NKey fields decoded empty (seed=${#NKEY_SEED}, sig=${#NKEY_SIGNATURE})"
  exit 1
fi

write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "nats/auth-callout-creds" \
  "nkey_seed=$NKEY_SEED nkey_signature=$NKEY_SIGNATURE shared_worker_nkey_pub=$NATS_PUB_KEY shared_worker_account=APP" \
  "true"
