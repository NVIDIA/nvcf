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
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func newTestContext() context.Context {
	ctx := context.Background()
	// Uncomment the lines below out to enable debug logging.
	// ctx = core.WithDefaultLogger(ctx)
	// log := core.GetLogger(ctx)
	// _ = core.SetLevel(log, "debug")
	return ctx
}

func getDynamicClientTestNodesWithGPUProductOverride() []*v1.Node {
	return []*v1.Node{
		{
			ObjectMeta: newObjectMeta("node-1", "", map[string]string{
				// No instance label
				gpuPresentLabelKey:         "true",
				gpuFamilyLabelKey:          "volta",
				gpuMachineLabelKey:         "Google-Compute-Engine",
				gpuMemoryLabelKey:          "32768",
				gpuProductLabelKey:         "V100-SXM2-32GB",
				gpuProductLabelOverrideKey: "AD102GL",
				gpuDriverMajorLabelKey:     "535",
				gpuDriverMinorLabelKey:     "135",
				gpuDriverRevisionLabelKey:  "05",
				osLabelKey:                 "linux",
				cpuArchLabelKey:            "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-2", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.V100x2",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "volta",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "32768",
				gpuProductLabelKey:          "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
	}
}

func getDynamicClientTestNodes() []*v1.Node {
	return []*v1.Node{
		{
			ObjectMeta: newObjectMeta("node-1", "", map[string]string{
				// No instance label
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "volta",
				gpuMachineLabelKey:        "Google-Compute-Engine",
				gpuMemoryLabelKey:         "32768",
				gpuProductLabelKey:        "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:    "535",
				gpuDriverMinorLabelKey:    "135",
				gpuDriverRevisionLabelKey: "05",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-2", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.V100x2",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "volta",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "32768",
				gpuProductLabelKey:          "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-3-extra-pgpu", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.V100x2",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "volta",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "32768",
				gpuProductLabelKey:          "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				// This should only increase capacity/allocatable of generic nodes by 4, not 5.
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					PGPUResourceKey:             resource.MustParse("5"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					PGPUResourceKey:             resource.MustParse("5"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-not-ready", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.V100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "volta",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "65536",
				gpuProductLabelKey:          "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionFalse,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-no-gpu", "", map[string]string{
				gpuPresentLabelKey:          "false",
				UniformInstanceTypeLabelKey: "ON-PREM.CPU.i913900KF",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8000m"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8000m"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-unlabeled", "", map[string]string{
				"foo": "bar",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("1000m"),
					v1.ResourceMemory:           resource.MustParse("8Gi"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("1000m"),
					v1.ResourceMemory:           resource.MustParse("8Gi"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("node-old-label", "", map[string]string{
				DeprecatedInstanceTypeLabelKey: "standardAD9sr",
				gpuPresentLabelKey:             "true",
				gpuFamilyLabelKey:              "volta",
				gpuMachineLabelKey:             "Google-Compute-Engine",
				gpuMemoryLabelKey:              "32768",
				gpuProductLabelKey:             "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:         "535",
				gpuDriverMinorLabelKey:         "135",
				gpuDriverRevisionLabelKey:      "05",
				osLabelKey:                     "linux",
				cpuArchLabelKey:                "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
	}
}

func TestGetDoNotWarnLabelSet(t *testing.T) {
	assert.Len(t, doNotWarnLabelSet, 1)
	assert.Contains(t, doNotWarnLabelSet, gpuProductLabelOverrideKey)
	assert.True(t, doNotWarnLabelSet[gpuProductLabelOverrideKey])
	assert.False(t, doNotWarnLabelSet[gpuProductLabelKey])
}

func TestDynamicClient_GPUProductOverride(t *testing.T) {
	t.Run("all nodes", func(t *testing.T) {
		t.Parallel()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "AD102GL",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.AD102GL",
						FullName:        "AD102GL",
						Description:     "AD102GL (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       1,
					},
				},
			},
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100x2",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       1,
					},
				},
			},
		})

		ctx := newTestContext()

		nodes := getDynamicClientTestNodesWithGPUProductOverride()
		opts := DynamicClientOptions{
			UniformInstanceLabels:   true,
			MultipleGPUTypesAllowed: true,
		}
		ag := &mockGPUAllocationGetter{gpus: map[types.GPUName]uint64{}}
		dc := newMockDynamicClient(t, ctx, ag, opts, nodes...)

		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPURes, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  4,
			Allocated: 0,
		}, gotGPURes)

		// Add GPU allocation, ensure resources change.
		ag.gpus["AD102GL"] = 1

		gotGPURes, err = dc.GetGPUResources(ctx, "AD102GL")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  2,
			Allocated: 1,
		}, gotGPURes)
	})
}

func TestDynamicClient(t *testing.T) {
	t.Run("all nodes", func(t *testing.T) {
		t.Parallel()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100x2",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
				},
			},
		})

		ctx := newTestContext()

		nodes := getDynamicClientTestNodes()
		opts := DynamicClientOptions{
			UniformInstanceLabels: true,
		}
		ag := &mockGPUAllocationGetter{gpus: map[types.GPUName]uint64{}}
		dc := newMockDynamicClient(t, ctx, ag, opts, nodes...)

		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPURes, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  12,
			Allocated: 0,
		}, gotGPURes)

		// Add GPU allocation, ensure resources change.
		ag.gpus["V100"] = 1

		gotGPURes, err = dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  12,
			Allocated: 1,
		}, gotGPURes)
	})

	t.Run("nodes different non-GPU resources", func(t *testing.T) {
		t.Parallel()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100x2",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       3,
					},
				},
			},
		})

		ctx := newTestContext()

		nodes := append(getDynamicClientTestNodes(), &v1.Node{
			ObjectMeta: newObjectMeta("xxx-node-diff-resources", "", map[string]string{
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "volta",
				gpuMachineLabelKey:        "Google-Compute-Engine",
				gpuMemoryLabelKey:         "32768",
				gpuProductLabelKey:        "V100-SXM2-32GB",
				gpuDriverMajorLabelKey:    "535",
				gpuDriverMinorLabelKey:    "135",
				gpuDriverRevisionLabelKey: "05",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("7"),
					v1.ResourceMemory:           resource.MustParse("37Gi"),
					GPUResourceKey:              resource.MustParse("2"),
					v1.ResourceEphemeralStorage: resource.MustParse("423Gi"),
				},
			},
		})
		opts := DynamicClientOptions{
			UniformInstanceLabels: true,
		}
		ag := &mockGPUAllocationGetter{gpus: map[types.GPUName]uint64{}}
		dc := newMockDynamicClient(t, ctx, ag, opts, nodes...)

		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPURes, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  14,
			Allocated: 0,
		}, gotGPURes)

		// Add GPU allocation, ensure resources change.
		ag.gpus["V100"] = 1

		gotGPURes, err = dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  14,
			Allocated: 1,
		}, gotGPURes)
	})

	t.Run("deprecated label", func(t *testing.T) {
		t.Parallel()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       1,
					},
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
					{
						Name:            "standardAD9sr",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       1,
					},
				},
			},
		})

		ctx := newTestContext()

		opts := DynamicClientOptions{
			UniformInstanceLabels: false,
		}
		ag := &mockGPUAllocationGetter{gpus: map[types.GPUName]uint64{}}
		dc := newMockDynamicClient(t, ctx, ag, opts, getDynamicClientTestNodes()...)

		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPURes, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  12,
			Allocated: 0,
		}, gotGPURes)
	})

	t.Run("bad node", func(t *testing.T) {
		t.Parallel()
		ctx := newTestContext()

		nodes := []*v1.Node{{
			ObjectMeta: newObjectMeta("node", "", map[string]string{
				gpuPresentLabelKey:          "true",
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				// No product label.
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
			},
		}}
		opts := DynamicClientOptions{}
		ag := &mockGPUAllocationGetter{}
		dc := newMockDynamicClient(t, ctx, ag, opts, nodes...)
		_, err := dc.GetAllBackendGPUs(ctx)
		assert.EqualError(t, err, "not exist error: no backend GPUs were found. Ensure gpu-operator is installed and at least one node has GPU resources (nvidia.com/gpu). See https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#verification-running-sample-gpu-applications")
	})

	t.Run("too many GPUs", func(t *testing.T) {
		t.Parallel()
		ctx := newTestContext()

		tnodes := append(getDynamicClientTestNodes(), &v1.Node{
			ObjectMeta: newObjectMeta("node-too-many-gpus", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		})

		opts := DynamicClientOptions{}
		ag := &mockGPUAllocationGetter{}
		dc := newMockDynamicClient(t, ctx, ag, opts, tnodes...)
		_, err := dc.GetAllBackendGPUs(ctx)
		assert.EqualError(t, err, "exactly 1 backend GPU type is allowed, got: 2")
	})

	t.Run("multiple gpu types", func(t *testing.T) {
		t.Parallel()
		ctx := newTestContext()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "A100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.A100",
						FullName:        "A100-SXM4-40GB",
						Description:     "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        1,
						GPUMemoryPerGPU: resource.MustParse("40960Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       1,
					},
				},
			},
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100x2",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
				},
			},
		})

		tnodes := append(getDynamicClientTestNodes(), &v1.Node{
			ObjectMeta: newObjectMeta("node-other-gpu", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		})

		opts := DynamicClientOptions{
			MultipleGPUTypesAllowed: true,
			UniformInstanceLabels:   true,
		}
		ag := &mockGPUAllocationGetter{}
		dc := newMockDynamicClient(t, ctx, ag, opts, tnodes...)
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPUResA100, err := dc.GetGPUResources(ctx, "A100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  1,
			Allocated: 0,
		}, gotGPUResA100)
		gotGPUResV100, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  12,
			Allocated: 0,
		}, gotGPUResV100)
	})

	t.Run("shared cluster", func(t *testing.T) {
		t.Parallel()
		ctx := newTestContext()

		expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
			{
				Name: "V100",
				InstanceTypes: []types.InstanceType{
					{
						Name:            "ON-PREM.GPU.V100x2",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        4,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
					{
						Name:            "ON-PREM.GPU.V100",
						FullName:        "V100-SXM2-32GB",
						Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
						CPU:             resource.MustParse("8"),
						SystemMemory:    resource.MustParse("64Gi"),
						GPUCount:        2,
						GPUMemoryPerGPU: resource.MustParse("32768Mi"),
						OS:              "linux",
						DriverVersion:   "535.135.05",
						CPUArch:         "amd64",
						Storage:         resource.MustParse("512Gi"),
						NodeCount:       2,
					},
				},
			},
		})

		var nodes []*v1.Node
		for _, node := range getDynamicClientTestNodes() {
			node := node
			node.Labels[sharedcluster.ScheduleLabelKey] = "true"
			nodes = append(nodes, node)
		}

		tnodes := append(nodes, &v1.Node{
			ObjectMeta: newObjectMeta("node-not-scheduled", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
				// No "schedule" label
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		})

		opts := DynamicClientOptions{
			UniformInstanceLabels: true,
			SharedClusterOn:       &atomic.Bool{},
		}
		opts.SharedClusterOn.Store(true)
		ag := &mockGPUAllocationGetter{}
		dc := newMockDynamicClient(t, ctx, ag, opts, tnodes...)
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
			assert.NoError(ct, err)
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}, 3*time.Second, 50*time.Millisecond)

		gotGPUResV100, err := dc.GetGPUResources(ctx, "V100")
		assert.NoError(t, err)
		assert.EqualValues(t, types.GPUResource{
			Capacity:  12,
			Allocated: 0,
		}, gotGPUResV100)
	})

	t.Run("unknown instance type", func(t *testing.T) {
		t.Parallel()
		ctx := newTestContext()

		node := &v1.Node{
			ObjectMeta: newObjectMeta("node", "", map[string]string{
				gpuPresentLabelKey: "true",
				gpuProductLabelKey: "A100-SXM4-40GB",
				// Missing many labels.
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
			},
		}

		opts := DynamicClientOptions{}
		ag := &mockGPUAllocationGetter{}
		dc := newMockDynamicClient(t, ctx, ag, opts, node)
		_, err := dc.GetAllBackendGPUs(ctx)
		assert.EqualError(t, err, "not exist error: no backend GPUs were found. Ensure gpu-operator is installed and at least one node has GPU resources (nvidia.com/gpu). See https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#verification-running-sample-gpu-applications")
	})
}

