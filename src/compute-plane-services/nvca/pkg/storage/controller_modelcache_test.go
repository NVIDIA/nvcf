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

package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureCacheMountOptionsConfigMap(t *testing.T) {
	ctx := context.Background()
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).Build()

	// Start-up creates it with the NVMesh defaults.
	require.NoError(t, EnsureCacheMountOptionsConfigMap(ctx, c, ""))

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: DefaultCacheMountOptionsConfigMapName, Namespace: ModelCacheInitNamespace}
	require.NoError(t, c.Get(ctx, key, cm))
	assert.Equal(t, NVMeshCacheMountOptions, cm.Data[NVMeshStorageClassProvisioner])

	// An operator edit must survive a restart, so the second call is a no-op.
	cm.Data[NVMeshStorageClassProvisioner] = "ro,nouuid"
	cm.Data["other.csi.driver"] = "ro"
	require.NoError(t, c.Update(ctx, cm))

	require.NoError(t, EnsureCacheMountOptionsConfigMap(ctx, c, ""))

	got := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, key, got))
	assert.Equal(t, "ro,nouuid", got.Data[NVMeshStorageClassProvisioner], "operator edit was overwritten")
	assert.Equal(t, "ro", got.Data["other.csi.driver"], "added provisioner was dropped")
}

func TestEnsureCacheMountOptionsConfigMap_NameOverride(t *testing.T) {
	ctx := context.Background()
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).Build()

	require.NoError(t, EnsureCacheMountOptionsConfigMap(ctx, c, "custom-cm"))

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: "custom-cm", Namespace: ModelCacheInitNamespace}
	require.NoError(t, c.Get(ctx, key, cm))
	assert.Equal(t, NVMeshCacheMountOptions, cm.Data[NVMeshStorageClassProvisioner])
}
