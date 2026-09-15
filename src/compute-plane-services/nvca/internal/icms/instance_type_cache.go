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

package icms

import (
	"sync"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// For fetching instance type info.
type RegistrationInstanceTypeCache interface {
	Get(string) (types.RegistrationInstanceType, bool)
	Put([]types.RegistrationGPU)
}

func NewRegistrationInstanceTypeCache() RegistrationInstanceTypeCache {
	return &registrationInstanceTypeCache{
		v: map[string]types.RegistrationInstanceType{},
	}
}

// registrationInstanceTypeCache caches instance types by their name (with _{d}x suffix).
type registrationInstanceTypeCache struct {
	v  map[string]types.RegistrationInstanceType
	mu sync.RWMutex
}

func (c *registrationInstanceTypeCache) Get(name string) (types.RegistrationInstanceType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.v[name]
	return t, ok
}

func (c *registrationInstanceTypeCache) Put(in []types.RegistrationGPU) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := map[string]types.RegistrationInstanceType{}
	for _, gpu := range in {
		for _, instanceType := range gpu.InstanceTypes {
			v[instanceType.Name] = instanceType
		}
	}
	c.v = v
}
