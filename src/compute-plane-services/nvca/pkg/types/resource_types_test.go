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

package types

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGPUResourceMarshalJSONValue(t *testing.T) {
	type spec struct {
		name     string
		gpuRes   GPUResource
		expected map[string]uint64
	}

	cases := []spec{
		{
			name:   "no GPUs",
			gpuRes: GPUResource{},
			expected: map[string]uint64{
				"available": 0,
				"capacity":  0,
				"allocated": 0,
			},
		},
		{
			name: "1 GPU",
			gpuRes: GPUResource{
				Capacity: 1,
			},
			expected: map[string]uint64{
				"available": 1,
				"capacity":  1,
				"allocated": 0,
			},
		},
		{
			name: "1 GPU - 1 allocated",
			gpuRes: GPUResource{
				Capacity:  1,
				Allocated: 1,
			},
			expected: map[string]uint64{
				"available": 0,
				"capacity":  1,
				"allocated": 1,
			},
		},
		{
			name: "1 GPU - 2 allocated",
			gpuRes: GPUResource{
				Capacity:  1,
				Allocated: 2,
			},
			expected: map[string]uint64{
				"available": 0,
				"capacity":  1,
				"allocated": 2,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Test the value case
			b, err := json.Marshal(c.gpuRes)
			require.NoError(t, err)
			actual := map[string]uint64{}
			require.NoError(t, json.Unmarshal(b, &actual))
			assert.Equal(t, c.expected, actual)

			// Test the pointer case
			b, err = json.Marshal(&c.gpuRes)
			require.NoError(t, err)
			actual = map[string]uint64{}
			require.NoError(t, json.Unmarshal(b, &actual))
			assert.Equal(t, c.expected, actual)
		})
	}
}

func TestHelperAPIs(t *testing.T) {
	rt1 := RegistrationInstanceType{
		Name:          "ON-PREM.GPU.A100",
		CPUCores:      4,
		CPU:           "4",
		SystemMemory:  "2048",
		GPUCount:      6,
		GPUMemory:     "2048",
		DriverVersion: "525.2.02",
	}

	rt2 := RegistrationInstanceType{
		Name:          "ON-PREM.GPU.A100",
		CPUCores:      4,
		CPU:           "4",
		SystemMemory:  "2048",
		GPUCount:      6,
		GPUMemory:     "2048",
		DriverVersion: "525.2.03",
	}
	assert.Equal(t, rt1.String(), rt2.String())

	ch1 := ComponentHealth{
		Status: HealthStatusUnhealthy,
	}
	assert.False(t, ch1.IsHealthy())

	ch2 := ComponentHealth{
		Status: HealthStatusHealthy,
		Errors: []string{
			"error1",
			"error2",
		},
	}
	assert.False(t, ch2.IsHealthy())

	ch3 := ComponentHealth{
		Status: HealthStatusHealthy,
	}
	assert.True(t, ch3.IsHealthy())

	assert.Equal(t, MakeInstanceName("GCP", "A100"), InstanceName("GCP.GPU.A100"))

	g, err := GetGPUNameFromInstanceType("GCP.GPU.A100")
	assert.Nil(t, err)
	assert.Equal(t, g, GPUName("A100"))

	_, err = GetGPUNameFromInstanceType("GCP.A100")
	assert.NotNil(t, err)

	c, err := NormalizeClusterProvider("gcp")
	assert.Nil(t, err)
	assert.Equal(t, c, "GCP")

	_, err = NormalizeClusterProvider("random")
	assert.NotNil(t, err)
}

func TestGPUResourceAvailable(t *testing.T) {
	type spec struct {
		name     string
		gpuRes   GPUResource
		expected uint64
	}

	cases := []spec{
		{
			name:     "no GPUs",
			gpuRes:   GPUResource{},
			expected: 0,
		},
		{
			name: "1 Capacity - 1 Available",
			gpuRes: GPUResource{
				Capacity: 1,
			},
			expected: 1,
		},
		{
			name: "2 GPU(s) - 1 allocated",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 1,
			},
			expected: 1,
		},
		{
			name: "2 GPU(s) - 2 allocated",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 2,
			},
			expected: 0,
		},
		{
			name: "2 GPU(s) - 3 allocated",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 3,
			},
			expected: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, c.gpuRes.Available())
		})
	}
}

func TestHasCapacityForRequest(t *testing.T) {
	type spec struct {
		name     string
		gpuRes   GPUResource
		request  uint64
		expected bool
	}

	cases := []spec{
		{
			name: "no GPUs - request 1",
			gpuRes: GPUResource{
				Capacity: 0,
			},
			request:  1,
			expected: false,
		},
		{
			name: "no GPUs - request 1",
			gpuRes: GPUResource{
				Capacity: 0,
			},
			request:  1,
			expected: false,
		},
		{
			name: "2 GPU(s) - request 1",
			gpuRes: GPUResource{
				Capacity: 2,
			},
			request:  1,
			expected: true,
		},
		{
			name: "2 GPU(s) - request 1",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 1,
			},
			request:  1,
			expected: true,
		},
		{
			name: "2 GPU(s) - request 2",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 1,
			},
			request:  2,
			expected: false,
		},
		{
			name: "2 GPU(s) - request 2 - FAIL",
			gpuRes: GPUResource{
				Capacity:  2,
				Allocated: 2,
			},
			request:  1,
			expected: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, c.gpuRes.HasCapacityForRequest(c.request))
		})
	}
}

