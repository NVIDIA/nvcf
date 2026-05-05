/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clustermgmt

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

// HelmManagedClient is kept for backward compatibility but now simply wraps
// ConfigMapClient with default behaviour suitable for Helm-managed clusters.
// Any caller can extend it by passing additional clusterMapper functions.

type HelmManagedClient struct {
	*ConfigMapClient
}

// NewHelmManagedClient constructs a HelmManagedClient.
// vaultMountPathTemplate is used as a fallback if the ConfigMap doesn't provide an explicit path.
func NewHelmManagedClient(
	envType nvidiaiov1.EnvType,
	fetcher func(ctx context.Context) (*corev1.ConfigMap, error),
	vaultMountPathTemplate string,
) *HelmManagedClient {
	return &HelmManagedClient{newConfigMapClient(
		envType, fetcher, vaultMountPathTemplate, withClusterSourceMapper(nvcaoptypes.ClusterSourceHelmManaged))}
}
