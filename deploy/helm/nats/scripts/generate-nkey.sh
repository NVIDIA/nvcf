#!/bin/sh
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


set -x # Print commands and their arguments as they are executed.

# Wait for nats-box to be ready
until kubectl get pod -l app.kubernetes.io/component=nats-box -n {{ .Release.Namespace }} -o jsonpath='{.items[0].status.phase}' | grep Running; do
  echo "Waiting for nats-box pod..."
  sleep 2
done

# Get the nats-box pod name
NATS_BOX_POD=$(kubectl get pod -l app.kubernetes.io/component=nats-box -n {{ .Release.Namespace }} -o jsonpath='{.items[0].metadata.name}')
echo "Using nats-box pod: $NATS_BOX_POD"

# Generate NKey using nsc from the nats-box container
echo "Generating NKey using nats-box pod $NATS_BOX_POD..."
OUTPUT=$(kubectl exec -n {{ .Release.Namespace }} $NATS_BOX_POD -- nsc generate nkey --user 2>&1)
echo "$OUTPUT" > /tmp/nkey_output.txt

# Display the output for debugging
cat /tmp/nkey_output.txt

# Extract the keys - the first line should be private (starts with S), second line public (starts with U)
PRIVATE_KEY=$(grep "^S" /tmp/nkey_output.txt)
PUBLIC_KEY=$(grep "^U" /tmp/nkey_output.txt)

echo "Extracted private key: $PRIVATE_KEY"
echo "Extracted public key: $PUBLIC_KEY"

# Create or update the secret
echo "Creating/updating nats-nkeys secret..."
if [ -n "$PRIVATE_KEY" ] && [ -n "$PUBLIC_KEY" ]; then
  kubectl create secret generic nats-nkeys \
    --from-literal=user.key="$PRIVATE_KEY" \
    --from-literal=user.pub="$PUBLIC_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -
  echo "Secret updated successfully"
else
  echo "ERROR: Failed to extract keys from output"
  cat /tmp/nkey_output.txt
  exit 1
fi

echo "NKey generation complete" 
