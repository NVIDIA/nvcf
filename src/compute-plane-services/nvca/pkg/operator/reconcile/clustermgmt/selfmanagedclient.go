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

// SelfManagedClient wraps ConfigMapClient adding the self-managed mapper
// and bootstrap registration mapper for cluster ID persistence across helm upgrades.

type SelfManagedClient struct {
	*ConfigMapClient
}

// NewSelfManagedClient creates a new SelfManagedClient that reads cluster configuration
// from the main self-managed ConfigMap and merges cluster IDs from the bootstrap
// registration ConfigMap (if present).
// vaultMountPathTemplate is used as a fallback if the ConfigMap doesn't provide an explicit path.
func NewSelfManagedClient(
	envType nvidiaiov1.EnvType,
	fetcher func(ctx context.Context) (*corev1.ConfigMap, error),
	registrationFetcher func(ctx context.Context) (*corev1.ConfigMap, error),
	vaultMountPathTemplate string,
) *SelfManagedClient {
	c := newConfigMapClient(
		envType,
		fetcher,
		vaultMountPathTemplate,
		withClusterSourceMapper(nvcaoptypes.ClusterSourceSelfHosted),
		withBootstrapRegistrationMapper(registrationFetcher),
	)
	return &SelfManagedClient{ConfigMapClient: c}
}
