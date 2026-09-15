#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

set -euo pipefail

# Generate TLS certificates for the webhook
# This creates a self-signed CA and signs the webhook certificate

WEBHOOK_NAMESPACE="${WEBHOOK_NAMESPACE:-nvcf-webhook}"
WEBHOOK_SERVICE="${WEBHOOK_SERVICE:-nvcf-pod-mutator}"
SECRET_NAME="${SECRET_NAME:-nvcf-pod-mutator-certs}"
CERT_DIR="${CERT_DIR:-./certs}"

echo "🔐 Generating TLS certificates for NVCF Pod Mutator Webhook"
echo "   Namespace: $WEBHOOK_NAMESPACE"
echo "   Service:   $WEBHOOK_SERVICE"
echo "   Secret:    $SECRET_NAME"
echo ""

# Create cert directory
mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

# Generate CA private key
echo "📝 Generating CA private key..."
openssl genrsa -out ca.key 2048

# Generate CA certificate
echo "📝 Generating CA certificate..."
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 \
  -out ca.crt \
  -subj "/CN=NVCF Webhook CA"

# Generate webhook private key
echo "📝 Generating webhook private key..."
openssl genrsa -out tls.key 2048

# Create certificate signing request
echo "📝 Creating certificate signing request..."
cat > csr.conf <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[v3_req]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${WEBHOOK_SERVICE}
DNS.2 = ${WEBHOOK_SERVICE}.${WEBHOOK_NAMESPACE}
DNS.3 = ${WEBHOOK_SERVICE}.${WEBHOOK_NAMESPACE}.svc
DNS.4 = ${WEBHOOK_SERVICE}.${WEBHOOK_NAMESPACE}.svc.cluster.local
EOF

openssl req -new -key tls.key -out tls.csr \
  -subj "/CN=${WEBHOOK_SERVICE}.${WEBHOOK_NAMESPACE}.svc" \
  -config csr.conf

# Sign the certificate
echo "📝 Signing webhook certificate..."
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out tls.crt -days 3650 -sha256 \
  -extensions v3_req -extfile csr.conf

# Verify the certificate
echo "✅ Verifying certificate..."
openssl verify -CAfile ca.crt tls.crt

# Display certificate info
echo ""
echo "📋 Certificate Information:"
openssl x509 -in tls.crt -noout -text | grep -A 3 "Subject Alternative Name"

# Create Kubernetes secret
echo ""
echo "🚀 Creating Kubernetes secret..."

# Delete existing secret if it exists
kubectl delete secret "$SECRET_NAME" -n "$WEBHOOK_NAMESPACE" 2>/dev/null || true

# Create new secret
kubectl create secret tls "$SECRET_NAME" \
  --cert=tls.crt \
  --key=tls.key \
  -n "$WEBHOOK_NAMESPACE"

echo "✅ Secret created: $SECRET_NAME"

# Export CA bundle for webhook configuration
echo ""
echo "📦 CA Bundle (for webhook configuration):"
CA_BUNDLE=$(cat ca.crt | base64 | tr -d '\n')
echo "$CA_BUNDLE"

# Update webhook configuration
echo ""
echo "🔧 Updating MutatingWebhookConfiguration..."
cd ..
sed "s/\${CA_BUNDLE}/$CA_BUNDLE/g" deploy/webhook-configuration.yaml | kubectl apply -f -

echo ""
echo "✅ Certificate generation complete!"
echo ""
echo "📁 Certificate files saved to: $CERT_DIR"
echo "   - ca.crt:  CA certificate"
echo "   - ca.key:  CA private key (keep secure!)"
echo "   - tls.crt: Webhook certificate"
echo "   - tls.key: Webhook private key (keep secure!)"
echo ""
echo "🎯 Next steps:"
echo "   1. Build and push the webhook image"
echo "   2. Update the image in deploy/webhook-deployment.yaml"
echo "   3. Deploy: kubectl apply -f deploy/webhook-deployment.yaml"


