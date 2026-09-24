#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
