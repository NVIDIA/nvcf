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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	ConfigMapName      = "nvca-config"
	ConfigMapGPUsKey   = "gpus"
	staticGPUCacheTime = 5 * time.Minute
)

type staticClient struct {
	clients          *kubeclients.KubeClients
	systemNamespace  string
	gpuAllocGetter   GPUAllocationGetter
	constGPUCapacity uint64
	backendGPUs      []types.BackendGPU
	lastCachedTime   time.Time
}

func NewStaticClient(clients *kubeclients.KubeClients, ag GPUAllocationGetter, systemNamespace string, gpuCap uint64) Client {
	c := &staticClient{
		clients:          clients,
		systemNamespace:  systemNamespace,
		gpuAllocGetter:   ag,
		constGPUCapacity: gpuCap,
	}
	return c
}

func (c *staticClient) GetAllBackendGPUs(ctx context.Context) ([]types.BackendGPU, error) {
	log := core.GetLogger(ctx)

	log.Debug("Gathering backend GPU info from ConfigMap")

	cm, err := c.clients.K8s.CoreV1().ConfigMaps(c.systemNamespace).Get(ctx,
		ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get GPU instance ConfigMap: %v", err)
	}

	// This method uses internal types for static instance type configs because
	// the registration type sent to ICMS may change but the static representation
	// must be backwards-compatible.
	type registrationInstanceType struct {
		Name          string                                 `json:"name,omitempty"`
		Value         string                                 `json:"value,omitempty"`
		Description   string                                 `json:"description,omitempty"`
		Default       bool                                   `json:"default,omitempty"`
		CPUCores      int                                    `json:"cpuCores"`
		SystemMemory  string                                 `json:"systemMemory,omitempty"`
		GPUMemory     string                                 `json:"gpuMemory,omitempty"`
		GPUCount      uint64                                 `json:"gpuCount"`
		CPUArch       string                                 `json:"cpuArch,omitempty"`
		Storage       string                                 `json:"storage,omitempty"`
		OS            string                                 `json:"os,omitempty"`
		DriverVersion string                                 `json:"driverVersion,omitempty"`
		NodeType      types.RegistrationInstanceTypeNodeType `json:"instanceTypeNodeType"`
	}

	type registrationGPU struct {
		Name          string                     `json:"name,omitempty"`
		Capacity      uint64                     `json:"capacity,omitempty"`
		InstanceTypes []registrationInstanceType `json:"instanceTypes,omitempty"`
	}

	regGPUs := []registrationGPU{}

	gpuData := cm.Data[ConfigMapGPUsKey]
	err = json.Unmarshal([]byte(gpuData), &regGPUs)
	if err != nil {
		log.WithError(err).Errorf("Decode GPU instance data from ConfigMap (%v)", gpuData)
		return nil, fmt.Errorf("decode GPU instance data from ConfigMap: %v", err)
	}

	// TODO: cache this.
	backendGPUs := make([]types.BackendGPU, len(regGPUs))
	for i, regGPU := range regGPUs {
		backendGPUs[i].Name = types.GPUName(regGPU.Name)
		backendGPUs[i].Capacity = regGPU.Capacity
		backendGPUs[i].Static = true
		backendGPUs[i].InstanceTypes = make([]types.InstanceType, len(regGPU.InstanceTypes))
		for j, regInstanceType := range regGPU.InstanceTypes {
			gpuMemory, err := resource.ParseQuantity(regInstanceType.GPUMemory)
			if err != nil {
				return nil, fmt.Errorf("parse instance type %s gpu memory %q: %w",
					regInstanceType.Name, regInstanceType.GPUMemory, err)
			}
			systemMemory, err := resource.ParseQuantity(regInstanceType.SystemMemory)
			if err != nil {
				return nil, fmt.Errorf("parse instance type %s system memory %q: %w",
					regInstanceType.Name, regInstanceType.SystemMemory, err)
			}
			var storagePerIT resource.Quantity
			if regInstanceType.Storage != "" {
				storagePerIT, err = resource.ParseQuantity(regInstanceType.Storage)
				if err != nil {
					return nil, fmt.Errorf("parse instance type %s storage %q: %w",
						regInstanceType.Name, regInstanceType.Storage, err)
				}
			}
			cpuCores := resource.NewQuantity(int64(regInstanceType.CPUCores), resource.DecimalSI)
			backendGPUs[i].InstanceTypes[j] = types.InstanceType{
				Name: types.InstanceName(regInstanceType.Name),
				// Since the static GPU config historically does not contain FullName,
				// set it to Value so the registration type conversion can deduplicate instances.
				FullName:        regInstanceType.Value,
				Description:     regInstanceType.Description,
				Default:         regInstanceType.Default,
				CPU:             *cpuCores,
				SystemMemory:    systemMemory,
				GPUCount:        regInstanceType.GPUCount,
				GPUMemoryPerGPU: gpuMemory,
				OS:              regInstanceType.OS,
				DriverVersion:   regInstanceType.DriverVersion,
				CPUArch:         regInstanceType.CPUArch,
				Storage:         storagePerIT,
			}
		}
	}

	return backendGPUs, nil
}

func (c *staticClient) GetGPUResources(ctx context.Context, gpuName types.GPUName) (types.GPUResource, error) {
	log := core.GetLogger(ctx)
	allocated, err := c.gpuAllocGetter.GetForGPU(ctx, gpuName)
	if err != nil {
		return types.GPUResource{}, fmt.Errorf("get allocated GPU resources: %v", err)
	}

	// query the API once and cache it for StaticGPUCacheTime duration
	if c.lastCachedTime.IsZero() || time.Since(c.lastCachedTime) >= staticGPUCacheTime {
		c.backendGPUs, err = c.GetAllBackendGPUs(ctx)
		if err != nil {
			return types.GPUResource{}, fmt.Errorf("failed to GetAllBackendGPUs: %v", err)
		}
		// update timeStamp
		c.lastCachedTime = time.Now()
	}

	var gpuCap uint64

	for _, g := range c.backendGPUs {
		if strings.EqualFold(string(g.Name), string(gpuName)) {
			gpuCap = g.Capacity
			break
		}
	}

	if gpuCap == 0 {
		log.Warnf("failed to get per GPU capacity from config for %v, return global capacity", gpuName)
		gpuCap = c.constGPUCapacity
	}

	return types.GPUResource{
		Capacity:  gpuCap,
		Allocated: allocated,
	}, nil
}
