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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

type k8sClient interface {
	CreateOrUpdateNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error
}

type Client interface {
	GetCluster(ctx context.Context, clusterID string) (*Cluster, error)
}

func ReconcileNVCACluster(ctx context.Context, localClient k8sClient, clusterClient Client, clusterID string) error {
	log := core.GetLogger(ctx)

	srcCluster, err := clusterClient.GetCluster(ctx, clusterID)
	if err != nil {
		log.WithError(err).Errorf("failed to retrieve the cluster clusterID=%s", clusterID)
		return err
	}

	err = localClient.CreateOrUpdateNVCFBackend(ctx, srcCluster.NVCFBackend)
	return err
}