func TestToRegistration(t *testing.T) {
	inBackendGPUs := BackendGPUs{
		{
			Name: "A100",
			InstanceTypes: []InstanceType{
				{
					Name:            "ON-PREM.GPU.A100",
					FullName:        "NVIDIA-A100-SXM4-40GB",
					Description:     "Desc",
					CPU:             resource.MustParse("2"),
					SystemMemory:    resource.MustParse("2Gi"),
					GPUCount:        6,
					GPUMemoryPerGPU: resource.MustParse("40960Mi"),
					CPUArch:         "amd64",
					OS:              "Linux",
					DriverVersion:   "525.2.02",
					Storage:         resource.MustParse("512Gi"),
				},
				{
					Name:            "ON-PREM.GPU.A100",
					FullName:        "NVIDIA-A100-SXM4-80GB",
					Description:     "Desc",
					CPU:             resource.MustParse("2"),
					SystemMemory:    resource.MustParse("2Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("81920Mi"),
					CPUArch:         "amd64",
					OS:              "Linux",
					DriverVersion:   "525.2.02",
					Storage:         resource.MustParse("512Gi"),
				},
			},
		},
		{
			Name: "V100",
			InstanceTypes: []InstanceType{
				{
					Name:            "ON-PREM.GPU.V100",
					FullName:        "NVIDIA-V100-SXM2-32GB",
					Description:     "Desc",
					CPU:             resource.MustParse("2"),
					SystemMemory:    resource.MustParse("2Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("32768Mi"),
					CPUArch:         "amd64",
					OS:              "Linux",
					DriverVersion:   "525.2.02",
					Storage:         resource.MustParse("512Gi"),
				},
			},
		},
		{
			Name: "V100 no storage",
			InstanceTypes: []InstanceType{
				{
					Name:            "ON-PREM.GPU.V100",
					FullName:        "NVIDIA-V100-SXM2-32GB",
					Description:     "Desc",
					CPU:             resource.MustParse("2"),
					SystemMemory:    resource.MustParse("2Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("32768Mi"),
					CPUArch:         "amd64",
					OS:              "Linux",
					DriverVersion:   "525.2.02",
				},
			},
		},
		{
			Name: "L40",
			InstanceTypes: []InstanceType{
				{
					Name:            "ON-PREM.GPU.L40",
					FullName:        "NVIDIA-L40",
					Description:     "Desc",
					CPU:             resource.MustParse("2"),
					SystemMemory:    resource.MustParse("2Gi"),
					GPUCount:        2,
					GPUMemoryPerGPU: resource.MustParse("40960Mi"),
					CPUArch:         "amd64",
					OS:              "Linux",
					DriverVersion:   "525.2.02",
					Storage:         resource.MustParse("512Gi"),
					NodeCount:       3,
				},
			},
		},
	}

	expRegistrationGPUs := []RegistrationGPU{
		{
			Name: "A100",
			InstanceTypes: []RegistrationInstanceType{
				{
					Name:          "ON-PREM.GPU.A100_1x",
					Value:         "ON-PREM.GPU.A100",
					Description:   "Desc",
					Default:       true,
					CPUCores:      1,
					CPU:           "333m",
					SystemMemory:  "341Mi",
					GPUCount:      1,
					GPUMemory:     "40Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "85Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
				{
					Name:          "ON-PREM.GPU.A100_1x",
					Value:         "ON-PREM.GPU.A100",
					Description:   "Desc",
					Default:       false,
					CPUCores:      2,
					CPU:           "2",
					SystemMemory:  "2Gi",
					GPUCount:      1,
					GPUMemory:     "80Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "512Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
				{
					Name:          "ON-PREM.GPU.A100_2x",
					Value:         "ON-PREM.GPU.A100",
					Description:   "Desc",
					Default:       false,
					CPUCores:      1,
					CPU:           "666m",
					SystemMemory:  "682Mi",
					GPUCount:      2,
					GPUMemory:     "80Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "170Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
				{
					Name:          "ON-PREM.GPU.A100_4x",
					Value:         "ON-PREM.GPU.A100",
					Description:   "Desc",
					Default:       false,
					CPUCores:      2,
					CPU:           "1332m",
					SystemMemory:  "1365Mi",
					GPUCount:      4,
					GPUMemory:     "160Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "341Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
				{
					Name:          "ON-PREM.GPU.A100_6x",
					Value:         "ON-PREM.GPU.A100",
					Description:   "Desc",
					Default:       false,
					CPUCores:      2,
					CPU:           "2",
					SystemMemory:  "2Gi",
					GPUCount:      6,
					GPUMemory:     "240Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "512Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
			},
		},
		{
			Name: "V100",
			InstanceTypes: []RegistrationInstanceType{
				{
					Name:          "ON-PREM.GPU.V100_1x",
					Value:         "ON-PREM.GPU.V100",
					Description:   "Desc",
					Default:       true,
					CPUCores:      2,
					CPU:           "2",
					SystemMemory:  "2Gi",
					GPUCount:      1,
					GPUMemory:     "32Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "512Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
			},
		},
		{
			Name: "V100 no storage",
			InstanceTypes: []RegistrationInstanceType{
				{
					Name:          "ON-PREM.GPU.V100_1x",
					Value:         "ON-PREM.GPU.V100",
					Description:   "Desc",
					Default:       true,
					CPUCores:      2,
					CPU:           "2",
					SystemMemory:  "2Gi",
					GPUCount:      1,
					GPUMemory:     "32Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "0",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
			},
		},
		{
			Name: "L40",
			InstanceTypes: []RegistrationInstanceType{
				{
					Name:          "ON-PREM.GPU.L40_1x",
					Value:         "ON-PREM.GPU.L40",
					Description:   "Desc",
					Default:       true,
					CPUCores:      1,
					CPU:           "1",
					SystemMemory:  "1Gi",
					GPUCount:      1,
					GPUMemory:     "40Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "256Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
				{
					Name:          "ON-PREM.GPU.L40_2x",
					Value:         "ON-PREM.GPU.L40",
					Description:   "Desc",
					Default:       false,
					CPUCores:      2,
					CPU:           "2",
					SystemMemory:  "2Gi",
					GPUCount:      2,
					GPUMemory:     "80Gi",
					CPUArch:       "amd64",
					OS:            "Linux",
					DriverVersion: "525.2.02",
					Storage:       "512Gi",
					NodeType:      RegistrationInstanceTypeNodeTypeSingle,
				},
			},
		},
	}

	assert.Equal(t, expRegistrationGPUs, inBackendGPUs.ToRegistration(false, corev1.ResourceList{}))

	// Multinode instances.
	expRegistrationGPUs[len(expRegistrationGPUs)-1].InstanceTypes = append(
		expRegistrationGPUs[len(expRegistrationGPUs)-1].InstanceTypes,
		[]RegistrationInstanceType{
			{
				Name:          "ON-PREM.GPU.L40_2x.x2",
				Value:         "ON-PREM.GPU.L40",
				Description:   "Desc, 2 nodes",
				Default:       false,
				CPUCores:      4,
				CPU:           "4",
				SystemMemory:  "4Gi",
				GPUCount:      4,
				GPUMemory:     "160Gi",
				CPUArch:       "amd64",
				OS:            "Linux",
				DriverVersion: "525.2.02",
				Storage:       "1Ti",
				NodeType:      RegistrationInstanceTypeNodeTypeMulti,
				MaxInstances:  1,
			},
			{
				Name:          "ON-PREM.GPU.L40_2x.x3",
				Value:         "ON-PREM.GPU.L40",
				Description:   "Desc, 3 nodes",
				Default:       false,
				CPUCores:      6,
				CPU:           "6",
				SystemMemory:  "6Gi",
				GPUCount:      6,
				GPUMemory:     "240Gi",
				CPUArch:       "amd64",
				OS:            "Linux",
				DriverVersion: "525.2.02",
				Storage:       "1536Gi",
				NodeType:      RegistrationInstanceTypeNodeTypeMulti,
				MaxInstances:  1,
			},
		}...,
	)

	assert.Equal(t, expRegistrationGPUs, inBackendGPUs.ToRegistration(true, corev1.ResourceList{}))
}

func Test_calcFractionCPU(t *testing.T) {
	type spec struct {
		q         resource.Quantity
		numFactor uint64
		denom     *inf.Dec
		exp       string
	}

	cases := []spec{
		{
			q:         resource.MustParse("10"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "2500m",
		},
		{
			q:         resource.MustParse("1000"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "250",
		},
		{
			q:         resource.MustParse("10k"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "2500",
		},
		{
			q:         resource.MustParse("1000m"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "250m",
		},
		{
			q:         resource.MustParse("12345m"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "3086m",
		},
		{
			q:         resource.MustParse("12345"),
			numFactor: 1,
			denom:     inf.NewDec(4, 0),
			exp:       "3086250m",
		},
	}

	for _, tt := range cases {
		t.Run(tt.q.String(), func(t *testing.T) {
			got := calcFractionCPU(tt.q, tt.numFactor, tt.denom)
			assert.Equal(t, tt.exp, got.String())
		})
	}
}

func Test_roundToNearestByteSize(t *testing.T) {
	type spec struct {
		q   resource.Quantity
		exp string
	}

	cases := []spec{
		{
			q:   resource.MustParse("1023"),
			exp: "1023",
		},
		{
			q:   resource.MustParse("1025"),
			exp: "1025",
		},
		{
			q:   resource.MustParse("1023Ki"),
			exp: "1023Ki",
		},
		{
			q:   resource.MustParse("1025Ki"),
			exp: "1025Ki",
		},
		{
			q:   resource.MustParse("10250Ki"),
			exp: "10Mi",
		},
		{
			q:   resource.MustParse("12312091Ki"),
			exp: "11Gi",
		},
		{
			q:   resource.MustParse("1023Mi"),
			exp: "1023Mi",
		},
		{
			q:   resource.MustParse("1025Mi"),
			exp: "1025Mi",
		},
		{
			q:   resource.MustParse("1023Gi"),
			exp: "1023Gi",
		},
		{
			q:   resource.MustParse("1025Gi"),
			exp: "1025Gi",
		},
		{
			q:   resource.MustParse("1023Ti"),
			exp: "1023Ti",
		},
		{
			q:   resource.MustParse("1725Ti"),
			exp: "1725Ti",
		},
		{
			q:   resource.MustParse("1023Pi"),
			exp: "1023Pi",
		},
		{
			q:   resource.MustParse("1025Pi"),
			exp: "1025Pi",
		},
		{
			q:   resource.MustParse("1Ei"),
			exp: "1Ei",
		},
		{
			q:   resource.MustParse("8Ei"),
			exp: "8191Pi",
		},
		// Binary resources are not allowed to be > the max inf.Dec amount,
		// according to a "resource" package code comment.
		{
			q:   resource.MustParse("16Ei"),
			exp: "8191Pi",
		},
		{
			q:   resource.MustParse("1024Ki"),
			exp: "1Mi",
		},
		{
			q:   *resource.NewQuantity(366503875925, resource.BinarySI),
			exp: "341Gi",
		},
		{
			q:   resource.MustParse("1e30"),
			exp: "1e30",
		},
	}

	for _, tt := range cases {
		t.Run(fmt.Sprintf("%s to %s", tt.q.String(), tt.exp), func(t *testing.T) {
			got := roundToNearestByteSize(tt.q)
			assert.Equal(t, tt.exp, got)
		})
	}
}

func TestRegistrationInstanceTypeNodeTypeString(t *testing.T) {
	type spec struct {
		name     string
		nodeType RegistrationInstanceTypeNodeType
		expected string
	}

	cases := []spec{
		{
			name:     "single node type",
			nodeType: RegistrationInstanceTypeNodeTypeSingle,
			expected: "SINGLE",
		},
		{
			name:     "multi node type",
			nodeType: RegistrationInstanceTypeNodeTypeMulti,
			expected: "MULTI",
		},
		{
			name:     "empty node type",
			nodeType: "",
			expected: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, c.nodeType.String())
		})
	}
}

func TestBackendGPUsToRegistration(t *testing.T) {
	type spec struct {
		name                    string
		backendGPUs             BackendGPUs
		allowMultiNodeWorkloads bool
		infraOverhead           corev1.ResourceList
		expected                []RegistrationGPU
	}

	cases := []spec{
		{
			name:                    "empty backend GPUs",
			backendGPUs:             BackendGPUs{},
			allowMultiNodeWorkloads: false,
			expected:                []RegistrationGPU{},
		},
		{
			name: "single static GPU",
			backendGPUs: BackendGPUs{
				{
					Name: "A100",
					InstanceTypes: []InstanceType{
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							Description:     "Desc",
							CPU:             resource.MustParse("2"),
							SystemMemory:    resource.MustParse("2Gi"),
							GPUCount:        1,
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
						},
					},
					Static: true,
				},
			},
			allowMultiNodeWorkloads: false,
			expected: []RegistrationGPU{
				{
					Name: "A100",
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:          "ON-PREM.GPU.A100",
							Value:         "NVIDIA-A100-SXM4-40GB",
							Description:   "Desc",
							Default:       false,
							CPUCores:      2,
							CPU:           "2",
							SystemMemory:  "2Gi",
							GPUCount:      1,
							GPUMemory:     "40Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
						},
					},
				},
			},
		},
		{
			name: "single dynamic GPU with multi-node support",
			backendGPUs: BackendGPUs{
				{
					Name: "A100",
					InstanceTypes: []InstanceType{
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							Description:     "Desc",
							CPU:             resource.MustParse("2"),
							SystemMemory:    resource.MustParse("2Gi"),
							GPUCount:        2,
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
							NodeCount:       4,
						},
					},
					Static: false,
				},
			},
			allowMultiNodeWorkloads: true,
			expected: []RegistrationGPU{
				{
					Name: "A100",
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:          "ON-PREM.GPU.A100_1x",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc",
							Default:       true,
							CPUCores:      1,
							CPU:           "1",
							SystemMemory:  "1Gi",
							GPUCount:      1,
							GPUMemory:     "40Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "256Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  8,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc",
							Default:       false,
							CPUCores:      2,
							CPU:           "2",
							SystemMemory:  "2Gi",
							GPUCount:      2,
							GPUMemory:     "80Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  4,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x.x2",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc, 2 nodes",
							Default:       false,
							CPUCores:      4,
							CPU:           "4",
							SystemMemory:  "4Gi",
							GPUCount:      4,
							GPUMemory:     "160Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "1Ti",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  2,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x.x3",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc, 3 nodes",
							Default:       false,
							CPUCores:      6,
							CPU:           "6",
							SystemMemory:  "6Gi",
							GPUCount:      6,
							GPUMemory:     "240Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "1536Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  1,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x.x4",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc, 4 nodes",
							Default:       false,
							CPUCores:      8,
							CPU:           "8",
							SystemMemory:  "8Gi",
							GPUCount:      8,
							GPUMemory:     "320Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "2Ti",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  1,
						},
					},
				},
			},
		},
		{
			name: "dynamic with overhead",
			backendGPUs: BackendGPUs{
				{
					Name: "A100",
					InstanceTypes: []InstanceType{
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							Description:     "Desc",
							CPU:             resource.MustParse("2"),
							SystemMemory:    resource.MustParse("2Gi"),
							GPUCount:        2,
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
							NodeCount:       2,
						},
					},
				},
			},
			allowMultiNodeWorkloads: true,
			infraOverhead: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
			expected: []RegistrationGPU{
				{
					Name: "A100",
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:          "ON-PREM.GPU.A100_1x",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc",
							Default:       true,
							CPUCores:      1,
							CPU:           "800m",
							SystemMemory:  "924Mi",
							GPUCount:      1,
							GPUMemory:     "40Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "256Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  4,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc",
							Default:       false,
							CPUCores:      2,
							CPU:           "1800m",
							SystemMemory:  "1948Mi",
							GPUCount:      2,
							GPUMemory:     "80Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  2,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x.x2",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc, 2 nodes",
							Default:       false,
							CPUCores:      4,
							CPU:           "3800m",
							SystemMemory:  "3996Mi",
							GPUCount:      4,
							GPUMemory:     "160Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "1Ti",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  1,
						},
					},
				},
			},
		},
		{
			name: "dynamic with overhead excludes 1x instance type",
			backendGPUs: BackendGPUs{
				{
					Name: "A100",
					InstanceTypes: []InstanceType{
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							Description:     "Desc",
							CPU:             resource.MustParse("2"),
							SystemMemory:    resource.MustParse("2Gi"),
							GPUCount:        2,
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
							NodeCount:       2,
						},
					},
				},
			},
			allowMultiNodeWorkloads: true,
			infraOverhead: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
			expected: []RegistrationGPU{
				{
					Name: "A100",
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:          "ON-PREM.GPU.A100_2x",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc",
							Default:       true,
							CPUCores:      1,
							CPU:           "1",
							SystemMemory:  "1948Mi",
							GPUCount:      2,
							GPUMemory:     "80Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  2,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x.x2",
							Value:         "ON-PREM.GPU.A100",
							Description:   "Desc, 2 nodes",
							Default:       false,
							CPUCores:      3,
							CPU:           "3",
							SystemMemory:  "3996Mi",
							GPUCount:      4,
							GPUMemory:     "160Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "1Ti",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  1,
						},
					},
				},
			},
		},
		{
			name: "node with bad GPU",
			backendGPUs: BackendGPUs{
				{
					Name: "A100",
					InstanceTypes: []InstanceType{
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							CPU:             resource.MustParse("32"),
							SystemMemory:    resource.MustParse("256Gi"),
							GPUCount:        7, // Bad GPU
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
							NodeCount:       1,
						},
						{
							Name:            "ON-PREM.GPU.A100",
							FullName:        "NVIDIA-A100-SXM4-40GB",
							CPU:             resource.MustParse("32"),
							SystemMemory:    resource.MustParse("256Gi"),
							GPUCount:        8,
							GPUMemoryPerGPU: resource.MustParse("40960Mi"),
							CPUArch:         "amd64",
							OS:              "Linux",
							DriverVersion:   "525.2.02",
							Storage:         resource.MustParse("512Gi"),
							NodeCount:       2,
						},
					},
					Static: false,
				},
			},
			allowMultiNodeWorkloads: true,
			expected: []RegistrationGPU{
				{
					Name: "A100",
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:          "ON-PREM.GPU.A100_1x",
							Value:         "ON-PREM.GPU.A100",
							Default:       true,
							CPUCores:      4,
							CPU:           "4",
							SystemMemory:  "32Gi",
							GPUCount:      1,
							GPUMemory:     "40Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "64Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  23,
						},
						{
							Name:          "ON-PREM.GPU.A100_2x",
							Value:         "ON-PREM.GPU.A100",
							Default:       false,
							CPUCores:      8,
							CPU:           "8",
							SystemMemory:  "64Gi",
							GPUCount:      2,
							GPUMemory:     "80Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "128Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  11,
						},
						{
							Name:          "ON-PREM.GPU.A100_4x",
							Value:         "ON-PREM.GPU.A100",
							Default:       false,
							CPUCores:      16,
							CPU:           "16",
							SystemMemory:  "128Gi",
							GPUCount:      4,
							GPUMemory:     "160Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "256Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  5,
						},
						{
							Name:          "ON-PREM.GPU.A100_7x",
							Value:         "ON-PREM.GPU.A100",
							Default:       false,
							CPUCores:      32,
							CPU:           "32",
							SystemMemory:  "256Gi",
							GPUCount:      7,
							GPUMemory:     "280Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  3,
						},
						{
							Name:          "ON-PREM.GPU.A100_8x",
							Value:         "ON-PREM.GPU.A100",
							Default:       false,
							CPUCores:      32,
							CPU:           "32",
							SystemMemory:  "256Gi",
							GPUCount:      8,
							GPUMemory:     "320Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "512Gi",
							NodeType:      RegistrationInstanceTypeNodeTypeSingle,
							MaxInstances:  2,
						},
						{
							Name:          "ON-PREM.GPU.A100_8x.x2",
							Value:         "ON-PREM.GPU.A100",
							Default:       false,
							CPUCores:      64,
							CPU:           "64",
							SystemMemory:  "512Gi",
							GPUCount:      16,
							GPUMemory:     "640Gi",
							CPUArch:       "amd64",
							OS:            "Linux",
							DriverVersion: "525.2.02",
							Storage:       "1Ti",
							NodeType:      RegistrationInstanceTypeNodeTypeMulti,
							MaxInstances:  1,
						},
					},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := core.WithDefaultLogger(t.Context())
			log := core.GetLogger(ctx)
			// log.Logger.Level = logrus.DebugLevel
			ctx = core.WithLogger(ctx, log)

			nodes := backendGPUSToNodes(c.backendGPUs)
			actual := c.backendGPUs.ToRegistration(c.allowMultiNodeWorkloads, c.infraOverhead)
			actual, err := AddInstanceCapacity(ctx, actual, fakeNodeInformer{nodes: nodes}, nil)
			require.NoError(t, err)
			require.Equal(t, len(c.expected), len(actual))
			for i, expGPU := range c.expected {
				assert.Equal(t, expGPU.Name, actual[i].Name)
				if !assert.Equal(t, len(expGPU.InstanceTypes), len(actual[i].InstanceTypes)) {
					continue
				}
				for j, expIT := range expGPU.InstanceTypes {
					actualIT := actual[i].InstanceTypes[j]
					assert.Equal(t, expIT.Name, actualIT.Name, expIT.Name)
					assert.Equal(t, expIT.Value, actualIT.Value, expIT.Name)
					assert.Equal(t, expIT.Default, actualIT.Default, expIT.Name)
					assert.Equal(t, expIT.CPUCores, actualIT.CPUCores, expIT.Name)
					assert.Equal(t, expIT.CPU, actualIT.CPU, expIT.Name)
					assert.Equal(t, expIT.SystemMemory, actualIT.SystemMemory, expIT.Name)
					assert.Equal(t, expIT.GPUCount, actualIT.GPUCount, expIT.Name)
					assert.Equal(t, expIT.GPUMemory, actualIT.GPUMemory, expIT.Name)
					assert.Equal(t, expIT.CPUArch, actualIT.CPUArch, expIT.Name)
					assert.Equal(t, expIT.OS, actualIT.OS, expIT.Name)
					assert.Equal(t, expIT.DriverVersion, actualIT.DriverVersion, expIT.Name)
					assert.Equal(t, expIT.Storage, actualIT.Storage, expIT.Name)
					assert.Equal(t, expIT.NodeType, actualIT.NodeType, expIT.Name)
					assert.Equal(t, expIT.MaxInstances, actualIT.MaxInstances, expIT.Name)
					assert.Equal(t, expIT.UnschedulableCapacity, actualIT.UnschedulableCapacity, expIT.Name)
				}
			}
		})
	}
}

