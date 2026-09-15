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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const gpuData = `
[
    {
        "name": "L40",
        "capacity": 10,
        "instanceTypes": [
            {
                "name": "ON-PREM.GPU.L40_1x",
                "value": "ON-PREM.GPU.L40",
                "description": "One Nvidia ada GPU",
                "default": true,
                "cpuCores": 4,
                "systemMemory": "28G",
                "gpuMemory": "48G",
                "gpuCount": 1,
				"os": "linux",
				"driverVersion": "535.135.05",
				"cpuArch": "amd64",
				"storage": "180G"
            },
            {
                "name": "ON-PREM.GPU.L40_2x",
                "value": "ON-PREM.GPU.L40",
                "description": "Two Nvidia ada GPUs",
                "default": false,
                "cpuCores": 4,
                "systemMemory": "28G",
                "gpuMemory": "96G",
                "gpuCount": 2,
				"os": "linux",
				"driverVersion": "535.135.05",
				"cpuArch": "amd64",
				"storage": "360G"
            }
        ]
    }
]
`

var backendGPUs = []types.BackendGPU{
	{
		Name:     "L40",
		Capacity: 10,
		Static:   true,
		InstanceTypes: []types.InstanceType{
			{
				Name:            "ON-PREM.GPU.L40_1x",
				FullName:        "ON-PREM.GPU.L40",
				Description:     "One Nvidia ada GPU",
				Default:         true,
				CPU:             resource.MustParse("4"),
				SystemMemory:    resource.MustParse("28G"),
				GPUCount:        1,
				GPUMemoryPerGPU: resource.MustParse("48G"),
				OS:              "linux",
				DriverVersion:   "535.135.05",
				CPUArch:         "amd64",
				Storage:         resource.MustParse("180G"),
			},
			{
				Name:            "ON-PREM.GPU.L40_2x",
				FullName:        "ON-PREM.GPU.L40",
				Description:     "Two Nvidia ada GPUs",
				Default:         false,
				CPU:             resource.MustParse("4"),
				SystemMemory:    resource.MustParse("28G"),
				GPUCount:        2,
				GPUMemoryPerGPU: resource.MustParse("96G"),
				OS:              "linux",
				DriverVersion:   "535.135.05",
				CPUArch:         "amd64",
				Storage:         resource.MustParse("360G"),
			},
		},
	},
}

var backendGPUsOld = []types.BackendGPU{
	{
		Name:   "L40",
		Static: true,
		InstanceTypes: []types.InstanceType{
			{
				Name:            "ON-PREM.GPU.L40_1x",
				FullName:        "ON-PREM.GPU.L40",
				Description:     "One Nvidia ada GPU",
				Default:         true,
				CPU:             resource.MustParse("4"),
				SystemMemory:    resource.MustParse("28G"),
				GPUCount:        1,
				GPUMemoryPerGPU: resource.MustParse("48G"),
			},
			{
				Name:            "ON-PREM.GPU.L40_2x",
				FullName:        "ON-PREM.GPU.L40",
				Description:     "Two Nvidia ada GPUs",
				Default:         false,
				CPU:             resource.MustParse("4"),
				SystemMemory:    resource.MustParse("28G"),
				GPUCount:        2,
				GPUMemoryPerGPU: resource.MustParse("96G"),
			},
		},
	},
}

const gpuDataOld = `
[
    {
        "name": "L40",
        "instanceTypes": [
            {
                "name": "ON-PREM.GPU.L40_1x",
                "value": "ON-PREM.GPU.L40",
                "description": "One Nvidia ada GPU",
                "default": true,
                "cpuCores": 4,
                "systemMemory": "28G",
                "gpuMemory": "48G",
                "gpuCount": 1
            },
            {
                "name": "ON-PREM.GPU.L40_2x",
                "value": "ON-PREM.GPU.L40",
                "description": "Two Nvidia ada GPUs",
                "default": false,
                "cpuCores": 4,
                "systemMemory": "28G",
                "gpuMemory": "96G",
                "gpuCount": 2
            }
        ]
    }
]
`

func TestStaticClientBackwardCompatible(t *testing.T) {
	t.Parallel()

	expBackendGPUs := stringifyBackendGPUs(backendGPUsOld)

	ctx := context.Background()

	sc := newMockStaticClient(t, gpuDataOld)

	gotBackendGPUs, err := sc.GetAllBackendGPUs(ctx)
	require.NoError(t, err)
	gotBackendGPUs = stringifyBackendGPUs(gotBackendGPUs)
	assert.Equal(t, expBackendGPUs, gotBackendGPUs)

	gotGPURes, err := sc.GetGPUResources(ctx, "L40")
	require.NoError(t, err)
	assert.EqualValues(t, types.GPUResource{
		Capacity:  5,
		Allocated: 1,
	}, gotGPURes)
}

func TestStaticClient(t *testing.T) {
	t.Parallel()

	expBackendGPUs := stringifyBackendGPUs(backendGPUs)

	ctx := context.Background()

	sc := newMockStaticClient(t, gpuData)

	gotBackendGPUs, err := sc.GetAllBackendGPUs(ctx)
	assert.NoError(t, err)
	gotBackendGPUs = stringifyBackendGPUs(gotBackendGPUs)
	assert.Equal(t, expBackendGPUs, gotBackendGPUs)

	gotGPURes, err := sc.GetGPUResources(ctx, "L40")
	assert.NoError(t, err)
	assert.EqualValues(t, types.GPUResource{
		Capacity:  10,
		Allocated: 1,
	}, gotGPURes)

}

func newMockStaticClient(t *testing.T, gpuData string) Client {
	t.Helper()

	cm := &v1.ConfigMap{
		ObjectMeta: newObjectMeta(ConfigMapName, "nvca-system", nil),
		Data: map[string]string{
			ConfigMapGPUsKey: gpuData,
		},
	}
	k8sclient := fakek8sclient.NewSimpleClientset(cm)
	clients := &kubeclients.KubeClients{K8s: k8sclient}
	rg := func(_ context.Context, gpuName types.GPUName) (uint64, error) {
		return 1, nil
	}

	sc := NewStaticClient(clients, GetGPUAllocationFunc(rg), "nvca-system", 5)

	return sc
}
