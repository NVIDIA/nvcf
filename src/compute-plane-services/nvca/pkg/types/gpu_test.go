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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGPUResource(t *testing.T) {
	tests := []struct {
		name     string
		resource GPUResource
		want     uint64
	}{
		{
			name: "normal case",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 30,
			},
			want: 70,
		},
		{
			name: "fully allocated",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 100,
			},
			want: 0,
		},
		{
			name: "over allocated",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 150,
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resource.Available(); got != tt.want {
				t.Errorf("Available() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGPUResource_HasCapacityForRequest(t *testing.T) {
	tests := []struct {
		name     string
		resource GPUResource
		request  uint64
		want     bool
	}{
		{
			name: "has capacity",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 30,
			},
			request: 50,
			want:    true,
		},
		{
			name: "no capacity",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 80,
			},
			request: 30,
			want:    false,
		},
		{
			name: "exact capacity",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 50,
			},
			request: 50,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resource.HasCapacityForRequest(tt.request); got != tt.want {
				t.Errorf("HasCapacityForRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGPUResource_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		resource GPUResource
		want     string
	}{
		{
			name: "normal case",
			resource: GPUResource{
				Capacity:  100,
				Allocated: 30,
			},
			want: `{"available":70,"capacity":100,"allocated":30}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.resource)
			if err != nil {
				t.Errorf("MarshalJSON() error = %v", err)
				return
			}
			if string(got) != tt.want {
				t.Errorf("MarshalJSON() = %v, want %v", string(got), tt.want)
			}
		})
	}
}

func TestGPUResourceList_GetGPUCount(t *testing.T) {
	tests := []struct {
		name      string
		resources GPUResourceList
		names     []string
		want      uint64
		wantFound bool
	}{
		{
			name: "single GPU",
			resources: GPUResourceList{
				"gpu": resource.MustParse("2"),
			},
			names:     []string{"gpu", "pgpu"},
			want:      2,
			wantFound: true,
		},
		{
			name: "multiple GPUs",
			resources: GPUResourceList{
				"gpu":  resource.MustParse("2"),
				"pgpu": resource.MustParse("1"),
			},
			names:     []string{"gpu", "pgpu"},
			want:      3,
			wantFound: true,
		},
		{
			name: "no matching GPUs",
			resources: GPUResourceList{
				"gpu": resource.MustParse("2"),
			},
			names:     []string{"unknown"},
			want:      0,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := tt.resources.GetGPUCount(tt.names...)
			if got != tt.want || found != tt.wantFound {
				t.Errorf("GetGPUCount() = (%v, %v), want (%v, %v)", got, found, tt.want, tt.wantFound)
			}
		})
	}
}

func TestGPUResourceList_GPU(t *testing.T) {
	tests := []struct {
		name      string
		resources GPUResourceList
		want      *resource.Quantity
		wantFound bool
	}{
		{
			name: "has gpu",
			resources: GPUResourceList{
				"gpu": resource.MustParse("2"),
			},
			want:      resource.NewQuantity(2, resource.DecimalSI),
			wantFound: true,
		},
		{
			name: "has pgpu",
			resources: GPUResourceList{
				"pgpu": resource.MustParse("1"),
			},
			want:      resource.NewQuantity(1, resource.DecimalSI),
			wantFound: true,
		},
		{
			name: "has both gpu and pgpu",
			resources: GPUResourceList{
				"gpu":  resource.MustParse("2"),
				"pgpu": resource.MustParse("1"),
			},
			want:      resource.NewQuantity(3, resource.DecimalSI),
			wantFound: true,
		},
		{
			name:      "no GPU",
			resources: GPUResourceList{},
			want:      nil,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := tt.resources.GPU()
			if found != tt.wantFound {
				t.Errorf("GPU() found = %v, want %v", found, tt.wantFound)
				return
			}
			if found && !got.Equal(*tt.want) {
				t.Errorf("GPU() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGPUResourceList_HasMIG(t *testing.T) {
	tests := []struct {
		name      string
		resources GPUResourceList
		want      bool
	}{
		{
			name: "has MIG",
			resources: GPUResourceList{
				"mig-1g.5gb": resource.MustParse("1"),
			},
			want: true,
		},
		{
			name: "no MIG",
			resources: GPUResourceList{
				"gpu": resource.MustParse("2"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resources.HasMIG(); got != tt.want {
				t.Errorf("HasMIG() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGPUResourceList_HasGenericPGPU(t *testing.T) {
	tests := []struct {
		name      string
		resources GPUResourceList
		want      bool
	}{
		{
			name: "has pgpu",
			resources: GPUResourceList{
				"pgpu": resource.MustParse("1"),
			},
			want: true,
		},
		{
			name: "no pgpu",
			resources: GPUResourceList{
				"gpu": resource.MustParse("2"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resources.HasGenericPGPU(); got != tt.want {
				t.Errorf("HasGenericPGPU() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseGPUResourceList(t *testing.T) {
	tests := []struct {
		name string
		rl   corev1.ResourceList
		want GPUResourceList
	}{
		{
			name: "normal case",
			rl: corev1.ResourceList{
				"nvidia.com/gpu":  resource.MustParse("2"),
				"nvidia.com/pgpu": resource.MustParse("1"),
			},
			want: GPUResourceList{
				"gpu":  resource.MustParse("2"),
				"pgpu": resource.MustParse("1"),
			},
		},
		{
			name: "no GPU resources",
			rl:   corev1.ResourceList{},
			want: GPUResourceList{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGPUResourceList(tt.rl)
			if len(got) != len(tt.want) {
				t.Errorf("ParseGPUResourceList() length = %v, want %v", len(got), len(tt.want))
				return
			}
			for k, v := range got {
				if !v.Equal(tt.want[k]) {
					t.Errorf("ParseGPUResourceList() resource %v = %v, want %v", k, v, tt.want[k])
				}
			}
		})
	}
}

func TestGetGPUNameFromInstanceType(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		want         GPUName
		wantErr      bool
	}{
		{
			name:         "valid instance type",
			instanceType: "ON-PREM.GPU.A100",
			want:         "A100",
			wantErr:      false,
		},
		{
			name:         "invalid instance type",
			instanceType: "ON-PREM.GPU",
			want:         "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetGPUNameFromInstanceType(tt.instanceType)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetGPUNameFromInstanceType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetGPUNameFromInstanceType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseGPUName(t *testing.T) {
	type spec struct {
		name        string
		gpuLabelVal string
		expGPUName  string
		expError    string
	}

	cases := []spec{
		{
			name:     "empty",
			expError: "empty GPU name",
		},
		{
			name:        "a100",
			gpuLabelVal: "NVIDIA-A100-SXM4-40GB",
			expGPUName:  "A100",
		},
		{
			name:        "rtx",
			gpuLabelVal: "RTX-2080-Ti-8G",
			expGPUName:  "RTX2080Ti",
		},
		{
			name:        "gtx",
			gpuLabelVal: "GTX-1080-8G",
			expGPUName:  "GTX1080",
		},
		{
			name:        "geforce",
			gpuLabelVal: "GEFORCE-RTX-4080-TI-32GB",
			expGPUName:  "RTX4080Ti",
		},
		{
			name:        "nv geforce",
			gpuLabelVal: "NVIDIA-GeForce-RTX-3070-Ti",
			expGPUName:  "RTX3070Ti",
		},
		{
			name:        "nv geforce",
			gpuLabelVal: "TU106-GeForce-RTX-2070-Rev.-A",
			expGPUName:  "RTX2070",
		},
		{
			name:        "nv rtx pro",
			gpuLabelVal: "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition",
			expGPUName:  "RTXPRO6000",
		},
		{
			name:        "mig 2g.48gb",
			gpuLabelVal: "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition-MIG-2g.48gb",
			expGPUName:  "RTXPRO6000-MIG-2g-48gb",
		},
		{
			name:        "mig 1g.24gb",
			gpuLabelVal: "NVIDIA-RTX-PRO-6000-Blackwell-Server-Edition-MIG-1g.24gb",
			expGPUName:  "RTXPRO6000-MIG-1g-24gb",
		},
		{
			name:        "h200 mig 7g.141gb",
			gpuLabelVal: "NVIDIA-H200-MIG-7g.141gb",
			expGPUName:  "H200-MIG-7g-141gb",
		},
		{
			name:        "mig mixed case",
			gpuLabelVal: "NVIDIA-H200-mig-7G.141GB",
			expGPUName:  "H200-MIG-7g-141gb",
		},
		{
			name:        "mig hyphen separator",
			gpuLabelVal: "NVIDIA-H200-MIG-3g-40gb",
			expGPUName:  "H200-MIG-3g-40gb",
		},
		{
			name:        "malformed mig falls through",
			gpuLabelVal: "NVIDIA-H200-MIG-bad",
			expGPUName:  "H200",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotGPUName, err := ParseGPUName(c.gpuLabelVal)
			if len(c.expError) > 0 {
				assert.EqualError(t, err, c.expError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, GPUName(c.expGPUName), gotGPUName)
			}
		})
	}
}
