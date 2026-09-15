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

package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_setupCRDs(t *testing.T) {
	b := &BackendK8sCache{
		clients: mockKubeClients(),
	}
	ctx := context.Background()

	err := b.setupCRDs(ctx)
	require.NoError(t, err)

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "storagerequests.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "miniservices.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Try again, should get no error.
	err = b.setupCRDs(ctx)
	require.NoError(t, err)

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "storagerequests.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "miniservices.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
}

func Test_setupCRDs_migrateMiniService(t *testing.T) {
	b := &BackendK8sCache{
		clients: mockKubeClients(),
	}
	ctx := context.Background()

	miniserviceCRD, err := decodeCRD(miniserviceCRDData)
	require.NoError(t, err)
	miniserviceCRD.Spec.Scope = apiextv1.NamespaceScoped

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Create(ctx, miniserviceCRD, metav1.CreateOptions{})
	require.NoError(t, err)

	err = b.setupCRDs(ctx)
	require.NoError(t, err)

	gotMiniserviceCRD, err := b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, miniserviceCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, apiextv1.ClusterScoped, gotMiniserviceCRD.Spec.Scope)
}
