#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


NAMESPACE="image-credential-helper-test"
CLEANUP=""
if [ "$1" == "--cleanup" ]; then
    CLEANUP="true"
fi

set -euo pipefail

if [ "$CLEANUP" = "true" ]; then
    kubectl delete namespace $NAMESPACE
    exit 0
fi

kubectl create namespace $NAMESPACE || true

kubectl apply -n $NAMESPACE -f examples/init.yaml
kubectl apply -n $NAMESPACE -f examples/helper.yaml
