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

package nodefeatures

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Client discovers GPU facts about a cluster at one point in time.
type Client interface {
	// GetAllBackendGPUs returns the cluster's GPU and corresponding instance types.
	GetAllBackendGPUs(context.Context) ([]types.BackendGPU, error)
	// GetGPUResources calculates the GPU capacity in the cluster
	// for a specific GPU type across all instances.
	GetGPUResources(context.Context, types.GPUName) (types.GPUResource, error)
}

type GPUAllocationGetter interface {
	GetForGPU(context.Context, types.GPUName) (uint64, error)
}

type GetGPUAllocationFunc func(context.Context, types.GPUName) (uint64, error)

func (f GetGPUAllocationFunc) GetForGPU(ctx context.Context, gpuName types.GPUName) (uint64, error) {
	return f(ctx, gpuName)
}

// MakeGPUResource returns the key/value for a GPU resource to be added to a corev1.ResourceList.
// attrs determines what the resource name should be.
func MakeGPUResource(gpuCount uint64, attrs featureflag.Attributes) (corev1.ResourceName, resource.Quantity) {
	q := *resource.NewQuantity(int64(gpuCount), resource.DecimalSI) //nolint:gosec
	return GetGPUResourceName(attrs), q
}

func GetGPUResourceName(attrs featureflag.Attributes) corev1.ResourceName {
	switch {
	case attrs.Empty():
	case attrs.Enabled(featureflag.AttrKataRuntimeIsolation),
		attrs.Enabled(featureflag.AttrPassthroughGPUEnabled):
		return corev1.ResourceName(PGPUResourceKey)
	}
	return corev1.ResourceName(GPUResourceKey)
}

func MakeGPUResourceFetcher(gpuCount uint64, ff featureflag.Fetcher) (corev1.ResourceName, resource.Quantity) {
	q := *resource.NewQuantity(int64(gpuCount), resource.DecimalSI) //nolint:gosec
	return GetGPUResourceNameFetcher(ff), q
}

func GetGPUResourceNameFetcher(ff featureflag.AttributeFetcher) corev1.ResourceName {
	switch {
	case ff.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation),
		ff.IsAttributeEnabled(featureflag.AttrPassthroughGPUEnabled):
		return corev1.ResourceName(PGPUResourceKey)
	}
	return corev1.ResourceName(GPUResourceKey)
}
