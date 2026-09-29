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

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// TestNewModelCacheInitNamespace_HasUnboundDNSLabel asserts that the namespace
// carries the workload-instance-type label so the nvcf-unbound Kyverno policy
// (add-unbound-dns) injects the cluster's nvcf-unbound nameserver into pods.
// Without it the model-cache writer job falls back to the kube-dns ClusterIP,
// which is unreachable on clusters running node-local-dns with hostNetwork=true.
func TestNewModelCacheInitNamespace_HasUnboundDNSLabel(t *testing.T) {
	ns := NewModelCacheInitNamespace()
	assert.Equal(t, ModelCacheInitNamespace, ns.Name)
	assert.Equal(t,
		types.WorkloadInstanceTypeValueMiniService,
		ns.Labels[types.WorkloadInstanceTypeLabel],
		"workload-instance-type label must be present so add-unbound-dns injects nvcf-unbound nameservers",
	)
}

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