type fakeNodeInformer struct {
	nodes []*corev1.Node
}

func (ni fakeNodeInformer) List(selector labels.Selector) (ret []*corev1.Node, err error) {
	for _, node := range ni.nodes {
		if selector.Matches(labels.Set(node.Labels)) {
			ret = append(ret, node)
		}
	}
	return ret, nil
}

func (ni fakeNodeInformer) Get(name string) (*corev1.Node, error) {
	for _, node := range ni.nodes {
		if node.Name == name {
			return node, nil
		}
	}
	return nil, errors.NewNotFound(schema.GroupResource{Group: "v1", Resource: "nodes"}, name)
}

func backendGPUSToNodes(bgs BackendGPUs) (nodes []*corev1.Node) {
	for _, gpu := range bgs {
		for _, it := range gpu.InstanceTypes {
			baseNode := &corev1.Node{}
			baseNode.Labels = map[string]string{
				InstanceTypeLabel:        string(it.Name),
				"nvidia.com/gpu.present": "true",
			}
			baseNode.Status.Conditions = []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}}
			baseNode.Status.Allocatable = corev1.ResourceList{
				corev1.ResourceCPU:     it.CPU,
				corev1.ResourceMemory:  it.SystemMemory,
				"nvidia.com/gpu":       *resource.NewQuantity(int64(it.GPUCount), resource.DecimalSI),
				corev1.ResourceStorage: it.Storage,
			}
			for i := 0; i < int(it.NodeCount); i++ {
				node := baseNode.DeepCopy()
				node.Name = fmt.Sprintf("%s-node-%d-%d", strings.ToLower(it.FullName), it.GPUCount, i)
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "ready node",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					Unschedulable: false,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			want: true,
		},
		{
			name: "cordoned node",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not ready node",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					Unschedulable: false,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			want: false,
		},
		{
			name: "no ready condition",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					Unschedulable: false,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNodeReady(tt.node); got != tt.want {
				t.Errorf("IsNodeReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCalculateAvailability(t *testing.T) {
	tests := []struct {
		name          string
		capacity      corev1.ResourceList
		instResources corev1.ResourceList
		want          uint64
	}{
		{
			name: "sufficient resources",
			capacity: corev1.ResourceList{
				"nvidia.com/gpu":    resource.MustParse("4"),
				"cpu":               resource.MustParse("8"),
				"memory":            resource.MustParse("32Gi"),
				"ephemeral-storage": resource.MustParse("100Gi"),
			},
			instResources: corev1.ResourceList{
				"gpu": resource.MustParse("1"),
			},
			want: 4,
		},
		{
			name: "limited by GPU",
			capacity: corev1.ResourceList{
				"nvidia.com/gpu":    resource.MustParse("2"),
				"cpu":               resource.MustParse("8"),
				"memory":            resource.MustParse("32Gi"),
				"ephemeral-storage": resource.MustParse("100Gi"),
			},
			instResources: corev1.ResourceList{
				"gpu":     resource.MustParse("1"),
				"cpu":     resource.MustParse("2"),
				"memory":  resource.MustParse("8Gi"),
				"storage": resource.MustParse("25Gi"),
			},
			want: 2,
		},
		{
			name: "not limited by CPU",
			capacity: corev1.ResourceList{
				"nvidia.com/gpu":    resource.MustParse("4"),
				"cpu":               resource.MustParse("6"),
				"memory":            resource.MustParse("32Gi"),
				"ephemeral-storage": resource.MustParse("100Gi"),
			},
			instResources: corev1.ResourceList{
				"gpu":     resource.MustParse("1"),
				"cpu":     resource.MustParse("2"),
				"memory":  resource.MustParse("8Gi"),
				"storage": resource.MustParse("25Gi"),
			},
			want: 4,
		},
		{
			name: "no GPU resources",
			capacity: corev1.ResourceList{
				"cpu":               resource.MustParse("8"),
				"memory":            resource.MustParse("32Gi"),
				"ephemeral-storage": resource.MustParse("100Gi"),
			},
			instResources: corev1.ResourceList{
				"gpu":     resource.MustParse("1"),
				"cpu":     resource.MustParse("2"),
				"memory":  resource.MustParse("8Gi"),
				"storage": resource.MustParse("25Gi"),
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, calculateAvailability(tt.capacity, tt.instResources["gpu"]))
		})
	}
}

func TestMultiplierMultiNode(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		want         string
		expNodeCount int
	}{
		{
			name:         "no multiplier",
			input:        "ON-PREM.GPU.A100",
			want:         "ON-PREM.GPU.A100",
			expNodeCount: 1,
		},
		{
			name:         "multiplier",
			input:        "ON-PREM.GPU.A100_2x",
			want:         "ON-PREM.GPU.A100",
			expNodeCount: 1,
		},
		{
			name:         "multiplier with node count",
			input:        "ON-PREM.GPU.A100_2x.x2",
			want:         "ON-PREM.GPU.A100",
			expNodeCount: 2,
		},
		{
			name:         "multiplier with node count",
			input:        "ON-PREM.GPU.A100_2x.x212314212",
			want:         "ON-PREM.GPU.A100",
			expNodeCount: 212314212,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			itn := InstanceName(tt.input)
			assert.Equal(t, tt.want, itn.WithoutMultiplierMultiNode())
			assert.Equal(t, tt.expNodeCount, itn.GetNodeCount())
		})
	}
}