func TestDynamicClient_Kata(t *testing.T) {
	expBackendGPUs := stringifyBackendGPUs([]types.BackendGPU{
		{
			Name: "A100",
			InstanceTypes: []types.InstanceType{
				{
					Name:            "ON-PREM.GPU.A100",
					FullName:        "A100-SXM4-40GB",
					Description:     "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine (Kata-enabled)",
					CPU:             resource.MustParse("8"),
					SystemMemory:    resource.MustParse("64Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("40960Mi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("512Gi"),
					NodeCount:       1,
				},
			},
		},
	})

	tnodes := append(getDynamicClientTestNodes(),
		&v1.Node{
			ObjectMeta: newObjectMeta("node-kata", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuWorkloadConfigLabel:      vmPassthroughVal,
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"AD102GL_L40":               resource.MustParse("0"),
					GPUResourceKey:              resource.MustParse("0"),
					PGPUResourceKey:             resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"AD102GL_L40":               resource.MustParse("0"),
					GPUResourceKey:              resource.MustParse("0"),
					PGPUResourceKey:             resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		&v1.Node{
			ObjectMeta: newObjectMeta("node-kata-bad-gpu", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuWorkloadConfigLabel:      vmPassthroughVal,
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
		&v1.Node{
			ObjectMeta: newObjectMeta("node-kata-bad-gpu-2", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuWorkloadConfigLabel:      vmPassthroughVal,
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"AD102GL_L40":               resource.MustParse("1"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
					PGPUResourceKey:             resource.MustParse("0"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"AD102GL_L40":               resource.MustParse("1"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
					PGPUResourceKey:             resource.MustParse("0"),
				},
			},
		},
	)

	ctx := newTestContext()

	var (
		dc   Client
		opts DynamicClientOptions
	)

	// Kata only.
	opts = DynamicClientOptions{
		UniformInstanceLabels:   true,
		MultipleGPUTypesAllowed: true,
		AttributeFetcher: &mockAttrFetcher{
			attrEnabledFunc: func(a *featureflag.Attribute) bool {
				return a.Key == featureflag.AttrKataRuntimeIsolation.Key
			},
		},
	}
	ag := &mockGPUAllocationGetter{}
	dc = newMockDynamicClient(t, ctx, ag, opts, tnodes...)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
		if assert.NoError(ct, err) {
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}
	}, 3*time.Second, 50*time.Millisecond)

	gotGPURes, err := dc.GetGPUResources(ctx, "A100")
	assert.NoError(t, err)
	if assert.NoError(t, err) {
		assert.EqualValues(t, types.GPUResource{
			Capacity:  1,
			Allocated: 0,
		}, gotGPURes)
	}
	_, err = dc.GetGPUResources(ctx, "V100")
	assert.EqualError(t, err, `gpu V100 not found in capacity set`)

	// GPU passthrough only.
	opts = DynamicClientOptions{
		UniformInstanceLabels:   true,
		MultipleGPUTypesAllowed: true,
		AttributeFetcher: &mockAttrFetcher{
			attrEnabledFunc: func(a *featureflag.Attribute) bool {
				return a.Key == featureflag.AttrPassthroughGPUEnabled.Key
			},
		},
	}
	dc = newMockDynamicClient(t, ctx, ag, opts, tnodes...)

	expBackendGPUs[0].InstanceTypes[0].Description =
		"A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine (Passthrough GPU)"
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
		if assert.NoError(ct, err) {
			assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
		}
	}, 3*time.Second, 50*time.Millisecond)

	gotGPURes, err = dc.GetGPUResources(ctx, "A100")
	if assert.NoError(t, err) {
		assert.EqualValues(t, types.GPUResource{
			Capacity:  1,
			Allocated: 0,
		}, gotGPURes)
	}
	_, err = dc.GetGPUResources(ctx, "V100")
	assert.EqualError(t, err, `gpu V100 not found in capacity set`)

	// Kata and GPU passthrough attributes may be disabled
	// but a node may be configured for vm-passthrough.
	opts = DynamicClientOptions{
		UniformInstanceLabels:   true,
		MultipleGPUTypesAllowed: true,
		AttributeFetcher: &mockAttrFetcher{
			attrEnabledFunc: func(a *featureflag.Attribute) bool {
				return a.Key != featureflag.AttrKataRuntimeIsolation.Key &&
					a.Key != featureflag.AttrPassthroughGPUEnabled.Key
			},
		},
	}
	dc = newMockDynamicClient(t, ctx, ag, opts, tnodes...)

	tnodes = append(getDynamicClientTestNodes(),
		&v1.Node{
			ObjectMeta: newObjectMeta("node-kata", "", map[string]string{
				UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				gpuPresentLabelKey:          "true",
				gpuFamilyLabelKey:           "ampere",
				gpuMachineLabelKey:          "Google-Compute-Engine",
				gpuMemoryLabelKey:           "40960",
				gpuProductLabelKey:          "A100-SXM4-40GB",
				gpuWorkloadConfigLabel:      vmPassthroughVal,
				gpuDriverMajorLabelKey:      "535",
				gpuDriverMinorLabelKey:      "135",
				gpuDriverRevisionLabelKey:   "05",
				osLabelKey:                  "linux",
				cpuArchLabelKey:             "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{
						Type:   v1.NodeReady,
						Status: v1.ConditionTrue,
					},
				},
				Capacity: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"GA102_A100":                resource.MustParse("0"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("8"),
					v1.ResourceMemory:           resource.MustParse("64Gi"),
					"GA102_A100":                resource.MustParse("0"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
				},
			},
		},
	)

	expBackendGPUs = stringifyBackendGPUs([]types.BackendGPU{
		{
			Name: "A100",
			InstanceTypes: []types.InstanceType{
				{
					Name:            "ON-PREM.GPU.A100",
					FullName:        "A100-SXM4-40GB",
					Description:     "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
					CPU:             resource.MustParse("8"),
					SystemMemory:    resource.MustParse("64Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("40960Mi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("512Gi"),
					NodeCount:       1,
				},
			},
		},
		{
			Name: "V100",
			InstanceTypes: []types.InstanceType{
				{
					Name:            "ON-PREM.GPU.V100x2",
					FullName:        "V100-SXM2-32GB",
					Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
					CPU:             resource.MustParse("8"),
					SystemMemory:    resource.MustParse("64Gi"),
					GPUCount:        4,
					GPUMemoryPerGPU: resource.MustParse("32768Mi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("512Gi"),
					NodeCount:       2,
				},
				{
					Name:            "ON-PREM.GPU.V100",
					FullName:        "V100-SXM2-32GB",
					Description:     "V100-SXM2-32GB (volta family) on a Google-Compute-Engine machine",
					CPU:             resource.MustParse("8"),
					SystemMemory:    resource.MustParse("64Gi"),
					GPUCount:        2,
					GPUMemoryPerGPU: resource.MustParse("32768Mi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("512Gi"),
					NodeCount:       2,
				},
			},
		},
	})

	dc = newMockDynamicClient(t, ctx, ag, opts, tnodes...)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
		assert.NoError(ct, err)
		assert.Equal(ct, expBackendGPUs, stringifyBackendGPUs(gotBackendGPUs))
	}, 3*time.Second, 50*time.Millisecond)

	gotGPURes, err = dc.GetGPUResources(ctx, "A100")
	assert.NoError(t, err)
	assert.EqualValues(t, types.GPUResource{
		Capacity:  1,
		Allocated: 0,
	}, gotGPURes)
	gotGPURes, err = dc.GetGPUResources(ctx, "V100")
	assert.NoError(t, err)
	assert.EqualValues(t, types.GPUResource{
		Capacity:  12,
		Allocated: 0,
	}, gotGPURes)
}

func newObjectMeta(name, namespace string, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    labels,
	}
}

func newMockDynamicClient(t *testing.T,
	ctx context.Context,
	ag GPUAllocationGetter,
	opts DynamicClientOptions,
	nodes ...*v1.Node,
) Client {
	t.Helper()
	dc, _ := newMockDynamicClientPods(t, ctx, ag, opts, nodes...)
	return dc
}

func newMockDynamicClientPods(t *testing.T,
	ctx context.Context,
	ag GPUAllocationGetter,
	opts DynamicClientOptions,
	nodes ...*v1.Node,
) (Client, kubernetes.Interface) {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	t.Cleanup(cancel)

	k8sclient := fakek8sclient.NewSimpleClientset()
	f := informers.NewSharedInformerFactoryWithOptions(
		k8sclient,
		3*time.Second,
		NewNodeInformerOptions(opts.AttributeFetcher)...,
	)

	ni := f.Core().V1().Nodes()

	dc := NewDynamicClient(ag, sortingNodeLister{ni.Lister()}, "ON-PREM", opts)

	for _, node := range nodes {
		_, err := k8sclient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	f.Start(ctx.Done())

	cache.WaitForCacheSync(ctx.Done(), ni.Informer().HasSynced)

	err := wait.PollUntilContextCancel(ctx, 10*time.Millisecond, true, func(context.Context) (bool, error) {
		return len(ni.Informer().GetStore().List()) != 0, nil
	})
	require.NoError(t, err)

	return dc, k8sclient
}

type sortingNodeLister struct {
	listersv1.NodeLister
}

func (ni sortingNodeLister) List(selector labels.Selector) (ret []*v1.Node, err error) {
	if ret, err = ni.NodeLister.List(selector); err != nil {
		return nil, err
	}
	sort.Slice(ret, func(i, j int) bool { return ret[i].Name < ret[j].Name })
	return ret, nil
}

type mockGPUAllocationGetter struct {
	gpus map[types.GPUName]uint64
	err  error
}

func (ag *mockGPUAllocationGetter) GetForGPU(_ context.Context, gpuName types.GPUName) (uint64, error) {
	if ag.err != nil {
		return 0, ag.err
	}
	if ag.gpus == nil {
		return 0, nil
	}
	return ag.gpus[gpuName], nil
}

// The String() method on resources mutates the underlying type
// so equality fails unless tests do the same.
func stringifyBackendGPUs(backendGPUs []types.BackendGPU) []types.BackendGPU {
	for _, gpu := range backendGPUs {
		for i := range gpu.InstanceTypes {
			_ = (&gpu.InstanceTypes[i].SystemMemory).String()
			_ = (&gpu.InstanceTypes[i].GPUMemoryPerGPU).String()
			_ = (&gpu.InstanceTypes[i].CPU).String()
			_ = (&gpu.InstanceTypes[i].Storage).String()
		}
	}
	return backendGPUs
}

func getMIGTestNodes() []*v1.Node {
	return []*v1.Node{
		{
			ObjectMeta: newObjectMeta("mig-node-1", "", map[string]string{
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "blackwell",
				gpuMachineLabelKey:        "NVIDIA-DGX",
				gpuMemoryLabelKey:         "49152",
				gpuProductLabelKey:        "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition-MIG-2g.48gb",
				gpuDriverMajorLabelKey:    "560",
				gpuDriverMinorLabelKey:    "35",
				gpuDriverRevisionLabelKey: "03",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{Type: v1.NodeReady, Status: v1.ConditionTrue},
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("64"),
					v1.ResourceMemory:           resource.MustParse("512Gi"),
					GPUResourceKey:              resource.MustParse("4"),
					v1.ResourceEphemeralStorage: resource.MustParse("1Ti"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("mig-node-2", "", map[string]string{
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "blackwell",
				gpuMachineLabelKey:        "NVIDIA-DGX",
				gpuMemoryLabelKey:         "24576",
				gpuProductLabelKey:        "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition-MIG-1g.24gb",
				gpuDriverMajorLabelKey:    "560",
				gpuDriverMinorLabelKey:    "35",
				gpuDriverRevisionLabelKey: "03",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{Type: v1.NodeReady, Status: v1.ConditionTrue},
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("64"),
					v1.ResourceMemory:           resource.MustParse("512Gi"),
					GPUResourceKey:              resource.MustParse("8"),
					v1.ResourceEphemeralStorage: resource.MustParse("1Ti"),
				},
			},
		},
		{
			ObjectMeta: newObjectMeta("mig-node-3-h200", "", map[string]string{
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "hopper",
				gpuMachineLabelKey:        "NVIDIA-DGX",
				gpuMemoryLabelKey:         "141312",
				gpuProductLabelKey:        "NVIDIA-H200-MIG-7g.141gb",
				gpuDriverMajorLabelKey:    "560",
				gpuDriverMinorLabelKey:    "35",
				gpuDriverRevisionLabelKey: "03",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{Type: v1.NodeReady, Status: v1.ConditionTrue},
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("128"),
					v1.ResourceMemory:           resource.MustParse("1Ti"),
					GPUResourceKey:              resource.MustParse("1"),
					v1.ResourceEphemeralStorage: resource.MustParse("2Ti"),
				},
			},
		},
		{
			// Non-MIG node: same base GPU, no MIG suffix.
			ObjectMeta: newObjectMeta("non-mig-node", "", map[string]string{
				gpuPresentLabelKey:        "true",
				gpuFamilyLabelKey:         "blackwell",
				gpuMachineLabelKey:        "NVIDIA-DGX",
				gpuMemoryLabelKey:         "49152",
				gpuProductLabelKey:        "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition",
				gpuDriverMajorLabelKey:    "560",
				gpuDriverMinorLabelKey:    "35",
				gpuDriverRevisionLabelKey: "03",
				osLabelKey:                "linux",
				cpuArchLabelKey:           "amd64",
			}),
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{
					{Type: v1.NodeReady, Status: v1.ConditionTrue},
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:              resource.MustParse("64"),
					v1.ResourceMemory:           resource.MustParse("512Gi"),
					GPUResourceKey:              resource.MustParse("8"),
					v1.ResourceEphemeralStorage: resource.MustParse("1Ti"),
				},
			},
		},
	}
}

func TestDynamicClient_MIGInstanceTypeNaming(t *testing.T) {
	t.Parallel()

	ctx := newTestContext()
	nodes := getMIGTestNodes()
	opts := DynamicClientOptions{
		UniformInstanceLabels:   true,
		MultipleGPUTypesAllowed: true,
	}
	ag := &mockGPUAllocationGetter{gpus: map[types.GPUName]uint64{}}
	dc := newMockDynamicClient(t, ctx, ag, opts, nodes...)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotBackendGPUs, err := dc.GetAllBackendGPUs(ctx)
		assert.NoError(ct, err)

		gpuNames := map[types.GPUName]bool{}
		instanceNames := map[types.InstanceName]bool{}
		for _, gpu := range gotBackendGPUs {
			gpuNames[gpu.Name] = true
			for _, it := range gpu.InstanceTypes {
				instanceNames[it.Name] = true
			}
		}

		// Each MIG profile and non-MIG GPU must have a unique GPU name.
		assert.True(ct, gpuNames[types.GPUName("RTXPRO6000-MIG-2g-48gb")],
			"expected MIG-2g-48gb GPU name")
		assert.True(ct, gpuNames[types.GPUName("RTXPRO6000-MIG-1g-24gb")],
			"expected MIG-1g-24gb GPU name")
		assert.True(ct, gpuNames[types.GPUName("H200-MIG-7g-141gb")],
			"expected H200 MIG-7g-141gb GPU name")
		assert.True(ct, gpuNames[types.GPUName("RTXPRO6000")],
			"expected non-MIG RTXPRO6000 GPU name")

		// Instance names must match the expected format.
		assert.True(ct, instanceNames[types.InstanceName("ON-PREM.GPU.RTXPRO6000-MIG-2g-48gb")],
			"expected MIG-2g-48gb instance name")
		assert.True(ct, instanceNames[types.InstanceName("ON-PREM.GPU.RTXPRO6000-MIG-1g-24gb")],
			"expected MIG-1g-24gb instance name")
		assert.True(ct, instanceNames[types.InstanceName("ON-PREM.GPU.H200-MIG-7g-141gb")],
			"expected H200 MIG instance name")
		assert.True(ct, instanceNames[types.InstanceName("ON-PREM.GPU.RTXPRO6000")],
			"expected non-MIG instance name")

		// All 4 must be distinct GPU entries.
		assert.Equal(ct, 4, len(gpuNames), "expected 4 distinct GPU names")
	}, 3*time.Second, 50*time.Millisecond)
}

type mockAttrFetcher struct {
	attrEnabledFunc func(*featureflag.Attribute) bool
}

func (f *mockAttrFetcher) IsAttributeEnabled(ff *featureflag.Attribute) bool {
	return f.attrEnabledFunc(ff)
}
