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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	k8serr "k8s.io/apimachinery/pkg/api/errors"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

type mockReconcilerClient struct {
	createOrUpdateNVCFBackendFunc func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error
	deleteNVCFBackendFunc         func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error
	getClusterFunc                func(ctx context.Context, clusterID string) (*Cluster, error)
	isEnabled                     func() bool
}

func (m *mockReconcilerClient) CreateOrUpdateNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	return m.createOrUpdateNVCFBackendFunc(ctx, nb)
}

func (m *mockReconcilerClient) GetCluster(ctx context.Context, clusterID string) (*Cluster, error) {
	return m.getClusterFunc(ctx, clusterID)
}

func (m *mockReconcilerClient) DeleteNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	return m.deleteNVCFBackendFunc(ctx, nb)
}

func (m *mockReconcilerClient) IsEnabled() bool {
	return m.isEnabled()
}

func TestReconcileNVCACluster(t *testing.T) {
	client := &mockReconcilerClient{
		createOrUpdateNVCFBackendFunc: func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
			return nil
		},
		getClusterFunc: func(ctx context.Context, clusterID string) (*Cluster, error) {
			return &Cluster{
				NVCFBackend: &nvidiaiov1.NVCFBackend{},
			}, nil
		},
		deleteNVCFBackendFunc: func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
			return nil
		},
		isEnabled: func() bool { return true },
	}
	ctx := context.Background()

	err := ReconcileNVCACluster(ctx, client, client, "some-cluster-id")
	assert.NoError(t, err)

	client.getClusterFunc = func(ctx context.Context, clusterID string) (*Cluster, error) {
		return nil, errors.New("some error")
	}
	err = ReconcileNVCACluster(ctx, client, client, "some-cluster-id")
	assert.Error(t, err)
}

func TestReconcileNVCACluster404(t *testing.T) {
	client := &mockReconcilerClient{
		createOrUpdateNVCFBackendFunc: func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
			return nil
		},
		getClusterFunc: func(ctx context.Context, clusterID string) (*Cluster, error) {
			return nil, k8serr.NewNotFound(nvidiaiov1.Resource("some-cluster-id"), "some-cluster-id")
		},
		deleteNVCFBackendFunc: func(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
			return nil
		},
		isEnabled: func() bool { return true },
	}
	ctx := context.Background()

	err := ReconcileNVCACluster(ctx, client, client, "some-cluster-id")
	assert.Error(t, err)
}
