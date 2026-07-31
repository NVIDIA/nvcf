# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# compute-plane-webhook-pki

Installs a shared self-signed CA `ClusterIssuer` for compute-plane webhook
`Certificate` resources and optionally provisions the Grove operator webhook
certificate before `grove-operator` starts.

Publish as `nvcf/helm-compute-plane-webhook-pki` alongside
`helm-nvcf-cert-manager` for the compute-plane helmfile stack.
