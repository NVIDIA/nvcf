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

package fakenodefeatures

import (
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type Client struct {
	BackendGPUs []types.BackendGPU
	ResourceSet types.GPUResourceSet
}

func (c *Client) GetAllBackendGPUs(_ context.Context) ([]types.BackendGPU, error) {
	return c.BackendGPUs, nil
}

func (c *Client) GetGPUResources(_ context.Context, tn types.GPUName) (types.GPUResource, error) {
	if c.ResourceSet == nil || c.ResourceSet[tn] == (types.GPUResource{}) {
		return types.GPUResource{}, fmt.Errorf("gpu not found: %s", tn)
	}
	return c.ResourceSet[tn], nil
}