func TestAddInstanceAvailability(t *testing.T) {
	// Create a mock node lister
	mockNodeLister := new(mockNodeLister)
	mockNodeLister.On("List", mock.Anything).Return([]*corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-1",
				Labels: map[string]string{
					InstanceTypeLabel: "ON-PREM.GPU.A100",
				},
			},
			Spec: corev1.NodeSpec{
				Unschedulable: false,
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
				Capacity: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("4"),
					corev1.ResourceCPU:              resource.MustParse("8"),
					corev1.ResourceMemory:           resource.MustParse("64Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
				},
				Allocatable: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("4"),
					corev1.ResourceCPU:              resource.MustParse("8"),
					corev1.ResourceMemory:           resource.MustParse("64Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-2",
				Labels: map[string]string{
					InstanceTypeLabel: "ON-PREM.GPU.A100",
				},
			},
			Spec: corev1.NodeSpec{
				Unschedulable: false,
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
				Capacity: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("4"),
					corev1.ResourceCPU:              resource.MustParse("8"),
					corev1.ResourceMemory:           resource.MustParse("64Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
				},
				// only half of capacity is allocatable
				Allocatable: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("2"),
					corev1.ResourceCPU:              resource.MustParse("8"),
					corev1.ResourceMemory:           resource.MustParse("32Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("50Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-3",
				Labels: map[string]string{
					InstanceTypeLabel: "ON-PREM.GPU.L40",
				},
			},
			Spec: corev1.NodeSpec{
				Unschedulable: false,
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
				Capacity: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("2"),
					corev1.ResourceCPU:              resource.MustParse("4"),
					corev1.ResourceMemory:           resource.MustParse("16Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("25Gi"),
				},
				Allocatable: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("2"),
					corev1.ResourceCPU:              resource.MustParse("4"),
					corev1.ResourceMemory:           resource.MustParse("16Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("25Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-4",
				Labels: map[string]string{
					InstanceTypeLabel: "ON-PREM.GPU.L40",
				},
			},
			Spec: corev1.NodeSpec{
				Unschedulable: false,
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
				Capacity: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("2"),
					corev1.ResourceCPU:              resource.MustParse("4"),
					corev1.ResourceMemory:           resource.MustParse("16Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("25Gi"),
				},
				// no resources are allocatable
				Allocatable: corev1.ResourceList{
					GPUResourceKey:                  resource.MustParse("0"),
					corev1.ResourceCPU:              resource.MustParse("0"),
					corev1.ResourceMemory:           resource.MustParse("0Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("0Gi"),
				},
			},
		},
	}, nil)

	tests := []struct {
		name             string
		gpus             []RegistrationGPU
		sharedClusterOn  *atomic.Bool
		wantMaxInstances []uint64
		wantErr          bool
	}{
		{
			name: "A100",
			gpus: []RegistrationGPU{
				{
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:         "ON-PREM.GPU.A100_1x",
							CPU:          "2",
							SystemMemory: "16Gi",
							Storage:      "25Gi",
							GPUCount:     1,
						},
						{
							Name:         "ON-PREM.GPU.A100_2x",
							CPU:          "4",
							SystemMemory: "32Gi",
							Storage:      "50Gi",
							GPUCount:     2,
						},
						{
							Name:         "ON-PREM.GPU.A100_4x",
							CPU:          "8",
							SystemMemory: "64Gi",
							Storage:      "100Gi",
							GPUCount:     4,
						},
						{
							Name:         "ON-PREM.GPU.A100_8x",
							CPU:          "16",
							SystemMemory: "128Gi",
							Storage:      "200Gi",
							GPUCount:     8,
						},
					},
				},
			},
			sharedClusterOn:  nil,
			wantMaxInstances: []uint64{6, 3, 1, 0},
			wantErr:          false,
		},
		{
			name: "L40 full node",
			gpus: []RegistrationGPU{
				{
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:         "ON-PREM.GPU.L40_2x",
							CPU:          "4",
							SystemMemory: "16Gi",
							Storage:      "25Gi",
							GPUCount:     2,
						},
					},
				},
			},
			sharedClusterOn:  nil,
			wantMaxInstances: []uint64{1},
			wantErr:          false,
		},
		{
			name: "V100 not available",
			gpus: []RegistrationGPU{
				{
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:         "ON-PREM.GPU.V100_1x",
							CPU:          "2",
							SystemMemory: "2Gi",
							Storage:      "512Gi",
							GPUCount:     1,
						},
					},
				},
			},
			sharedClusterOn:  nil,
			wantMaxInstances: []uint64{0},
			wantErr:          false,
		},
		{
			name: "shared cluster mode",
			gpus: []RegistrationGPU{
				{
					InstanceTypes: []RegistrationInstanceType{
						{
							Name:         "ON-PREM.GPU.A100_1x",
							CPU:          "2",
							SystemMemory: "16Gi",
							Storage:      "25Gi",
							GPUCount:     1,
						},
					},
				},
			},
			sharedClusterOn:  &atomic.Bool{},
			wantMaxInstances: []uint64{0},
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sharedClusterOn != nil {
				tt.sharedClusterOn.Store(true)
			}

			got, err := AddInstanceCapacity(context.Background(), tt.gpus, mockNodeLister, tt.sharedClusterOn)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, len(got[0].InstanceTypes), len(tt.wantMaxInstances))
			for i, it := range got[0].InstanceTypes {
				assert.Equalf(t, tt.wantMaxInstances[i], it.MaxInstances, "MaxInstances mismatch for %s, idx %v", it.Name, i)
			}
		})
	}
}

// mockNodeLister implements the NodeLister interface for testing
type mockNodeLister struct {
	mock.Mock
}

func (m *mockNodeLister) List(selector labels.Selector) ([]*corev1.Node, error) {
	args := m.Called(selector)
	return args.Get(0).([]*corev1.Node), args.Error(1)
}

func (m *mockNodeLister) Get(val string) (*corev1.Node, error) {
	args := m.Called(val)
	return args.Get(0).(*corev1.Node), args.Error(1)
}
