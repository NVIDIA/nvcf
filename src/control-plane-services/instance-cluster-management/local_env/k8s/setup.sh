#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -euo pipefail

minikube start --nodes 2 -p multinode --driver=docker --ports=8443:8443
kubectl apply -f namespace.yaml
kubectl apply -f serviceaccount.yaml
kubectl apply -f secret.yaml

echo 'Update "oci_k8s_credentials.api_token" with base64 decoded value of "token"'
echo 'and "oci_k8s_credentials.ssl_cert" with "ca-cert"'
echo 'in local_env/vault/secrets.json file from below data'

kubectl label nodes multinode node.kubernetes.io/instance-type=BM.GPU.A100-v2.8
kubectl label nodes multinode topology.kubernetes.io/zone=local
kubectl label nodes multinode-m02 node.kubernetes.io/instance-type=BM.GPU.A100-v2.8
kubectl label nodes multinode-m02 topology.kubernetes.io/zone=local

kubectl -n picasso-local get secret picasso-secret -o json
